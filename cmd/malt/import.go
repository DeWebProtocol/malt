package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	daemonclient "github.com/dewebprotocol/malt/client"
	"github.com/dewebprotocol/malt/httpapi"
	cid "github.com/ipfs/go-cid"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(importCmd)
	importCmd.AddCommand(importFileCmd)
	importCmd.AddCommand(importDirCmd)

	importCmd.PersistentFlags().StringVar(&importGraphID, "graph", "", "Import into a managed graph head (created automatically if missing)")
	importCmd.PersistentFlags().StringVar(&importRootID, "root", "", "Import into an existing explicit root instead of creating a fresh root")
}

var (
	importGraphID string
	importRootID  string
)

var importCmd = &cobra.Command{
	Use:   "import",
	Short: "Upload local payloads and attach them to MALT structure in one step",
}

var importFileCmd = &cobra.Command{
	Use:   "file <local-path> [<malt-path>]",
	Short: "Import one file into CAS and attach its CID to MALT",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runImportFile,
}

var importDirCmd = &cobra.Command{
	Use:   "dir <local-dir> [<malt-prefix>]",
	Short: "Import a directory tree into CAS and attach relative paths to MALT",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runImportDir,
}

type importEntry struct {
	LocalPath string
	MaltPath  string
}

type importTarget struct {
	GraphID string
	RootID  string
}

type importSummary struct {
	Scope        string `json:"scope"`
	Graph        string `json:"graph,omitempty"`
	PreviousRoot string `json:"previous_root,omitempty"`
	Root         string `json:"root"`
	Files        int    `json:"files"`
	Bytes        int64  `json:"bytes"`
}

type casUploader interface {
	Put(ctx context.Context, data []byte) (cid.Cid, error)
}

type importBackend interface {
	GetGraph(ctx context.Context, id string) (*httpapi.Graph, error)
	CreateGraph(ctx context.Context, id string, backend string) (*httpapi.Graph, error)
	CreateGraphStructure(ctx context.Context, id string, arcs map[string]string) (*httpapi.CreateStructureResponse, error)
	BatchUpdateGraph(ctx context.Context, id string, updates map[string]string) (*httpapi.WriteBatchResponse, error)
	CreateRootStructure(ctx context.Context, arcs map[string]string) (*httpapi.CreateStructureResponse, error)
	BatchUpdateRoot(ctx context.Context, root string, updates map[string]string) (*httpapi.WriteBatchResponse, error)
}

func runImportFile(cmd *cobra.Command, args []string) error {
	target := importTarget{GraphID: importGraphID, RootID: importRootID}
	if err := validateImportTarget(target); err != nil {
		return err
	}

	maltPath := ""
	if len(args) > 1 {
		maltPath = args[1]
	}

	entry, err := buildFileImportEntry(args[0], maltPath)
	if err != nil {
		return err
	}
	return runImport(cmd.Context(), []importEntry{entry}, target)
}

func runImportDir(cmd *cobra.Command, args []string) error {
	target := importTarget{GraphID: importGraphID, RootID: importRootID}
	if err := validateImportTarget(target); err != nil {
		return err
	}

	prefix := ""
	if len(args) > 1 {
		prefix = args[1]
	}

	entries, err := buildDirectoryImportEntries(args[0], prefix)
	if err != nil {
		return err
	}
	return runImport(cmd.Context(), entries, target)
}

func runImport(ctx context.Context, entries []importEntry, target importTarget) error {
	casClient, err := makeCASClient()
	if err != nil {
		return err
	}
	daemon := mustDaemonClient()

	arcs, totalBytes, err := uploadImportEntries(ctx, casClient, entries)
	if err != nil {
		return err
	}

	result, err := applyImportedArcs(ctx, daemon, target, arcs)
	if err != nil {
		return daemonCommandError(err)
	}

	result.Files = len(entries)
	result.Bytes = totalBytes
	printJSON(result)

	switch {
	case result.Graph != "":
		fmt.Fprintf(os.Stderr, "imported %d file(s) into graph %q\n", result.Files, result.Graph)
	case result.PreviousRoot != "":
		fmt.Fprintf(os.Stderr, "imported %d file(s) and advanced root\n", result.Files)
	default:
		fmt.Fprintf(os.Stderr, "imported %d file(s) into a fresh root\n", result.Files)
	}
	return nil
}

func validateImportTarget(target importTarget) error {
	if target.GraphID != "" && target.RootID != "" {
		return fmt.Errorf("--graph and --root are mutually exclusive")
	}
	return nil
}

func buildFileImportEntry(localPath string, overridePath string) (importEntry, error) {
	info, err := os.Stat(localPath)
	if err != nil {
		return importEntry{}, fmt.Errorf("stat file: %w", err)
	}
	if info.IsDir() {
		return importEntry{}, fmt.Errorf("%q is a directory; use `malt import dir`", localPath)
	}
	if !info.Mode().IsRegular() {
		return importEntry{}, fmt.Errorf("%q is not a regular file", localPath)
	}

	targetPath := overridePath
	if targetPath == "" {
		targetPath = filepath.Base(localPath)
	}
	targetPath = normalizeImportPath(targetPath)
	if targetPath == "" {
		return importEntry{}, fmt.Errorf("malt path must not be empty")
	}

	absPath, err := filepath.Abs(localPath)
	if err != nil {
		return importEntry{}, fmt.Errorf("resolve file path: %w", err)
	}

	return importEntry{
		LocalPath: absPath,
		MaltPath:  targetPath,
	}, nil
}

func buildDirectoryImportEntries(localDir string, prefix string) ([]importEntry, error) {
	root, err := filepath.Abs(localDir)
	if err != nil {
		return nil, fmt.Errorf("resolve directory path: %w", err)
	}

	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%q is not a directory", localDir)
	}

	entries := make([]importEntry, 0)
	seen := make(map[string]string)
	err = filepath.WalkDir(root, func(current string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return fmt.Errorf("symlink import is not supported: %s", current)
		}

		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", current, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular file is not supported: %s", current)
		}

		rel, err := filepath.Rel(root, current)
		if err != nil {
			return fmt.Errorf("compute relative path for %s: %w", current, err)
		}

		maltPath := joinImportPath(prefix, rel)
		if previous, ok := seen[maltPath]; ok {
			return fmt.Errorf("duplicate import target path %q from %q and %q", maltPath, previous, current)
		}
		seen[maltPath] = current
		entries = append(entries, importEntry{
			LocalPath: current,
			MaltPath:  maltPath,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("directory %q does not contain any regular files", localDir)
	}

	slices.SortFunc(entries, func(a, b importEntry) int {
		return strings.Compare(a.MaltPath, b.MaltPath)
	})
	return entries, nil
}

func normalizeImportPath(raw string) string {
	cleaned := strings.TrimSpace(raw)
	cleaned = strings.ReplaceAll(cleaned, "\\", "/")
	cleaned = path.Clean("/" + cleaned)
	cleaned = strings.TrimPrefix(cleaned, "/")
	if cleaned == "." {
		return ""
	}
	return cleaned
}

func joinImportPath(prefix string, rel string) string {
	normalizedRel := normalizeImportPath(filepath.ToSlash(rel))
	if prefix == "" {
		return normalizedRel
	}
	return normalizeImportPath(path.Join(prefix, normalizedRel))
}

func uploadImportEntries(ctx context.Context, casClient casUploader, entries []importEntry) (map[string]string, int64, error) {
	arcs := make(map[string]string, len(entries))
	var totalBytes int64

	for _, entry := range entries {
		data, err := os.ReadFile(entry.LocalPath)
		if err != nil {
			return nil, 0, fmt.Errorf("read %s: %w", entry.LocalPath, err)
		}
		blockCID, err := casClient.Put(ctx, data)
		if err != nil {
			return nil, 0, fmt.Errorf("upload %s to CAS: %w", entry.LocalPath, err)
		}

		if _, exists := arcs[entry.MaltPath]; exists {
			return nil, 0, fmt.Errorf("duplicate imported arc path %q", entry.MaltPath)
		}
		arcs[entry.MaltPath] = blockCID.String()
		totalBytes += int64(len(data))
	}

	return arcs, totalBytes, nil
}

func applyImportedArcs(ctx context.Context, daemon importBackend, target importTarget, arcs map[string]string) (*importSummary, error) {
	if len(arcs) == 0 {
		return nil, fmt.Errorf("import requires at least one arc")
	}

	if target.GraphID != "" {
		return applyImportedGraphArcs(ctx, daemon, target.GraphID, arcs)
	}
	if target.RootID != "" {
		resp, err := daemon.BatchUpdateRoot(ctx, target.RootID, arcs)
		if err != nil {
			return nil, err
		}
		return &importSummary{
			Scope:        "root",
			PreviousRoot: resp.OldRoot,
			Root:         resp.NewRoot,
		}, nil
	}

	resp, err := daemon.CreateRootStructure(ctx, arcs)
	if err != nil {
		return nil, err
	}
	return &importSummary{
		Scope: "root",
		Root:  resp.Root,
	}, nil
}

func applyImportedGraphArcs(ctx context.Context, daemon importBackend, graphID string, arcs map[string]string) (*importSummary, error) {
	meta, err := ensureImportGraph(ctx, daemon, graphID)
	if err != nil {
		return nil, err
	}

	if meta.Root == "" {
		resp, err := daemon.CreateGraphStructure(ctx, graphID, arcs)
		if err != nil {
			return nil, err
		}
		return &importSummary{
			Scope: "graph",
			Graph: graphID,
			Root:  resp.Root,
		}, nil
	}

	resp, err := daemon.BatchUpdateGraph(ctx, graphID, arcs)
	if err != nil {
		return nil, err
	}
	return &importSummary{
		Scope:        "graph",
		Graph:        graphID,
		PreviousRoot: resp.OldRoot,
		Root:         resp.NewRoot,
	}, nil
}

func ensureImportGraph(ctx context.Context, daemon importBackend, graphID string) (*httpapi.Graph, error) {
	meta, err := daemon.GetGraph(ctx, graphID)
	if err == nil {
		return meta, nil
	}

	var apiErr *daemonclient.Error
	if errors.As(err, &apiErr) && apiErr.StatusCode == 404 {
		return daemon.CreateGraph(ctx, graphID, "")
	}
	return nil, err
}

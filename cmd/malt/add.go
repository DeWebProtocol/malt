package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"

	daemonclient "github.com/dewebprotocol/malt/client"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/manifest"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/writer"
	"github.com/dewebprotocol/malt/httpapi"
	"github.com/dewebprotocol/malt/internal/merkledagimport"
	cid "github.com/ipfs/go-cid"
	"github.com/spf13/cobra"
)

const addFixedChunkSize = 262144

const (
	addTargetMALT      = "malt"
	addTargetMerkleDAG = "merkle-dag"

	addModelUnixFS = "unixfs"

	addLayoutFlat         = "flat"
	addLayoutHierarchical = "hierarchical"

	addFileLayoutBalanced = "balanced"
	addFileLayoutTrickle  = "trickle"

	addDirLayoutBasic    = "basic"
	addDirLayoutHAMT     = "hamt"
	addDirLayoutAdaptive = "adaptive"
)

var (
	addPrefixFlag       string
	addWrapFlag         bool
	addWrapNameFlag     string
	addTargetFlag       string
	addModelFlag        string
	addLayoutFlag       string
	addFileLayoutFlag   string
	addDirLayoutFlag    string
	addNoGitignoreFlag  bool
	addNoMaltignoreFlag bool
	addIgnoreFileFlags  []string
	addRootFlag         string
)

func init() {
	rootCmd.AddCommand(addCmd)
	addCmd.Flags().StringVarP(&addPrefixFlag, "prefix", "p", "", "Prefix inside the current root")
	addCmd.Flags().BoolVarP(&addWrapFlag, "wrap", "w", false, "Wrap all inputs under one directory")
	addCmd.Flags().StringVar(&addWrapNameFlag, "wrap-name", "", "Wrapper directory name (required for multi-input --wrap)")
	addCmd.Flags().StringVar(&addTargetFlag, "target", addTargetMALT, "Authenticated target substrate: malt or merkle-dag")
	addCmd.Flags().StringVar(&addModelFlag, "model", addModelUnixFS, "Source data model/schema")
	addCmd.Flags().StringVar(&addLayoutFlag, "layout", "", "MALT materialization layout: flat or hierarchical")
	addCmd.Flags().StringVar(&addFileLayoutFlag, "file-layout", "", "Merkle DAG UnixFS file layout: balanced or trickle")
	addCmd.Flags().StringVar(&addDirLayoutFlag, "dir-layout", "", "Merkle DAG UnixFS directory layout: basic, hamt, or adaptive")
	addCmd.Flags().BoolVar(&addNoGitignoreFlag, "no-gitignore", false, "Do not read .gitignore files while adding directories")
	addCmd.Flags().BoolVar(&addNoMaltignoreFlag, "no-maltignore", false, "Do not read .maltignore files while adding directories")
	addCmd.Flags().StringArrayVar(&addIgnoreFileFlags, "ignore-file", nil, "Additional gitignore-style ignore file to apply while adding directories")
	addCmd.Flags().StringVar(&addRootFlag, "root", "", "Root CID to add files under (creates a new root if empty)")
}

var addCmd = &cobra.Command{
	Use:   "add <local-path> [<local-path>...]",
	Short: "Upload local files/directories and merge into the current root",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runAdd,
}

type addSummary struct {
	Target           string `json:"target,omitempty"`
	Model            string `json:"model,omitempty"`
	Layout           string `json:"layout,omitempty"`
	FileLayout       string `json:"file_layout,omitempty"`
	DirLayout        string `json:"dir_layout,omitempty"`
	OldRoot          string `json:"old_root,omitempty"`
	NewRoot          string `json:"new_root"`
	Files            int    `json:"files_imported"`
	Bytes            int64  `json:"bytes_uploaded"`
	ImmutableObjects int    `json:"immutable_objects_written,omitempty"`
	MALTObjects      int    `json:"malt_objects_written,omitempty"`
	MALTMaps         int    `json:"malt_maps_written,omitempty"`
	MALTLists        int    `json:"malt_lists_written,omitempty"`
	ArcSets          int    `json:"arcsets_written,omitempty"`
	Arcs             int    `json:"arcs_written,omitempty"`
	SymlinkRoots     int    `json:"symlink_roots,omitempty"`
}

type addNode struct {
	Kind        string
	StorageKind string
	Key         cid.Cid
	Chunks      []cid.Cid
	Children    map[string]*addNode
	Changed     bool
}

func newDirNode() *addNode {
	return &addNode{
		Kind:     "dir",
		Children: make(map[string]*addNode),
	}
}

type addInput struct {
	Original string
	AbsPath  string
	BaseName string
	Info     fs.FileInfo
	Symlink  bool
}

type addMountedInput struct {
	Input     addInput
	MountBase string
}

type addBuildResult struct {
	Root             *addNode
	Files            int
	Bytes            int64
	ImmutableObjects int
	MALTObjects      int
	MALTMaps         int
	MALTLists        int
	ArcSets          int
	Arcs             int
	SymlinkRoots     int
}

type addMaterializeResult struct {
	Key              cid.Cid
	ArcCount         int
	Descendants      map[string]cid.Cid
	ImmutableObjects int
	MALTObjects      int
	MALTMaps         int
	MALTLists        int
	ArcSets          int
	Arcs             int
}

type addUnixFSResult struct {
	Files            int
	Bytes            int64
	NewRoot          string
	ImmutableObjects int
	MALTObjects      int
	MALTMaps         int
	MALTLists        int
	ArcSets          int
	Arcs             int
	SymlinkRoots     int
}

type addCASClient interface {
	Put(ctx context.Context, data []byte) (cid.Cid, error)
	PutWithCodec(ctx context.Context, data []byte, codec uint64) (cid.Cid, error)
	Get(ctx context.Context, c cid.Cid) ([]byte, error)
}

func runAdd(cmd *cobra.Command, args []string) error {
	ctx := cmd.Context()
	opts, err := normalizeAddBuildOptions(addBuildOptions{
		Prefix:     addPrefixFlag,
		Wrap:       addWrapFlag,
		WrapName:   addWrapNameFlag,
		Target:     addTargetFlag,
		Model:      addModelFlag,
		Layout:     addLayoutFlag,
		FileLayout: addFileLayoutFlag,
		DirLayout:  addDirLayoutFlag,
		Ignore: addIgnoreOptions{
			NoGitignore:  addNoGitignoreFlag,
			NoMaltignore: addNoMaltignoreFlag,
			IgnoreFiles:  addIgnoreFileFlags,
		},
	})
	if err != nil {
		return err
	}
	casClient, err := makeCASClient()
	if err != nil {
		return err
	}

	var daemon *daemonclient.Client
	workingRoot := strings.TrimSpace(addRootFlag)
	if opts.Target == addTargetMALT {
		daemon = mustDaemonClient()
	}

	result, err := addInputsWithUnixFS(ctx, daemon, casClient, args, workingRoot, opts)
	if err != nil {
		var apiErr *daemonclient.Error
		if errors.As(err, &apiErr) {
			return daemonCommandError(err)
		}
		return err
	}
	if result.NewRoot == "" {
		return fmt.Errorf("failed to materialize a new root")
	}

	summary := addSummary{
		Target:           opts.Target,
		Model:            opts.Model,
		Layout:           opts.Layout,
		FileLayout:       opts.FileLayout,
		DirLayout:        opts.DirLayout,
		OldRoot:          addRootFlag,
		NewRoot:          result.NewRoot,
		Files:            result.Files,
		Bytes:            result.Bytes,
		ImmutableObjects: result.ImmutableObjects,
		MALTObjects:      result.MALTObjects,
		MALTMaps:         result.MALTMaps,
		MALTLists:        result.MALTLists,
		ArcSets:          result.ArcSets,
		Arcs:             result.Arcs,
		SymlinkRoots:     result.SymlinkRoots,
	}
	fmt.Print(formatAddSummary(summary))
	return nil
}

func formatAddSummary(summary addSummary) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Uploaded %d immutable objects, %d bytes\n", summary.ImmutableObjects, summary.Bytes)
	fmt.Fprintf(&b, "Wrote %d MALT objects: %d maps, %d lists\n", summary.MALTObjects, summary.MALTMaps, summary.MALTLists)
	if summary.SymlinkRoots == 1 {
		fmt.Fprintf(&b, "Materialized 1 symlink root\n")
	} else if summary.SymlinkRoots > 1 {
		fmt.Fprintf(&b, "Materialized %d symlink roots\n", summary.SymlinkRoots)
	}
	fmt.Fprintf(&b, "Result root: %s\n", summary.NewRoot)
	return b.String()
}

type addBuildOptions struct {
	Prefix     string
	Wrap       bool
	WrapName   string
	Target     string
	Model      string
	Layout     string
	FileLayout string
	DirLayout  string
	Ignore     addIgnoreOptions
}

func addInputsWithUnixFS(ctx context.Context, daemon *daemonclient.Client, casClient addCASClient, rawInputs []string, root string, opts addBuildOptions) (*addUnixFSResult, error) {
	normalized, err := normalizeAddBuildOptions(opts)
	if err != nil {
		return nil, err
	}
	switch normalized.Target {
	case addTargetMALT:
		switch normalized.Layout {
		case addLayoutFlat:
			return addInputsWithMALTFlatUnixFS(ctx, daemon, casClient, rawInputs, root, normalized)
		case addLayoutHierarchical:
			return addInputsWithMALTHierarchicalUnixFS(ctx, daemon, casClient, rawInputs, root, normalized)
		}
	case addTargetMerkleDAG:
		return addInputsWithMerkleDAGUnixFS(ctx, casClient, rawInputs, normalized)
	}
	return nil, fmt.Errorf("unsupported add target/model/layout %q/%q/%q", normalized.Target, normalized.Model, normalized.Layout)
}

func normalizeAddBuildOptions(opts addBuildOptions) (addBuildOptions, error) {
	opts.Target = normalizeAddToken(opts.Target)
	opts.Model = normalizeAddToken(opts.Model)
	opts.Layout = normalizeAddToken(opts.Layout)
	opts.FileLayout = normalizeAddToken(opts.FileLayout)
	opts.DirLayout = normalizeAddToken(opts.DirLayout)
	if opts.Target == "" {
		opts.Target = addTargetMALT
	}
	if opts.Target == "merkledag" || opts.Target == "merkle_dag" {
		opts.Target = addTargetMerkleDAG
	}
	if opts.Model == "" {
		opts.Model = addModelUnixFS
	}
	if opts.Model != addModelUnixFS {
		return opts, fmt.Errorf("unsupported add model %q", opts.Model)
	}
	switch opts.Target {
	case addTargetMALT:
		if opts.Layout == "" {
			opts.Layout = addLayoutFlat
		}
		if opts.Layout != addLayoutFlat && opts.Layout != addLayoutHierarchical {
			return opts, fmt.Errorf("unsupported malt unixfs layout %q", opts.Layout)
		}
		if opts.FileLayout != "" || opts.DirLayout != "" {
			return opts, fmt.Errorf("--file-layout and --dir-layout are only supported with --target merkle-dag")
		}
	case addTargetMerkleDAG:
		if opts.Layout != "" {
			return opts, fmt.Errorf("--layout is only supported with --target malt; use --file-layout and --dir-layout for merkle-dag")
		}
		if opts.FileLayout == "" {
			opts.FileLayout = addFileLayoutBalanced
		}
		if opts.DirLayout == "" {
			opts.DirLayout = addDirLayoutAdaptive
		}
		if opts.FileLayout != addFileLayoutBalanced && opts.FileLayout != addFileLayoutTrickle {
			return opts, fmt.Errorf("unsupported merkle-dag unixfs file layout %q", opts.FileLayout)
		}
		if opts.DirLayout != addDirLayoutBasic && opts.DirLayout != addDirLayoutHAMT && opts.DirLayout != addDirLayoutAdaptive {
			return opts, fmt.Errorf("unsupported merkle-dag unixfs directory layout %q", opts.DirLayout)
		}
	default:
		return opts, fmt.Errorf("unsupported add target %q", opts.Target)
	}
	return opts, nil
}

func normalizeAddToken(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

func addInputsWithMALTFlatUnixFS(ctx context.Context, daemon *daemonclient.Client, casClient addCASClient, rawInputs []string, root string, opts addBuildOptions) (*addUnixFSResult, error) {
	return addInputsWithMALTStagedUnixFS(ctx, daemon, casClient, rawInputs, root, opts)
}

func addInputsWithMALTHierarchicalUnixFS(ctx context.Context, daemon *daemonclient.Client, casClient addCASClient, rawInputs []string, root string, opts addBuildOptions) (*addUnixFSResult, error) {
	return addInputsWithMALTStagedUnixFS(ctx, daemon, casClient, rawInputs, root, opts)
}

func addInputsWithMALTStagedUnixFS(ctx context.Context, daemon *daemonclient.Client, casClient addCASClient, rawInputs []string, root string, opts addBuildOptions) (*addUnixFSResult, error) {
	if daemon == nil {
		return nil, fmt.Errorf("malt target requires daemon client")
	}
	staged, err := buildAddStagingTree(ctx, casClient, daemon, rawInputs, opts)
	if err != nil {
		return nil, err
	}

	rootNode := staged.Root
	if strings.TrimSpace(root) != "" {
		existing, err := loadExistingCurrentTree(ctx, daemon, casClient, root)
		if err != nil {
			return nil, err
		}
		rootNode = mergeAddNodes(existing, staged.Root)
	}
	mat, err := materializeDirectory(ctx, daemon, casClient, rootNode)
	if err != nil {
		return nil, err
	}
	return &addUnixFSResult{
		Files:            staged.Files,
		Bytes:            staged.Bytes,
		NewRoot:          mat.Key.String(),
		ImmutableObjects: staged.ImmutableObjects + mat.ImmutableObjects,
		MALTObjects:      staged.MALTObjects + mat.MALTObjects,
		MALTMaps:         staged.MALTMaps + mat.MALTMaps,
		MALTLists:        staged.MALTLists + mat.MALTLists,
		ArcSets:          staged.ArcSets + mat.ArcSets,
		Arcs:             staged.Arcs + mat.Arcs,
		SymlinkRoots:     staged.SymlinkRoots,
	}, nil
}

func addInputsWithMerkleDAGUnixFS(ctx context.Context, casClient addCASClient, rawInputs []string, opts addBuildOptions) (*addUnixFSResult, error) {
	if opts.Prefix != "" || opts.Wrap || opts.WrapName != "" {
		return nil, fmt.Errorf("merkle-dag target does not support --prefix, --wrap, or --wrap-name yet")
	}
	if len(rawInputs) != 1 {
		return nil, fmt.Errorf("merkle-dag target expects exactly one local path")
	}
	ignoreFilter, err := newAddIgnoreFilter(rawInputs[0], opts.Ignore)
	if err != nil {
		return nil, err
	}
	result, err := merkledagimport.ImportPath(ctx, casClient, rawInputs[0], merkledagimport.Options{
		Model:      opts.Model,
		FileLayout: opts.FileLayout,
		DirLayout:  opts.DirLayout,
		ChunkSize:  addFixedChunkSize,
		Ignore:     ignoreFilter,
	})
	if err != nil {
		return nil, err
	}
	return &addUnixFSResult{Files: result.Files, Bytes: result.Bytes, NewRoot: result.Root}, nil
}

func stageFlatUnixFSDirectory(ctx context.Context, root *addNode, casClient addCASClient, daemon *daemonclient.Client, item addMountedInput, ignoreOpts addIgnoreOptions) (int, int64, error) {
	mountBase := canonicalAddPath(item.MountBase)
	if mountBase == "" {
		return 0, 0, fmt.Errorf("directory mount path must not be empty")
	}
	ensureDirNode(root, mountBase)
	ignoreFilter, err := newAddIgnoreFilter(item.Input.AbsPath, ignoreOpts)
	if err != nil {
		return 0, 0, err
	}

	var files int
	var bytesUploaded int64
	err = filepath.WalkDir(item.Input.AbsPath, func(current string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current != item.Input.AbsPath {
			ignored, err := ignoreFilter.Ignored(current, d.IsDir())
			if err != nil {
				return err
			}
			if ignored {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if d.IsDir() {
			if err := ignoreFilter.LoadDirectoryRules(current); err != nil {
				return err
			}
		}
		if current != item.Input.AbsPath && d.Type()&fs.ModeSymlink != 0 {
			rel, err := filepath.Rel(item.Input.AbsPath, current)
			if err != nil {
				return fmt.Errorf("compute relative path %q: %w", current, err)
			}
			targetPath := canonicalAddPath(path.Join(mountBase, filepath.ToSlash(rel)))
			info, err := os.Stat(current)
			if err != nil {
				return fmt.Errorf("stat symlink target %s: %w", current, err)
			}
			if info.IsDir() {
				key, dirFiles, dirBytes, _, _, err := materializeSymlinkDirectoryBoundary(ctx, daemon, casClient, current, nil)
				if err != nil {
					return err
				}
				if err := setMapDirNode(root, targetPath, key); err != nil {
					return err
				}
				files += dirFiles
				bytesUploaded += dirBytes
				return nil
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("non-regular symlink target is not supported: %s", current)
			}
			fileBytes, err := stageFlatUnixFSFile(ctx, root, casClient, current, targetPath)
			if err != nil {
				return err
			}
			files++
			bytesUploaded += fileBytes
			return nil
		}
		rel, err := filepath.Rel(item.Input.AbsPath, current)
		if err != nil {
			return fmt.Errorf("compute relative path %q: %w", current, err)
		}
		if rel == "." {
			return nil
		}
		targetPath := canonicalAddPath(path.Join(mountBase, filepath.ToSlash(rel)))
		if targetPath == "" {
			return nil
		}
		if d.IsDir() {
			ensureDirNode(root, targetPath)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", current, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular file is not supported: %s", current)
		}
		fileBytes, err := stageFlatUnixFSFile(ctx, root, casClient, current, targetPath)
		if err != nil {
			return err
		}
		files++
		bytesUploaded += fileBytes
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return files, bytesUploaded, nil
}

func stageFlatUnixFSFile(ctx context.Context, root *addNode, casClient addCASClient, localPath string, targetPath string) (int64, error) {
	targetPath = canonicalAddPath(targetPath)
	if targetPath == "" {
		return 0, fmt.Errorf("target path must not be empty")
	}
	info, err := os.Stat(localPath)
	if err != nil {
		return 0, fmt.Errorf("stat %s: %w", localPath, err)
	}
	if !info.Mode().IsRegular() {
		return 0, fmt.Errorf("not a regular file: %s", localPath)
	}
	node := ensureFileNode(root, targetPath)
	if info.Size() <= addFixedChunkSize {
		data, err := os.ReadFile(localPath)
		if err != nil {
			return 0, fmt.Errorf("read %s: %w", localPath, err)
		}
		blockCID, err := casClient.Put(ctx, data)
		if err != nil {
			return 0, fmt.Errorf("upload %s to CAS: %w", localPath, err)
		}
		node.Key = blockCID
		node.Chunks = nil
		return info.Size(), nil
	}
	chunks, err := uploadFlatChunks(ctx, casClient, localPath)
	if err != nil {
		return 0, err
	}
	node.Key = cid.Undef
	node.Chunks = chunks
	return info.Size(), nil
}

func uploadFlatChunks(ctx context.Context, casClient addCASClient, localPath string) ([]cid.Cid, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", localPath, err)
	}
	defer f.Close()

	chunks := make([]cid.Cid, 0)
	buf := make([]byte, addFixedChunkSize)
	for {
		n, readErr := io.ReadFull(f, buf)
		if readErr != nil && !errors.Is(readErr, io.EOF) && !errors.Is(readErr, io.ErrUnexpectedEOF) {
			return nil, fmt.Errorf("read %s: %w", localPath, readErr)
		}
		if n > 0 {
			chunkCID, err := casClient.Put(ctx, slices.Clone(buf[:n]))
			if err != nil {
				return nil, fmt.Errorf("upload chunk for %s: %w", localPath, err)
			}
			chunks = append(chunks, chunkCID)
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
	}
	if len(chunks) == 0 {
		return nil, fmt.Errorf("empty chunk sequence for %s", localPath)
	}
	return chunks, nil
}

func materializeSymlinkDirectoryBoundary(ctx context.Context, daemon *daemonclient.Client, casClient addCASClient, localPath string, seen map[string]struct{}) (cid.Cid, int, int64, *addMaterializeResult, int, error) {
	info, err := os.Stat(localPath)
	if err != nil {
		return cid.Undef, 0, 0, nil, 0, fmt.Errorf("stat symlink directory %s: %w", localPath, err)
	}
	if !info.IsDir() {
		return cid.Undef, 0, 0, nil, 0, fmt.Errorf("symlink target is not a directory: %s", localPath)
	}
	if seen == nil {
		seen = make(map[string]struct{})
	}
	staged := newDirNode()
	files, bytesUploaded, listObjects, nestedMat, nestedSymlinks, err := stageHierarchicalDirectoryChildren(ctx, staged, casClient, daemon, localPath, "", seen)
	if err != nil {
		return cid.Undef, 0, 0, nil, 0, err
	}
	mat, err := materializeDirectory(ctx, daemon, casClient, staged)
	if err != nil {
		return cid.Undef, 0, 0, nil, 0, err
	}
	mat.MALTObjects += nestedMat.MALTObjects + listObjects
	mat.MALTMaps += nestedMat.MALTMaps
	mat.MALTLists += nestedMat.MALTLists + listObjects
	mat.ArcSets += nestedMat.ArcSets + listObjects
	mat.Arcs += nestedMat.Arcs
	return mat.Key, files, bytesUploaded, mat, nestedSymlinks, nil
}

func stageHierarchicalDirectoryChildren(ctx context.Context, root *addNode, casClient addCASClient, daemon *daemonclient.Client, localDir string, mountBase string, seen map[string]struct{}) (int, int64, int, *addMaterializeResult, int, error) {
	cycleKey, err := filepath.EvalSymlinks(localDir)
	if err != nil {
		cycleKey, err = filepath.Abs(localDir)
		if err != nil {
			return 0, 0, 0, nil, 0, fmt.Errorf("resolve directory %s: %w", localDir, err)
		}
	}
	if _, ok := seen[cycleKey]; ok {
		return 0, 0, 0, nil, 0, fmt.Errorf("symlink cycle detected at %s", localDir)
	}
	seen[cycleKey] = struct{}{}
	defer delete(seen, cycleKey)

	entries, err := os.ReadDir(localDir)
	if err != nil {
		return 0, 0, 0, nil, 0, fmt.Errorf("read directory %s: %w", localDir, err)
	}
	var files int
	var bytesUploaded int64
	var listObjects int
	nestedMat := &addMaterializeResult{}
	var symlinkRoots int
	for _, entry := range entries {
		childLocal := filepath.Join(localDir, entry.Name())
		childPath := canonicalAddPath(path.Join(mountBase, entry.Name()))
		if entry.Type()&fs.ModeSymlink != 0 {
			info, err := os.Stat(childLocal)
			if err != nil {
				return 0, 0, 0, nil, 0, fmt.Errorf("stat symlink target %s: %w", childLocal, err)
			}
			if info.IsDir() {
				key, dirFiles, dirBytes, mat, nestedSymlinkCount, err := materializeSymlinkDirectoryBoundary(ctx, daemon, casClient, childLocal, seen)
				if err != nil {
					return 0, 0, 0, nil, 0, err
				}
				if err := setMapDirNode(root, childPath, key); err != nil {
					return 0, 0, 0, nil, 0, err
				}
				files += dirFiles
				bytesUploaded += dirBytes
				addMaterializeStats(nestedMat, mat)
				symlinkRoots += 1 + nestedSymlinkCount
				continue
			}
			if !info.Mode().IsRegular() {
				return 0, 0, 0, nil, 0, fmt.Errorf("non-regular symlink target is not supported: %s", childLocal)
			}
			fileBytes, childLists, err := stageSingleFile(ctx, root, casClient, daemon, childLocal, childPath)
			if err != nil {
				return 0, 0, 0, nil, 0, err
			}
			files++
			bytesUploaded += fileBytes
			listObjects += childLists
			continue
		}
		info, err := os.Stat(childLocal)
		if err != nil {
			return 0, 0, 0, nil, 0, fmt.Errorf("stat %s: %w", childLocal, err)
		}
		if info.IsDir() {
			ensureDirNode(root, childPath)
			childFiles, childBytes, childLists, childMat, childSymlinks, err := stageHierarchicalDirectoryChildren(ctx, root, casClient, daemon, childLocal, childPath, seen)
			if err != nil {
				return 0, 0, 0, nil, 0, err
			}
			files += childFiles
			bytesUploaded += childBytes
			listObjects += childLists
			addMaterializeStats(nestedMat, childMat)
			symlinkRoots += childSymlinks
			continue
		}
		if !info.Mode().IsRegular() {
			return 0, 0, 0, nil, 0, fmt.Errorf("non-regular file is not supported: %s", childLocal)
		}
		fileBytes, childLists, err := stageSingleFile(ctx, root, casClient, daemon, childLocal, childPath)
		if err != nil {
			return 0, 0, 0, nil, 0, err
		}
		files++
		bytesUploaded += fileBytes
		listObjects += childLists
	}
	return files, bytesUploaded, listObjects, nestedMat, symlinkRoots, nil
}

func flatUnixFSBatchEntries(ctx context.Context, casClient addCASClient, root *addNode) ([]httpapi.UnixFSBatchEntry, error) {
	entries := make([]httpapi.UnixFSBatchEntry, 0)
	var walk func(prefix string, node *addNode) error
	walk = func(prefix string, node *addNode) error {
		if node == nil {
			return nil
		}
		if prefix != "" {
			switch node.Kind {
			case "dir":
				manifestCID, err := putAddDirectoryManifest(ctx, casClient, node)
				if err != nil {
					return err
				}
				entries = append(entries, httpapi.UnixFSBatchEntry{
					Path:   prefix,
					Target: manifestCID.String(),
				})
			case "mapdir":
				entries = append(entries, httpapi.UnixFSBatchEntry{
					Path:   prefix,
					Target: node.Key.String(),
				})
				return nil
			case "file":
				entry := httpapi.UnixFSBatchEntry{Path: prefix}
				if len(node.Chunks) > 0 {
					entry.Chunks = make([]string, len(node.Chunks))
					for i, chunk := range node.Chunks {
						entry.Chunks[i] = chunk.String()
					}
				} else {
					entry.Target = node.Key.String()
				}
				entries = append(entries, entry)
				return nil
			}
		}
		names := make([]string, 0, len(node.Children))
		for name := range node.Children {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			childPath := name
			if prefix != "" {
				childPath = path.Join(prefix, name)
			}
			if err := walk(childPath, node.Children[name]); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk("", root); err != nil {
		return nil, err
	}
	return entries, nil
}

func putAddDirectoryManifest(ctx context.Context, casClient addCASClient, node *addNode) (cid.Cid, error) {
	names := make([]string, 0, len(node.Children))
	for name := range node.Children {
		names = append(names, name)
	}
	payload, err := manifest.Normalize(&manifest.DirectoryManifest{Entries: names}).MarshalJSON()
	if err != nil {
		return cid.Undef, fmt.Errorf("marshal directory manifest: %w", err)
	}
	manifestCID, err := casClient.PutWithCodec(ctx, payload, codec.CodecMaltManifest)
	if err != nil {
		return cid.Undef, fmt.Errorf("upload directory manifest: %w", err)
	}
	return manifestCID, nil
}

func buildAddStagingTree(ctx context.Context, casClient addCASClient, daemon *daemonclient.Client, rawInputs []string, opts addBuildOptions) (*addBuildResult, error) {
	inputs, err := collectAddInputs(rawInputs)
	if err != nil {
		return nil, err
	}
	mounted, err := mountAddInputs(inputs, opts)
	if err != nil {
		return nil, err
	}

	batcher := asAddCASBatcher(casClient)
	root := newDirNode()
	var files int
	var bytesUploaded int64
	var maltObjects int
	var maltMaps int
	var maltLists int
	var directLists int
	var arcSets int
	var arcs int
	var symlinkRoots int

	for _, item := range mounted {
		if item.Input.Info.IsDir() {
			if item.Input.Symlink {
				key, dirFiles, dirBytes, mat, nestedSymlinks, err := materializeSymlinkDirectoryBoundary(ctx, daemon, batcher, item.Input.AbsPath, nil)
				if err != nil {
					return nil, err
				}
				if err := setMapDirNode(root, item.MountBase, key); err != nil {
					return nil, err
				}
				files += dirFiles
				bytesUploaded += dirBytes
				maltObjects += mat.MALTObjects
				maltMaps += mat.MALTMaps
				maltLists += mat.MALTLists
				arcSets += mat.ArcSets
				arcs += mat.Arcs
				symlinkRoots += 1 + nestedSymlinks
				continue
			}
			dirFiles, dirBytes, dirLists, dirMat, dirSymlinks, err := stageDirectoryInput(ctx, root, batcher, daemon, item, opts.Ignore)
			if err != nil {
				return nil, err
			}
			files += dirFiles
			bytesUploaded += dirBytes
			maltLists += dirLists
			directLists += dirLists
			maltObjects += dirMat.MALTObjects
			maltMaps += dirMat.MALTMaps
			maltLists += dirMat.MALTLists
			arcSets += dirMat.ArcSets
			arcs += dirMat.Arcs
			symlinkRoots += dirSymlinks
			continue
		}
		if item.Input.Symlink {
			fileBytes, listObjects, err := stageSingleFile(ctx, root, batcher, daemon, item.Input.AbsPath, item.MountBase)
			if err != nil {
				return nil, err
			}
			files++
			bytesUploaded += fileBytes
			maltLists += listObjects
			directLists += listObjects
			continue
		}
		fileBytes, listObjects, err := stageSingleFile(ctx, root, batcher, daemon, item.Input.AbsPath, item.MountBase)
		if err != nil {
			return nil, err
		}
		files++
		bytesUploaded += fileBytes
		maltLists += listObjects
		directLists += listObjects
	}
	if err := batcher.Flush(ctx); err != nil {
		return nil, fmt.Errorf("flush staged CAS batch: %w", err)
	}

	return &addBuildResult{
		Root:             root,
		Files:            files,
		Bytes:            bytesUploaded,
		ImmutableObjects: batcher.UploadedCount(),
		MALTObjects:      maltObjects + directLists,
		MALTMaps:         maltMaps,
		MALTLists:        maltLists,
		ArcSets:          arcSets + directLists,
		Arcs:             arcs,
		SymlinkRoots:     symlinkRoots,
	}, nil
}

func collectAddInputs(rawInputs []string) ([]addInput, error) {
	out := make([]addInput, 0, len(rawInputs))
	for _, raw := range rawInputs {
		abs, err := filepath.Abs(raw)
		if err != nil {
			return nil, fmt.Errorf("resolve path %q: %w", raw, err)
		}
		info, err := os.Lstat(abs)
		if err != nil {
			return nil, fmt.Errorf("stat %q: %w", raw, err)
		}
		isSymlink := info.Mode()&os.ModeSymlink != 0
		if isSymlink {
			info, err = os.Stat(abs)
			if err != nil {
				return nil, fmt.Errorf("stat symlink target %q: %w", raw, err)
			}
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return nil, fmt.Errorf("only regular files and directories are supported: %s", raw)
		}
		out = append(out, addInput{
			Original: raw,
			AbsPath:  abs,
			BaseName: filepath.Base(abs),
			Info:     info,
			Symlink:  isSymlink,
		})
	}
	return out, nil
}

func mountAddInputs(inputs []addInput, opts addBuildOptions) ([]addMountedInput, error) {
	prefix := canonicalAddPath(opts.Prefix)
	if opts.Wrap && len(inputs) > 1 && strings.TrimSpace(opts.WrapName) == "" {
		return nil, fmt.Errorf("--wrap-name is required when --wrap is used with multiple inputs")
	}
	if opts.Wrap && len(inputs) == 1 && inputs[0].Info.IsDir() {
		return nil, fmt.Errorf("single directory input does not support extra wrapping")
	}

	seen := make(map[string]struct{})
	out := make([]addMountedInput, 0, len(inputs))
	for _, in := range inputs {
		mount := in.BaseName
		if opts.Wrap {
			wrapName := strings.TrimSpace(opts.WrapName)
			if wrapName == "" {
				wrapName = in.BaseName
			}
			mount = path.Join(canonicalAddPath(wrapName), in.BaseName)
		}
		if prefix != "" {
			mount = path.Join(prefix, mount)
		}
		mount = canonicalAddPath(mount)
		if mount == "" {
			return nil, fmt.Errorf("invalid mount path for input %q", in.Original)
		}
		if _, ok := seen[mount]; ok {
			return nil, fmt.Errorf("duplicate mounted target path %q", mount)
		}
		seen[mount] = struct{}{}
		out = append(out, addMountedInput{
			Input:     in,
			MountBase: mount,
		})
	}
	return out, nil
}

func stageDirectoryInput(ctx context.Context, root *addNode, casClient addCASClient, daemon *daemonclient.Client, item addMountedInput, ignoreOpts addIgnoreOptions) (int, int64, int, *addMaterializeResult, int, error) {
	mountBase := item.MountBase
	ensureDirNode(root, mountBase)
	ignoreFilter, err := newAddIgnoreFilter(item.Input.AbsPath, ignoreOpts)
	if err != nil {
		return 0, 0, 0, nil, 0, err
	}

	var files int
	var bytesUploaded int64
	var listObjects int
	symlinkMat := &addMaterializeResult{}
	var symlinkRoots int
	err = filepath.WalkDir(item.Input.AbsPath, func(current string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current != item.Input.AbsPath {
			ignored, err := ignoreFilter.Ignored(current, d.IsDir())
			if err != nil {
				return err
			}
			if ignored {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if d.IsDir() {
			if err := ignoreFilter.LoadDirectoryRules(current); err != nil {
				return err
			}
		}
		if current != item.Input.AbsPath {
			if d.Type()&fs.ModeSymlink != 0 {
				rel, err := filepath.Rel(item.Input.AbsPath, current)
				if err != nil {
					return fmt.Errorf("compute relative path %q: %w", current, err)
				}
				targetPath := canonicalAddPath(path.Join(mountBase, filepath.ToSlash(rel)))
				info, err := os.Stat(current)
				if err != nil {
					return fmt.Errorf("stat symlink target %s: %w", current, err)
				}
				if info.IsDir() {
					key, dirFiles, dirBytes, mat, nestedSymlinks, err := materializeSymlinkDirectoryBoundary(ctx, daemon, casClient, current, nil)
					if err != nil {
						return err
					}
					if err := setMapDirNode(root, targetPath, key); err != nil {
						return err
					}
					files += dirFiles
					bytesUploaded += dirBytes
					addMaterializeStats(symlinkMat, mat)
					symlinkRoots += 1 + nestedSymlinks
					return nil
				}
				if !info.Mode().IsRegular() {
					return fmt.Errorf("non-regular symlink target is not supported: %s", current)
				}
				fileBytes, childLists, err := stageSingleFile(ctx, root, casClient, daemon, current, targetPath)
				if err != nil {
					return err
				}
				files++
				bytesUploaded += fileBytes
				listObjects += childLists
				return nil
			}
		}

		rel, err := filepath.Rel(item.Input.AbsPath, current)
		if err != nil {
			return fmt.Errorf("compute relative path %q: %w", current, err)
		}
		if rel == "." {
			return nil
		}
		targetPath := canonicalAddPath(path.Join(mountBase, filepath.ToSlash(rel)))
		if targetPath == "" {
			return nil
		}

		if d.IsDir() {
			ensureDirNode(root, targetPath)
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return fmt.Errorf("stat %s: %w", current, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non-regular file is not supported: %s", current)
		}

		fileBytes, childLists, err := stageSingleFile(ctx, root, casClient, daemon, current, targetPath)
		if err != nil {
			return err
		}
		files++
		bytesUploaded += fileBytes
		listObjects += childLists
		return nil
	})
	if err != nil {
		return 0, 0, 0, nil, 0, err
	}
	return files, bytesUploaded, listObjects, symlinkMat, symlinkRoots, nil
}

func stageSingleFile(ctx context.Context, root *addNode, casClient addCASClient, daemon *daemonclient.Client, localPath string, targetPath string) (int64, int, error) {
	targetPath = canonicalAddPath(targetPath)
	if targetPath == "" {
		return 0, 0, fmt.Errorf("target path must not be empty")
	}

	info, err := os.Stat(localPath)
	if err != nil {
		return 0, 0, fmt.Errorf("stat %s: %w", localPath, err)
	}
	if !info.Mode().IsRegular() {
		return 0, 0, fmt.Errorf("not a regular file: %s", localPath)
	}

	var key cid.Cid
	listObjects := 0
	if info.Size() <= addFixedChunkSize {
		data, err := os.ReadFile(localPath)
		if err != nil {
			return 0, 0, fmt.Errorf("read %s: %w", localPath, err)
		}
		blockCID, err := casClient.Put(ctx, data)
		if err != nil {
			return 0, 0, fmt.Errorf("upload %s to CAS: %w", localPath, err)
		}
		key = blockCID
	} else {
		listRoot, err := uploadAsList(ctx, casClient, daemon, localPath, uint64(info.Size()))
		if err != nil {
			return 0, 0, err
		}
		key = listRoot
		listObjects = 1
	}

	if err := setFileNode(root, targetPath, key); err != nil {
		return 0, 0, err
	}
	return info.Size(), listObjects, nil
}

func uploadAsList(ctx context.Context, casClient addCASClient, daemon *daemonclient.Client, localPath string, totalSize uint64) (cid.Cid, error) {
	chunks, err := uploadFlatChunks(ctx, casClient, localPath)
	if err != nil {
		return cid.Undef, err
	}
	if batcher, ok := casClient.(*addCASBatcher); ok {
		if err := batcher.Flush(ctx); err != nil {
			return cid.Undef, fmt.Errorf("flush chunks for %s: %w", localPath, err)
		}
	}
	tempRootResp, err := daemon.CreatePayloadRoot(ctx, nil)
	if err != nil {
		return cid.Undef, err
	}
	baseRoot, err := cid.Decode(tempRootResp.Root)
	if err != nil {
		return cid.Undef, fmt.Errorf("decode temporary root CID: %w", err)
	}
	changes := make([]arcset.ArcChange, len(chunks))
	for i, chunk := range chunks {
		coord, err := arcset.NewListCoordinate(int64(i))
		if err != nil {
			return cid.Undef, err
		}
		after := arcset.NewCASTarget(chunk)
		changes[i] = arcset.ArcChange{
			Coordinate: coord,
			After:      &after,
		}
	}
	delta, err := arcset.NewCanonicalArcDelta(arcset.KindList, changes)
	if err != nil {
		return cid.Undef, err
	}
	resp, err := daemon.ApplySemanticMutation(ctx, writer.SemanticMutation{
		BaseRoot: baseRoot,
		Deltas: []writer.ArcSetDelta{{
			Kind:    arcset.KindList,
			Changes: delta,
			Commit: writer.CommitDescriptor{
				FixedList: &writer.FixedListCommit{
					TotalSize: totalSize,
					ChunkSize: addFixedChunkSize,
				},
			},
		}},
	})
	if err != nil {
		return cid.Undef, err
	}
	listRoot, err := cid.Decode(resp.NewRoot)
	if err != nil {
		return cid.Undef, fmt.Errorf("decode list root CID: %w", err)
	}
	return listRoot, nil
}

func ensureDirNode(root *addNode, p string) *addNode {
	root.Changed = true
	if p == "" {
		return root
	}
	segments := splitAddPath(p)
	cur := root
	for _, seg := range segments {
		child, ok := cur.Children[seg]
		if !ok {
			child = newDirNode()
			child.Changed = true
			cur.Children[seg] = child
		}
		if child.Kind != "dir" {
			child = newDirNode()
			child.Changed = true
			cur.Children[seg] = child
		}
		cur.Changed = true
		cur = child
	}
	return cur
}

func setFileNode(root *addNode, p string, key cid.Cid) error {
	segments := splitAddPath(p)
	if len(segments) == 0 {
		return fmt.Errorf("file path must not be empty")
	}
	parentPath := path.Dir(p)
	if parentPath == "." {
		parentPath = ""
	}
	parent := ensureDirNode(root, parentPath)
	name := segments[len(segments)-1]

	if existing, ok := parent.Children[name]; ok {
		if existing.Kind == "file" && existing.Key.Equals(key) {
			return nil
		}
	}
	parent.Children[name] = &addNode{
		Kind:        "file",
		Key:         key,
		StorageKind: storageKindFromCID(key),
		Changed:     true,
	}
	parent.Changed = true
	return nil
}

func ensureFileNode(root *addNode, p string) *addNode {
	segments := splitAddPath(p)
	if len(segments) == 0 {
		return nil
	}
	parentPath := path.Dir(p)
	if parentPath == "." {
		parentPath = ""
	}
	parent := ensureDirNode(root, parentPath)
	name := segments[len(segments)-1]
	node := &addNode{
		Kind:        "file",
		StorageKind: "raw",
		Changed:     true,
	}
	parent.Children[name] = node
	parent.Changed = true
	return node
}

func setMapDirNode(root *addNode, p string, key cid.Cid) error {
	segments := splitAddPath(p)
	if len(segments) == 0 {
		return fmt.Errorf("map directory path must not be empty")
	}
	parentPath := path.Dir(p)
	if parentPath == "." {
		parentPath = ""
	}
	parent := ensureDirNode(root, parentPath)
	name := segments[len(segments)-1]
	parent.Children[name] = &addNode{
		Kind:        "mapdir",
		StorageKind: "map",
		Key:         key,
		Changed:     true,
	}
	parent.Changed = true
	return nil
}

func mergeAddNodes(existing *addNode, staged *addNode) *addNode {
	if staged == nil {
		return existing
	}
	if existing == nil {
		return staged
	}
	if staged.Kind != "dir" {
		if existing.Kind == staged.Kind && existing.Key.Equals(staged.Key) {
			return existing
		}
		return staged
	}
	if existing.Kind != "dir" {
		return staged
	}
	for name, child := range staged.Children {
		mergedChild := mergeAddNodes(existing.Children[name], child)
		if existing.Children[name] != mergedChild {
			existing.Changed = true
		}
		if mergedChild != nil && mergedChild.Changed {
			existing.Changed = true
		}
		existing.Children[name] = mergedChild
	}
	return existing
}

func loadExistingCurrentTree(ctx context.Context, daemon *daemonclient.Client, casClient addCASClient, rootCID string) (*addNode, error) {
	rootStat, err := daemon.Stat(ctx, rootCID, "")
	if err != nil {
		return nil, err
	}
	if rootStat.Key != rootCID {
		rootCID = rootStat.Key
	}
	if rootStat.Kind != "dir" {
		return nil, fmt.Errorf("current root must be directory, got %q", rootStat.Kind)
	}
	return loadCurrentDirRecursive(ctx, daemon, casClient, rootCID, "", rootStat)
}

func loadCurrentDirRecursive(ctx context.Context, daemon *daemonclient.Client, casClient addCASClient, root string, currentPath string, stat *httpapi.PathStatResponse) (*addNode, error) {
	node := newDirNode()
	node.Changed = false
	node.StorageKind = stat.StorageKind
	keyCID, err := cid.Decode(stat.Key)
	if err != nil {
		return nil, fmt.Errorf("decode directory key %q: %w", stat.Key, err)
	}
	node.Key = keyCID

	if strings.TrimSpace(stat.Payload) == "" {
		return node, nil
	}
	payloadCID, err := cid.Decode(stat.Payload)
	if err != nil {
		return nil, fmt.Errorf("decode directory payload %q: %w", stat.Payload, err)
	}
	raw, err := casClient.Get(ctx, payloadCID)
	if err != nil {
		return nil, fmt.Errorf("fetch directory manifest %s: %w", stat.Payload, err)
	}
	m, err := manifest.ParseDirectoryJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("parse directory manifest %s: %w", stat.Payload, err)
	}
	for _, childName := range m.Entries {
		childPath := childName
		if currentPath != "" {
			childPath = path.Join(currentPath, childName)
		}
		childStat, err := daemon.Stat(ctx, root, childPath)
		if err != nil {
			return nil, err
		}
		switch childStat.Kind {
		case "dir":
			childDir, err := loadCurrentDirRecursive(ctx, daemon, casClient, root, childPath, childStat)
			if err != nil {
				return nil, err
			}
			node.Children[childName] = childDir
		case "file":
			childKey, err := cid.Decode(childStat.Key)
			if err != nil {
				return nil, fmt.Errorf("decode file key %q: %w", childStat.Key, err)
			}
			node.Children[childName] = &addNode{
				Kind:        "file",
				StorageKind: childStat.StorageKind,
				Key:         childKey,
				Changed:     false,
			}
		default:
			return nil, fmt.Errorf("unsupported child kind %q at %q", childStat.Kind, childPath)
		}
	}
	return node, nil
}

func materializeDirectory(ctx context.Context, daemon *daemonclient.Client, casClient addCASClient, node *addNode) (*addMaterializeResult, error) {
	return materializeDirectoryWithBatcher(ctx, daemon, asAddCASBatcher(casClient), node)
}

func addMaterializeStats(dst *addMaterializeResult, src *addMaterializeResult) {
	if dst == nil || src == nil {
		return
	}
	dst.ImmutableObjects += src.ImmutableObjects
	dst.MALTObjects += src.MALTObjects
	dst.MALTMaps += src.MALTMaps
	dst.MALTLists += src.MALTLists
	dst.ArcSets += src.ArcSets
	dst.Arcs += src.Arcs
	dst.ArcCount += src.ArcCount
}

func materializeDirectoryWithBatcher(ctx context.Context, daemon *daemonclient.Client, casClient *addCASBatcher, node *addNode) (*addMaterializeResult, error) {
	if node == nil || node.Kind != "dir" {
		return nil, fmt.Errorf("materializeDirectory requires a directory node")
	}

	names := make([]string, 0, len(node.Children))
	for name := range node.Children {
		names = append(names, name)
	}
	slices.Sort(names)

	desc := make(map[string]cid.Cid)
	childKeys := make(map[string]cid.Cid, len(node.Children))
	stats := &addMaterializeResult{}
	for _, name := range names {
		child := node.Children[name]
		if child == nil {
			continue
		}
		if child.Kind == "dir" {
			mat, err := materializeDirectoryWithBatcher(ctx, daemon, casClient, child)
			if err != nil {
				return nil, err
			}
			addMaterializeStats(stats, mat)
			child.Key = mat.Key
			child.Changed = false
			childKeys[name] = mat.Key
			desc[name] = mat.Key
			for rel, childKey := range mat.Descendants {
				desc[path.Join(name, rel)] = childKey
			}
			continue
		}
		childKeys[name] = child.Key
		desc[name] = child.Key
	}

	if !node.Changed && node.Key.Defined() {
		return &addMaterializeResult{
			Key:              node.Key,
			ArcCount:         stats.ArcCount,
			Descendants:      desc,
			ImmutableObjects: stats.ImmutableObjects,
			MALTObjects:      stats.MALTObjects,
			MALTMaps:         stats.MALTMaps,
			MALTLists:        stats.MALTLists,
			ArcSets:          stats.ArcSets,
			Arcs:             stats.Arcs,
		}, nil
	}

	payloadBytes, err := (&manifest.DirectoryManifest{Entries: names}).MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("marshal directory manifest: %w", err)
	}
	payloadCID, err := casClient.Put(ctx, payloadBytes)
	if err != nil {
		return nil, fmt.Errorf("upload directory manifest: %w", err)
	}
	if err := casClient.Flush(ctx); err != nil {
		return nil, fmt.Errorf("flush directory manifest: %w", err)
	}

	bindings := make(map[string]string, 1+len(childKeys)+len(desc))
	bindings["@payload"] = payloadCID.String()
	for name, key := range childKeys {
		bindings[name] = key.String()
	}
	for rel, key := range desc {
		if !strings.Contains(rel, "/") {
			continue
		}
		bindings[rel] = key.String()
	}

	resp, err := daemon.CreateRootStructure(ctx, bindings)
	if err != nil {
		return nil, err
	}
	rootCID, err := cid.Decode(resp.Root)
	if err != nil {
		return nil, fmt.Errorf("decode created map root: %w", err)
	}
	node.Key = rootCID
	node.Changed = false
	node.StorageKind = "map"
	arcCount := countDefinedBindings(bindings)
	return &addMaterializeResult{
		Key:              rootCID,
		ArcCount:         stats.ArcCount + arcCount,
		Descendants:      desc,
		ImmutableObjects: stats.ImmutableObjects + 1,
		MALTObjects:      stats.MALTObjects + 1,
		MALTMaps:         stats.MALTMaps + 1,
		MALTLists:        stats.MALTLists,
		ArcSets:          stats.ArcSets + 1,
		Arcs:             stats.Arcs + arcCount,
	}, nil
}

func countDefinedBindings(bindings map[string]string) int {
	count := 0
	for _, v := range bindings {
		if strings.TrimSpace(v) != "" {
			count++
		}
	}
	return count
}

func splitAddPath(p string) []string {
	clean := canonicalAddPath(p)
	if clean == "" {
		return nil
	}
	parts := strings.Split(clean, "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" || part == "." {
			continue
		}
		out = append(out, part)
	}
	return out
}

func canonicalAddPath(raw string) string {
	p := strings.TrimSpace(raw)
	p = strings.ReplaceAll(p, "\\", "/")
	p = path.Clean("/" + p)
	p = strings.TrimPrefix(p, "/")
	if p == "." {
		return ""
	}
	return p
}

func storageKindFromCID(c cid.Cid) string {
	if !c.Defined() {
		return ""
	}
	codec := c.Prefix().Codec
	switch codec {
	case 0x55:
		return "raw"
	case 0x300002, 0x300004:
		return "list"
	case 0x300001, 0x300003:
		return "map"
	default:
		return ""
	}
}

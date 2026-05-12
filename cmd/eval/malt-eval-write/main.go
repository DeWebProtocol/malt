// Package main provides the Git trace write-amplification evaluator.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/dewebprotocol/malt/cmd/eval/helper/adapters/hamt"
	"github.com/dewebprotocol/malt/cmd/eval/helper/adapters/maltflat"
	"github.com/dewebprotocol/malt/cmd/eval/helper/adapters/merkledag"
	gittrace "github.com/dewebprotocol/malt/cmd/eval/helper/git"
	"github.com/dewebprotocol/malt/cmd/eval/helper/replay"
	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
	"github.com/dewebprotocol/malt/internal/merkledagimport"
	"github.com/spf13/cobra"
)

var (
	repoURL      string
	repoPath     string
	repoRef      = "HEAD"
	commitLimit  int
	cacheDir     = ".eval-cache/repos"
	storeDir     = ".eval-cache/write-stores"
	storeMode    = string(evalstore.StoreModeIsolated)
	storeBackend = string(evalstore.StoreBackendMemory)
	systemsCSV   = "maltflat,merkledag,hamt"
	outPath      string
	firstParent  = true
)

var rootCmd = &cobra.Command{
	Use:   "malt-eval-write",
	Short: "Replay Git commit traces and emit write-amplification JSONL",
	Args:  cobra.NoArgs,
	RunE:  runEvalWrite,
}

func init() {
	rootCmd.Flags().StringVar(&repoURL, "repo-url", repoURL, "Git repository URL to clone and replay")
	rootCmd.Flags().StringVar(&repoPath, "repo-path", repoPath, "Existing local Git repository path to replay")
	rootCmd.Flags().StringVar(&repoRef, "repo-ref", repoRef, "Git ref to replay")
	rootCmd.Flags().IntVar(&commitLimit, "limit", commitLimit, "Maximum commits to replay; 0 means all")
	rootCmd.Flags().StringVar(&cacheDir, "cache-dir", cacheDir, "Repository clone cache directory")
	rootCmd.Flags().StringVar(&storeDir, "store-dir", storeDir, "Evaluation store directory for fs/badger backends")
	rootCmd.Flags().StringVar(&storeMode, "store-mode", storeMode, "Store mode: isolated or shared")
	rootCmd.Flags().StringVar(&storeBackend, "store-backend", storeBackend, "Store backend: memory, fs, or badger")
	rootCmd.Flags().StringVar(&systemsCSV, "systems", systemsCSV, "Comma-separated systems: maltflat, merkledag, hamt")
	rootCmd.Flags().StringVar(&outPath, "out", outPath, "Output JSONL file; defaults to stdout")
	rootCmd.Flags().BoolVar(&firstParent, "first-parent", firstParent, "Replay only the first-parent commit chain")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runEvalWrite(cmd *cobra.Command, args []string) error {
	if strings.TrimSpace(repoURL) == "" && strings.TrimSpace(repoPath) == "" {
		return fmt.Errorf("one of --repo-url or --repo-path is required")
	}
	factory, err := evalstore.NewFactory(evalstore.FactoryConfig{
		Mode:    evalstore.StoreMode(storeMode),
		Backend: evalstore.StoreBackend(storeBackend),
		RootDir: storeDir,
	})
	if err != nil {
		return err
	}
	defer factory.Close()

	systems, err := buildSystems(cmd.Context(), factory, systemsCSV)
	if err != nil {
		return err
	}
	writer, closeWriter, err := outputWriter(outPath)
	if err != nil {
		return err
	}
	defer closeWriter()

	enc := json.NewEncoder(writer)
	source := gittrace.Source{
		RepoURL:     repoURL,
		RepoPath:    repoPath,
		CacheDir:    cacheDir,
		Ref:         repoRef,
		Limit:       commitLimit,
		FirstParent: firstParent,
	}
	return source.Walk(cmd.Context(), func(commit replay.CommitMutation) error {
		return replay.RunCommit(cmd.Context(), commit, systems, enc)
	})
}

func buildSystems(ctx context.Context, factory *evalstore.Factory, csv string) ([]replay.SystemAdapter, error) {
	if factory == nil {
		return nil, fmt.Errorf("store factory is nil")
	}
	var systems []replay.SystemAdapter
	for _, raw := range strings.Split(csv, ",") {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		system, err := factory.NewSystem(ctx, name)
		if err != nil {
			return nil, err
		}
		switch name {
		case "maltflat":
			adapter, err := maltflat.New(system, maltflat.Options{})
			if err != nil {
				return nil, err
			}
			systems = append(systems, adapter)
		case "merkledag":
			systems = append(systems, merkledag.New(system, merkledag.Options{
				Name:      "merkledag",
				DirLayout: merkledagimport.DirLayoutBasic,
			}))
		case "hamt":
			systems = append(systems, hamt.New(system))
		default:
			return nil, fmt.Errorf("unknown system %q", raw)
		}
	}
	if len(systems) == 0 {
		return nil, fmt.Errorf("no systems selected")
	}
	return systems, nil
}

func outputWriter(path string) (io.Writer, func(), error) {
	if strings.TrimSpace(path) == "" {
		return os.Stdout, func() {}, nil
	}
	f, err := os.Create(path)
	if err != nil {
		return nil, nil, err
	}
	return f, func() { _ = f.Close() }, nil
}

func runWithContext(ctx context.Context, cmd *cobra.Command) error {
	cmd.SetContext(ctx)
	return cmd.Execute()
}

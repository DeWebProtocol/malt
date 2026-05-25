// Package evalwrite provides the Git trace write-amplification evaluation command.
package evalwrite

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
	"github.com/dewebprotocol/malt/cmd/internal/merkledagimport"
	"github.com/spf13/cobra"
)

type options struct {
	repoURL      string
	repoPath     string
	repoRef      string
	commitLimit  int
	cacheDir     string
	storeDir     string
	storeMode    string
	storeBackend string
	systemsCSV   string
	outPath      string
	firstParent  bool
}

// NewCommand creates the unified `malt-eval write` subcommand.
func NewCommand() *cobra.Command {
	return newCommand("write", "Replay Git commit traces and emit write-amplification JSONL")
}

func newCommand(use, short string) *cobra.Command {
	opts := &options{
		repoRef:      "HEAD",
		cacheDir:     ".eval-cache/repos",
		storeDir:     ".eval-cache/write-stores",
		storeMode:    string(evalstore.StoreModeIsolated),
		storeBackend: string(evalstore.StoreBackendMemory),
		systemsCSV:   "maltflat,merkledag,hamt",
		firstParent:  true,
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(cmd, opts)
		},
	}
	cmd.Flags().StringVar(&opts.repoURL, "repo-url", opts.repoURL, "Git repository URL to clone and replay")
	cmd.Flags().StringVar(&opts.repoPath, "repo-path", opts.repoPath, "Existing local Git repository path to replay")
	cmd.Flags().StringVar(&opts.repoRef, "repo-ref", opts.repoRef, "Git ref to replay")
	cmd.Flags().IntVar(&opts.commitLimit, "limit", opts.commitLimit, "Maximum commits to replay; 0 means all")
	cmd.Flags().StringVar(&opts.cacheDir, "cache-dir", opts.cacheDir, "Repository clone cache directory")
	cmd.Flags().StringVar(&opts.storeDir, "store-dir", opts.storeDir, "Evaluation store directory for fs/badger backends")
	cmd.Flags().StringVar(&opts.storeMode, "store-mode", opts.storeMode, "Store mode: isolated or shared")
	cmd.Flags().StringVar(&opts.storeBackend, "store-backend", opts.storeBackend, "Store backend: memory, fs, or badger")
	cmd.Flags().StringVar(&opts.systemsCSV, "systems", opts.systemsCSV, "Comma-separated systems: maltflat, merkledag, hamt")
	cmd.Flags().StringVar(&opts.outPath, "out", opts.outPath, "Output JSONL file; defaults to stdout")
	cmd.Flags().BoolVar(&opts.firstParent, "first-parent", opts.firstParent, "Replay only the first-parent commit chain")
	return cmd
}

func run(cmd *cobra.Command, opts *options) error {
	if strings.TrimSpace(opts.repoURL) == "" && strings.TrimSpace(opts.repoPath) == "" {
		return fmt.Errorf("one of --repo-url or --repo-path is required")
	}
	factory, err := evalstore.NewFactory(evalstore.FactoryConfig{
		Mode:    evalstore.StoreMode(opts.storeMode),
		Backend: evalstore.StoreBackend(opts.storeBackend),
		RootDir: opts.storeDir,
	})
	if err != nil {
		return err
	}
	defer factory.Close()

	systems, err := BuildSystems(cmd.Context(), factory, opts.systemsCSV)
	if err != nil {
		return err
	}
	writer, closeWriter, err := outputWriter(opts.outPath)
	if err != nil {
		return err
	}
	defer closeWriter()

	enc := json.NewEncoder(writer)
	source := gittrace.Source{
		RepoURL:     opts.repoURL,
		RepoPath:    opts.repoPath,
		CacheDir:    opts.cacheDir,
		Ref:         opts.repoRef,
		Limit:       opts.commitLimit,
		FirstParent: opts.firstParent,
	}
	return source.Walk(cmd.Context(), func(commit replay.CommitMutation) error {
		return replay.RunCommit(cmd.Context(), commit, systems, enc)
	})
}

// BuildSystems constructs the selected write-amplification system adapters.
func BuildSystems(ctx context.Context, factory *evalstore.Factory, csv string) ([]replay.SystemAdapter, error) {
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

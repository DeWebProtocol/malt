package main

import (
	"errors"
	"fmt"
	"strings"

	daemonclient "github.com/dewebprotocol/malt/client"
	"github.com/spf13/cobra"
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

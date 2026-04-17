package main

import (
	"encoding/base64"
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/core/graph"
	cid "github.com/ipfs/go-cid"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(proveCmd)
}

var proveGraphID string

var proveCmd = &cobra.Command{
	Use:   "prove [<root>] <path>",
	Short: "Generate a proof for a path resolution",
	Long: `Resolve a path and output the evidence (proof) for each step.

Examples:
  malt prove bafy... data/file.txt
  malt prove --graph my-graph data/file.txt`,
	Args: func(cmd *cobra.Command, args []string) error {
		if proveGraphID != "" {
			return cobra.ExactArgs(1)(cmd, args)
		}
		return cobra.ExactArgs(2)(cmd, args)
	},
	RunE: runProve,
}

func runProve(cmd *cobra.Command, args []string) error {
	var (
		g       *graph.Graph
		rootCid cid.Cid
		err     error
		path    string
	)

	if proveGraphID != "" {
		var meta *graph.GraphMeta
		g, meta = mustManagedGraph(proveGraphID, false)
		rootCid, err = managedGraphHeadRoot(meta)
		if err != nil {
			return err
		}
		path = args[0]
	} else {
		g = mustGraph()
		rootCid, err = parseCID(args[0])
		if err != nil {
			return err
		}
		path = args[1]
	}
	defer cleanupNode()

	result, err := g.Resolver().Resolve(rootCid, path)
	if err != nil {
		return fmt.Errorf("resolution failed: %w", err)
	}

	jsonOutput, _ := cmd.Flags().GetBool("json")
	if jsonOutput {
		steps := make([]map[string]string, len(result.Transcript.Steps))
		for i, step := range result.Transcript.Steps {
			kind := evidenceKindStr(step.Evidence.Kind())
			steps[i] = map[string]string{
				"path":     step.Path.String(),
				"target":   step.Target.String(),
				"evidence": base64.StdEncoding.EncodeToString(step.Evidence.Bytes()),
				"kind":     kind,
			}
		}
		printJSON(map[string]interface{}{
			"root":   rootCid.String(),
			"target": result.Target.String(),
			"steps":  steps,
		})
	} else {
		fmt.Fprintf(os.Stdout, "root:   %s\n", rootCid.String())
		fmt.Fprintf(os.Stdout, "target: %s\n", result.Target.String())
		fmt.Fprintf(os.Stdout, "path:   %s\n", path)
		fmt.Fprintf(os.Stdout, "\n")
		for i, step := range result.Transcript.Steps {
			kind := evidenceKindStr(step.Evidence.Kind())
			fmt.Fprintf(os.Stdout, "[%d] %s -> %s\n", i, step.Path.String(), step.Target)
			fmt.Fprintf(os.Stdout, "    evidence: %s (base64)\n", base64.StdEncoding.EncodeToString(step.Evidence.Bytes()))
			fmt.Fprintf(os.Stdout, "    kind:     %s\n", kind)
		}
	}

	return nil
}

func init() {
	proveCmd.Flags().BoolP("json", "j", false, "Output as JSON")
	proveCmd.Flags().StringVar(&proveGraphID, "graph", "", "Generate a proof from the managed graph head instead of an explicit root")
}

package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/dewebprotocol/malt/core/types/evidence"
	"github.com/spf13/cobra"
)

// evidenceKindStr converts an evidence.EvidenceKind to a string.
func evidenceKindStr(k evidence.EvidenceKind) string {
	switch k {
	case evidence.EvidenceKindExplicit:
		return "explicit"
	case evidence.EvidenceKindImplicit:
		return "implicit"
	case evidence.EvidenceKindHAMT:
		return "hamt"
	default:
		return "unknown"
	}
}

func init() {
	rootCmd.AddCommand(resolveCmd)
}

var resolveCmd = &cobra.Command{
	Use:   "resolve <root> [path]",
	Short: "Resolve a path through a MALT structure",
	Long: `Resolve a path starting from a MALT structure root or ordinary CID.
Native explicit-arc resolution is the primary path. Ordinary IPLD traversal is
used when resolution crosses into interoperable legacy CID space.

If no path is given, resolves to the structure root or payload.

Examples:
  malt resolve bafy... structure/root
  malt resolve bafy... data/file.txt
  malt resolve bafy...`,
	Args: cobra.RangeArgs(1, 2),
	RunE: runResolve,
}

func runResolve(cmd *cobra.Command, args []string) error {
	g := mustGraph()
	defer cleanupNode()

	rootStr := args[0]
	path := ""
	if len(args) > 1 {
		path = args[1]
	}

	rootCid, err := parseCID(rootStr)
	if err != nil {
		return err
	}

	result, err := g.Resolver().Resolve(rootCid, path)
	if err != nil {
		return fmt.Errorf("resolution failed: %w", err)
	}

	verbose, _ := cmd.Flags().GetBool("verbose")
	if verbose {
		printJSON(map[string]interface{}{
			"target": result.Target.String(),
			"steps":  len(result.Transcript.Steps),
		})
		fmt.Fprintf(os.Stderr, "\nResolution transcript:\n")
		for i, step := range result.Transcript.Steps {
			fmt.Fprintf(os.Stderr, "  [%d] %s -> %s (evidence: %s)\n",
				i, step.Path, step.Target, evidenceKindStr(step.Evidence.Kind()))
		}
	} else {
		fmt.Println(result.Target.String())
		if len(result.Transcript.Steps) > 0 {
			last := result.Transcript.Steps[len(result.Transcript.Steps)-1]
			_ = last
		}
		fmt.Fprintf(os.Stderr, "resolved via %d step(s)\n", len(result.Transcript.Steps))
	}

	// Show the resolution path
	if path != "" && len(result.Transcript.Steps) > 0 {
		matchedPaths := make([]string, len(result.Transcript.Steps))
		for i, step := range result.Transcript.Steps {
			matchedPaths[i] = step.Path
		}
		fmt.Fprintf(os.Stderr, "path matched: %s\n", strings.Join(matchedPaths, " -> "))
	}

	return nil
}

func init() {
	resolveCmd.Flags().BoolP("verbose", "v", false, "Show resolution transcript")
}

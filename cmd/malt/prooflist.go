package main

import (
	"encoding/json"
	"os"

	"github.com/dewebprotocol/malt/httpapi"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(proofListCmd)
	proofListCmd.Flags().BoolVar(&proofListCurrent, "current", false, "Generate a ProofList from the current root instead of an explicit root")
}

var proofListCurrent bool

var proofListCmd = &cobra.Command{
	Use:   "prooflist [<root>] <path>",
	Short: "Generate verifier-facing ProofList evidence for a path",
	Args: func(cmd *cobra.Command, args []string) error {
		if proofListCurrent {
			return cobra.ExactArgs(1)(cmd, args)
		}
		return cobra.ExactArgs(2)(cmd, args)
	},
	RunE: runProofList,
}

func runProofList(cmd *cobra.Command, args []string) error {
	client := mustDaemonClient()

	var (
		result *httpapi.ProofListResponse
		err    error
	)
	if proofListCurrent {
		result, err = client.ProofListCurrent(cmd.Context(), args[0])
	} else {
		result, err = client.ProofListRoot(cmd.Context(), args[0], args[1])
	}
	if err != nil {
		return daemonCommandError(err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

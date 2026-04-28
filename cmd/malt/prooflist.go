package main

import (
	"encoding/json"
	"os"

	"github.com/dewebprotocol/malt/httpapi"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(proofListCmd)
	proofListCmd.Flags().StringVarP(&proofListBucketID, "bucket", "b", "", "Generate a ProofList from the managed bucket head instead of an explicit root")
}

var proofListBucketID string

var proofListCmd = &cobra.Command{
	Use:   "prooflist [<root>] <path>",
	Short: "Generate verifier-facing ProofList evidence for a path",
	Args: func(cmd *cobra.Command, args []string) error {
		if proofListBucketID != "" {
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
	if proofListBucketID != "" {
		result, err = client.ProofListBucket(cmd.Context(), proofListBucketID, args[0])
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

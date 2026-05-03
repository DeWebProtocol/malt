package main

import (
	"encoding/json"
	"os"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(proofListCmd)
}

var proofListCmd = &cobra.Command{
	Use:   "prooflist <root> <path>",
	Short: "Generate verifier-facing ProofList evidence for a path",
	Args:  cobra.ExactArgs(2),
	RunE:  runProofList,
}

func runProofList(cmd *cobra.Command, args []string) error {
	client := mustDaemonClient()

	result, err := client.ProofList(cmd.Context(), args[0], args[1])
	if err != nil {
		return daemonCommandError(err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

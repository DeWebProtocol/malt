package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/dewebprotocol/malt/httpapi"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(semanticMutationCmd)
	semanticMutationCmd.Flags().StringVarP(&semanticMutationBucketID, "bucket", "b", "", "Bucket ID to mutate")
	semanticMutationCmd.Flags().StringVarP(&semanticMutationFile, "file", "f", "", "Request JSON file, or - for stdin")
}

var (
	semanticMutationBucketID string
	semanticMutationFile     string
)

var semanticMutationCmd = &cobra.Command{
	Use:   "semantic-mutation --bucket <id> --file <path|->",
	Short: "Apply a bucket semantic mutation request",
	Args:  cobra.NoArgs,
	RunE:  runSemanticMutation,
}

func runSemanticMutation(cmd *cobra.Command, args []string) error {
	if semanticMutationBucketID == "" {
		return fmt.Errorf("--bucket is required")
	}
	if semanticMutationFile == "" {
		return fmt.Errorf("--file is required")
	}

	req, err := readSemanticMutationRequest(semanticMutationFile)
	if err != nil {
		return err
	}

	resp, err := mustDaemonClient().ApplyBucketSemanticMutation(cmd.Context(), semanticMutationBucketID, req)
	if err != nil {
		return daemonCommandError(err)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(resp)
}

func readSemanticMutationRequest(path string) (*httpapi.BucketSemanticMutationRequest, error) {
	var r io.Reader
	if path == "-" {
		r = os.Stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("read semantic mutation request %q: %w", path, err)
		}
		defer f.Close()
		r = f
	}

	var req httpapi.BucketSemanticMutationRequest
	dec := json.NewDecoder(r)
	if err := dec.Decode(&req); err != nil {
		return nil, fmt.Errorf("decode semantic mutation request: %w", err)
	}
	return &req, nil
}

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/dewebprotocol/malt/core/types/prooflist"
	"github.com/dewebprotocol/malt/httpapi"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(verifyCmd)
}

var verifyCmd = &cobra.Command{
	Use:   "verify --prooflist <file|->",
	Short: "Verify a ProofList",
	Long: `Verify that a ProofList is valid.
The ProofList can be provided as a JSON file or via stdin. The input may be a
bare ProofList or a resolve response containing a prooflist field.

Examples:
  malt verify --prooflist resolve.json
  malt verify --prooflist -`,
	Args: cobra.NoArgs,
	RunE: runVerify,
}

func init() {
	verifyCmd.Flags().String("prooflist", "", "Path to ProofList JSON file, resolve JSON file, or - for stdin")
	_ = verifyCmd.MarkFlagRequired("prooflist")
}

func runVerify(cmd *cobra.Command, args []string) error {
	client := mustDaemonClient()

	pl, err := readProofListInput(cmd)
	if err != nil {
		return err
	}

	resp, err := client.Verify(cmd.Context(), &httpapi.VerifyRequest{
		ProofList: *pl,
	})
	if err != nil {
		return daemonCommandError(err)
	}

	if resp.Valid {
		fmt.Println("valid: true")
		fmt.Fprintf(os.Stderr, "ProofList verified successfully\n")
	} else {
		fmt.Println("valid: false")
		fmt.Fprintf(os.Stderr, "ProofList verification failed\n")
	}

	return nil
}

func readProofListInput(cmd *cobra.Command) (*prooflist.ProofList, error) {
	proofPath, _ := cmd.Flags().GetString("prooflist")
	if proofPath == "" {
		return nil, fmt.Errorf("--prooflist flag is required")
	}

	var (
		data []byte
		err  error
	)
	if proofPath == "-" {
		data, err = io.ReadAll(os.Stdin)
	} else {
		data, err = os.ReadFile(proofPath)
	}
	if err != nil {
		return nil, fmt.Errorf("reading ProofList: %w", err)
	}

	var wrapped struct {
		ProofList *prooflist.ProofList `json:"prooflist"`
	}
	if err := json.Unmarshal(data, &wrapped); err == nil && wrapped.ProofList != nil {
		return wrapped.ProofList, nil
	}

	var pl prooflist.ProofList
	if err := json.Unmarshal(data, &pl); err != nil {
		return nil, fmt.Errorf("parsing ProofList: %w", err)
	}
	return &pl, nil
}

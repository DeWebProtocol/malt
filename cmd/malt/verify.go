package main

import (
	"fmt"
	"io"
	"os"

	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	authverifier "github.com/dewebprotocol/malt-core/sdk/authentication/verifier"
	"github.com/spf13/cobra"
)

var verifyCmd = &cobra.Command{
	Use:   "verify --request <file> --result <file|->",
	Short: "Verify typed authentication against a caller-selected request",
	Long: `Verify an authentication result against an independently selected request.
The request JSON fixes the Root, explicit typed steps, and resolve, binding,
or range operation. The result JSON may be read from a file or stdin.
Successful verification authenticates bindings; it does not accept a root.`,
	Args: cobra.NoArgs,
	RunE: runVerify,
}

func init() {
	verifyCmd.Flags().String("request", "", "Caller-selected authentication request JSON")
	verifyCmd.Flags().String("result", "", "Authentication result JSON file or - for stdin")
	_ = verifyCmd.MarkFlagRequired("request")
	_ = verifyCmd.MarkFlagRequired("result")
	rootCmd.AddCommand(verifyCmd)
}

func runVerify(cmd *cobra.Command, _ []string) error {
	requestPath, _ := cmd.Flags().GetString("request")
	resultPath, _ := cmd.Flags().GetString("result")
	if requestPath == "-" && resultPath == "-" {
		return fmt.Errorf("request and result cannot both read stdin")
	}
	requestJSON, err := readVerificationInput(cmd, requestPath)
	if err != nil {
		return err
	}
	request, err := protocol.DecodeAuthenticationRequest(requestJSON)
	if err != nil {
		return fmt.Errorf("decode selected authentication request: %w", err)
	}
	resultJSON, err := readVerificationInput(cmd, resultPath)
	if err != nil {
		return err
	}
	result, err := protocol.DecodeAuthenticationResult(resultJSON)
	if err != nil {
		return fmt.Errorf("decode authentication result: %w", err)
	}
	e, err := authverifier.New(nil)
	if err != nil {
		return err
	}
	valid, err := authentication.Verify(e, request, result)
	if err != nil {
		return fmt.Errorf("verify authentication locally: %w", err)
	}
	if !valid {
		return fmt.Errorf("authentication proof does not match the selected Root and query")
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), "valid: true")
	return err
}

func readVerificationInput(cmd *cobra.Command, path string) ([]byte, error) {
	if path == "" {
		return nil, fmt.Errorf("verification input path is required")
	}
	source := cmd.InOrStdin()
	if path != "-" {
		file, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer file.Close()
		source = file
	}
	data, err := io.ReadAll(io.LimitReader(source, protocol.MaxVerificationJSONBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > protocol.MaxVerificationJSONBytes {
		return nil, fmt.Errorf("verification JSON exceeds size limit")
	}
	return data, nil
}

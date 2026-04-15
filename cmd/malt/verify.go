package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/types/evidence"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(verifyCmd)
}

var verifyCmd = &cobra.Command{
	Use:   "verify <root> --transcript <file>",
	Short: "Verify a resolution transcript",
	Long: `Verify that a recorded resolution transcript is valid for a given root.
The transcript can be provided as a JSON file or via stdin.

Examples:
  malt verify bafy... --transcript transcript.json`,
	Args: cobra.ExactArgs(1),
	RunE: runVerify,
}

// TranscriptInput is the JSON format expected for verification input.
type TranscriptInput struct {
	Root  string      `json:"root"`
	Path  string      `json:"path"`
	Steps []StepInput `json:"steps"`
}

// StepInput represents a single resolution step in JSON form.
type StepInput struct {
	Path     string `json:"path"`
	Target   string `json:"target"`
	Evidence string `json:"evidence"` // base64-encoded
	Kind     string `json:"kind"`
}

func init() {
	verifyCmd.Flags().String("transcript", "", "Path to transcript JSON file")
	_ = verifyCmd.MarkFlagRequired("transcript")
}

func runVerify(cmd *cobra.Command, args []string) error {
	g := mustGraph()
	defer cleanupNode()

	rootCid, err := parseCID(args[0])
	if err != nil {
		return err
	}

	transcriptPath, _ := cmd.Flags().GetString("transcript")
	if transcriptPath == "" {
		return fmt.Errorf("--transcript flag is required")
	}

	data, err := os.ReadFile(transcriptPath)
	if err != nil {
		return fmt.Errorf("reading transcript: %w", err)
	}

	var input TranscriptInput
	if err := json.Unmarshal(data, &input); err != nil {
		return fmt.Errorf("parsing transcript: %w", err)
	}

	steps := make([]resolver.StepEvidence, len(input.Steps))
	for i, step := range input.Steps {
		targetCid, err := parseCID(step.Target)
		if err != nil {
			return fmt.Errorf("step %d: invalid target CID: %w", i, err)
		}

		evBytes, err := base64.StdEncoding.DecodeString(step.Evidence)
		if err != nil {
			return fmt.Errorf("step %d: invalid evidence: %w", i, err)
		}

		var ev evidence.Evidence
		switch step.Kind {
		case "explicit":
			ev = evidence.NewExplicitEvidence(evBytes)
		case "implicit":
			ev = evidence.NewImplicitEvidence(evBytes)
		case "hamt":
			ev = evidence.NewHAMTEvidence(evBytes)
		default:
			return fmt.Errorf("step %d: unknown evidence kind %q", i, step.Kind)
		}

		steps[i] = resolver.StepEvidence{
			Path:     step.Path,
			Target:   targetCid,
			Evidence: ev,
		}
	}

	transcript := &resolver.Transcript{Steps: steps}
	valid, err := g.Resolver().VerifyTranscript(rootCid, transcript)
	if err != nil {
		return fmt.Errorf("verification failed: %w", err)
	}

	if valid {
		fmt.Println("valid: true")
		fmt.Fprintf(os.Stderr, "transcript verified successfully\n")
	} else {
		fmt.Println("valid: false")
		fmt.Fprintf(os.Stderr, "transcript verification failed\n")
	}

	return nil
}

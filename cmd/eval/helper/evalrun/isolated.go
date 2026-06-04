package evalrun

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/framework"
	"github.com/spf13/cobra"
)

const (
	defaultAPIBaseURL  = "http://127.0.0.1:4317"
	defaultCASEndpoint = "http://127.0.0.1:4318"
	defaultHealthWait  = 15 * time.Second
)

// RunIsolated returns a cobra RunE that runs an evaluation plan against an
// already-running malt daemon.
func RunIsolated(registry framework.Registry) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		planPath, _ := cmd.Flags().GetString("plan")
		if strings.TrimSpace(planPath) == "" {
			return fmt.Errorf("--plan is required")
		}

		plan, err := framework.LoadPlan(planPath)
		if err != nil {
			return err
		}

		// Determine endpoints: use plan values or defaults.
		apiBase := plan.APIBaseURL
		casEndpoint := plan.CASEndpoint
		if apiBase == "" {
			apiBase = defaultAPIBaseURL
		}
		if casEndpoint == "" {
			casEndpoint = defaultCASEndpoint
		}
		plan.APIBaseURL = apiBase
		plan.CASEndpoint = casEndpoint

		// Wait for the daemon to be reachable.
		if planRequiresDaemon(plan) {
			if err := waitForHealth(cmd.Context(), apiBase, defaultHealthWait); err != nil {
				return fmt.Errorf("daemon not reachable at %s: %w", apiBase, err)
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "daemon ready at %s (CAS: %s)\n", apiBase, casEndpoint)
		}

		// Create timestamped eval output directory.
		evalDir := filepath.Join("eval", time.Now().UTC().Format("20060102150405"))
		if err := os.MkdirAll(evalDir, 0o755); err != nil {
			return fmt.Errorf("create eval dir: %w", err)
		}
		plan.OverrideResultDir(filepath.Join(evalDir, "result", plan.RunID))
		plan.OverrideOutputDir(filepath.Join(evalDir, "output", plan.RunID))

		runOpts := framework.RunOptions{}
		if stderr, ok := cmd.ErrOrStderr().(*os.File); ok {
			runOpts.Stderr = stderr
		}
		err = framework.Run(cmd.Context(), plan, registry, runOpts)

		resultDir := filepath.Join(evalDir, "result", plan.RunID)
		fmt.Fprintf(cmd.ErrOrStderr(), "\nresults: %s\n", resultDir)
		if _, statErr := os.Stat(filepath.Join(resultDir, "manifest.json")); statErr == nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "manifest: %s\n", filepath.Join(resultDir, "manifest.json"))
		}

		return err
	}
}

func planRequiresDaemon(plan framework.Plan) bool {
	for _, suite := range plan.Suites {
		if !suite.EnabledOrDefault() {
			continue
		}
		if suite.Name == "read_query" {
			return true
		}
	}
	return false
}

// waitForHealth polls the daemon's /health endpoint until it responds 200.
func waitForHealth(ctx context.Context, baseURL string, timeout time.Duration) error {
	url := strings.TrimRight(baseURL, "/") + "/health"
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("daemon at %s did not become healthy within %s", baseURL, timeout)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

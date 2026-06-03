package evalrun

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/framework"
	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/daemon"
	"github.com/spf13/cobra"
)

// NewIsolatedCommand creates `malt-eval run-isolated`.
func NewIsolatedCommand(registry framework.Registry) *cobra.Command {
	opts := &options{}
	cmd := &cobra.Command{
		Use:   "run-isolated",
		Short: "Run an evaluation plan in an isolated timestamped directory with an embedded daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIsolated(cmd, registry, opts)
		},
	}
	cmd.Flags().StringVar(&opts.planPath, "plan", opts.planPath, "Evaluation plan JSON file")
	return cmd
}

func runIsolated(cmd *cobra.Command, registry framework.Registry, opts *options) error {
	if strings.TrimSpace(opts.planPath) == "" {
		return fmt.Errorf("--plan is required")
	}

	// Create timestamped eval directory.
	evalDir := filepath.Join("eval", time.Now().UTC().Format("20060102150405"))
	if err := os.MkdirAll(evalDir, 0o755); err != nil {
		return fmt.Errorf("create eval dir: %w", err)
	}

	// Pick a free port for the daemon.
	port, err := pickFreePort()
	if err != nil {
		return fmt.Errorf("pick free port: %w", err)
	}
	listen := fmt.Sprintf("127.0.0.1:%d", port)
	casListen := fmt.Sprintf("127.0.0.1:%d", port+1)

	// Configure daemon with isolated state.
	cfg := config.DefaultConfig()
	cfg.State.RootDir = filepath.Join(evalDir, "state")
	cfg.State.KVStore.Type = "memory"
	cfg.RPC.Listen = listen
	cfg.CAS.EmbeddedMock.Listen = casListen
	cfg.Logging.Level = "warn"

	// Start daemon.
	fmt.Fprintf(cmd.ErrOrStderr(), "starting daemon on %s (state: %s)\n", listen, cfg.State.RootDir)
	handle, err := daemon.Start(cfg, daemon.RunOptions{
		ListenOverride: listen,
		Stdout:         cmd.ErrOrStderr(),
		Stderr:         cmd.ErrOrStderr(),
	})
	if err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = handle.Shutdown(shutCtx)
	}()

	// Wait for daemon to be ready.
	apiBase := fmt.Sprintf("http://%s", listen)
	if err := waitForHealth(cmd.Context(), apiBase); err != nil {
		return fmt.Errorf("daemon health check: %w", err)
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "daemon ready at %s\n", apiBase)

	// Load plan and inject directories.
	plan, err := framework.LoadPlan(opts.planPath)
	if err != nil {
		return err
	}
	plan.OverrideResultDir(filepath.Join(evalDir, "result", plan.RunID))
	plan.OverrideOutputDir(filepath.Join(evalDir, "output", plan.RunID))
	injectAPIBaseURL(&plan, apiBase)

	// Run the evaluation framework.
	err = framework.Run(cmd.Context(), plan, registry, framework.RunOptions{})

	resultDir := filepath.Join(evalDir, "result", plan.RunID)
	fmt.Fprintf(cmd.ErrOrStderr(), "\nresults: %s\n", resultDir)
	if _, statErr := os.Stat(filepath.Join(resultDir, "manifest.json")); statErr == nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "manifest: %s\n", filepath.Join(resultDir, "manifest.json"))
	}

	return err
}

// pickFreePort asks the OS for a free port.
func pickFreePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// waitForHealth polls the daemon's /health endpoint until it responds 200.
func waitForHealth(ctx context.Context, baseURL string) error {
	url := strings.TrimRight(baseURL, "/") + "/health"
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
		time.Sleep(200 * time.Millisecond)
	}
}

// injectAPIBaseURL patches read_query suite configs with the daemon URL.
func injectAPIBaseURL(plan *framework.Plan, baseURL string) {
	for i := range plan.Suites {
		if plan.Suites[i].Name != "read_query" {
			continue
		}
		var cfg map[string]json.RawMessage
		if err := json.Unmarshal(plan.Suites[i].Config, &cfg); err != nil {
			continue
		}
		if _, ok := cfg["api_base_url"]; !ok {
			cfg["api_base_url"] = json.RawMessage(`"` + baseURL + `"`)
			patched, err := json.Marshal(cfg)
			if err == nil {
				plan.Suites[i].Config = patched
			}
		}
	}
}

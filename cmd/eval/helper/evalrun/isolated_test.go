package evalrun

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/framework"
	"github.com/spf13/cobra"
)

func TestRunIsolatedRunsLocalOnlyPlanWithoutDaemon(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)
	planPath := filepath.Join(tmp, "plan.json")
	if err := os.WriteFile(planPath, []byte(`{
  "run_id": "local-only",
  "api_base_url": "http://127.0.0.1:1",
  "cas_endpoint": "http://127.0.0.1:2",
  "suites": [{"name": "local_suite"}]
}`), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	reg := framework.NewRegistry()
	if err := reg.Register(localOnlySuite{}); err != nil {
		t.Fatalf("register suite: %v", err)
	}
	cmd := &cobra.Command{}
	cmd.Flags().String("plan", planPath, "")
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	cmd.SetContext(ctx)

	if err := RunIsolated(reg)(cmd, nil); err != nil {
		t.Fatalf("RunIsolated: %v", err)
	}
}

func TestRunIsolatedPreservesExplicitPlanDirectories(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)
	planPath := filepath.Join(tmp, "plan.json")
	if err := os.WriteFile(planPath, []byte(`{
  "run_id": "explicit-dirs",
  "api_base_url": "http://127.0.0.1:1",
  "cas_endpoint": "http://127.0.0.1:2",
  "output_dir": "explicit-output",
  "result_dir": "explicit-result",
  "suites": [{"name": "local_suite"}]
}`), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	reg := framework.NewRegistry()
	if err := reg.Register(localOnlySuite{}); err != nil {
		t.Fatalf("register suite: %v", err)
	}
	cmd := &cobra.Command{}
	cmd.Flags().String("plan", planPath, "")
	cmd.SetContext(context.Background())

	if err := RunIsolated(reg)(cmd, nil); err != nil {
		t.Fatalf("RunIsolated: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "explicit-result", "manifest.json")); err != nil {
		t.Fatalf("explicit result manifest missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "explicit-output", "logs")); err != nil {
		t.Fatalf("explicit output logs missing: %v", err)
	}
}

func TestRunIsolatedWaitsForSuitesThatRequireDaemon(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)
	planPath := filepath.Join(tmp, "plan.json")
	if err := os.WriteFile(planPath, []byte(`{
  "run_id": "daemon-required",
  "api_base_url": "http://127.0.0.1:1",
  "suites": [{"name": "daemon_suite"}]
}`), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	reg := framework.NewRegistry()
	if err := reg.Register(daemonRequiredSuite{}); err != nil {
		t.Fatalf("register suite: %v", err)
	}
	cmd := &cobra.Command{}
	cmd.Flags().String("plan", planPath, "")
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	cmd.SetContext(ctx)

	err := RunIsolated(reg)(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "daemon not reachable") {
		t.Fatalf("RunIsolated error = %v, want daemon reachability error", err)
	}
}

func TestWaitForHealthHonorsContextDuringPollSleep(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(20*time.Millisecond, cancel)

	start := time.Now()
	err := waitForHealth(ctx, ts.URL, time.Second)
	if err != context.Canceled {
		t.Fatalf("waitForHealth error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 150*time.Millisecond {
		t.Fatalf("waitForHealth took %s after cancellation", elapsed)
	}
}

type localOnlySuite struct{}

func (localOnlySuite) Name() string { return "local_suite" }

func (localOnlySuite) Run(ctx context.Context, env framework.Env, cfg json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if env.APIBaseURL != "http://127.0.0.1:1" {
		return fmt.Errorf("APIBaseURL = %q", env.APIBaseURL)
	}
	if env.CASEndpoint != "http://127.0.0.1:2" {
		return fmt.Errorf("CASEndpoint = %q", env.CASEndpoint)
	}
	return env.WriteRecord("local_suite", map[string]any{"ok": true})
}

type daemonRequiredSuite struct{}

func (daemonRequiredSuite) Name() string { return "daemon_suite" }

func (daemonRequiredSuite) RequiresDaemon() bool { return true }

func (daemonRequiredSuite) Run(context.Context, framework.Env, json.RawMessage) error {
	return nil
}

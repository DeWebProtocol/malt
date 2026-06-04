package evalrun

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

package evalrun

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dewebprotocol/malt/internal/eval/framework"
)

func TestCommandRunsPlanWithOverrides(t *testing.T) {
	tmp := t.TempDir()
	planPath := filepath.Join(tmp, "plan.json")
	if err := os.WriteFile(planPath, []byte(`{
		"run_id": "from-plan",
		"suites": [{"name": "fake_suite", "config": {"value": "ok"}}]
	}`), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	reg := framework.NewRegistry()
	if err := reg.Register(fakeSuite{}); err != nil {
		t.Fatalf("register fake suite: %v", err)
	}
	outDir := filepath.Join(tmp, "out")
	cmd := NewCommand(reg)
	cmd.SetArgs([]string{"--plan", planPath, "--out", outDir, "--run-id", "from-flags"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	manifestBytes, err := os.ReadFile(filepath.Join(outDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest framework.Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if manifest.RunID != "from-flags" {
		t.Fatalf("manifest run id = %q, want from-flags", manifest.RunID)
	}
	if _, err := os.Stat(filepath.Join(outDir, "raw", "fake_suite.jsonl")); err != nil {
		t.Fatalf("raw suite output missing: %v", err)
	}
}

func TestCommandRequiresPlan(t *testing.T) {
	cmd := NewCommand(framework.NewRegistry())
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err == nil {
		t.Fatal("Execute should require --plan")
	}
}

type fakeSuite struct{}

func (fakeSuite) Name() string { return "fake_suite" }

func (fakeSuite) Run(ctx context.Context, env framework.Env, cfg json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return env.WriteRecord("fake_suite", map[string]any{"ok": true})
}

package framework

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPlanDefaultsAndPreservesSuiteConfig(t *testing.T) {
	tmp := t.TempDir()
	planPath := filepath.Join(tmp, "plan.json")
	raw := `{
		"run_id": "paper-eval",
		"suites": [
			{
				"name": "write_trace",
				"config": {
					"systems": ["maltflat", "merkledag"],
					"commit_limit": 25
				}
			},
			{
				"name": "proof_overhead",
				"enabled": false,
				"config": {"iterations": 3}
			}
		]
	}`
	if err := os.WriteFile(planPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	plan, err := LoadPlan(planPath)
	if err != nil {
		t.Fatalf("LoadPlan: %v", err)
	}

	if plan.RunID != "paper-eval" {
		t.Fatalf("run id = %q, want paper-eval", plan.RunID)
	}
	if plan.OutputDir != filepath.Join("results", "paper-eval") {
		t.Fatalf("output dir = %q", plan.OutputDir)
	}
	if len(plan.Suites) != 2 {
		t.Fatalf("suites len = %d, want 2", len(plan.Suites))
	}
	if !plan.Suites[0].EnabledOrDefault() {
		t.Fatal("suite without enabled flag should default to enabled")
	}
	if plan.Suites[1].EnabledOrDefault() {
		t.Fatal("suite with enabled=false should be disabled")
	}

	var cfg struct {
		Systems     []string `json:"systems"`
		CommitLimit int      `json:"commit_limit"`
	}
	if err := json.Unmarshal(plan.Suites[0].Config, &cfg); err != nil {
		t.Fatalf("unmarshal suite config: %v", err)
	}
	if cfg.CommitLimit != 25 || len(cfg.Systems) != 2 || cfg.Systems[0] != "maltflat" {
		t.Fatalf("suite config = %#v", cfg)
	}
}

func TestPlanRejectsRunIDPathSegments(t *testing.T) {
	for _, runID := range []string{".", "..", "../outside", "nested/run", `nested\run`} {
		plan := Plan{
			RunID:  runID,
			Suites: []SuitePlan{{Name: "write_trace"}},
		}
		if err := plan.Normalize(); err == nil {
			t.Fatalf("Normalize should reject run_id %q", runID)
		}
	}
}

func TestRegistryRejectsDuplicateAndMissingSuites(t *testing.T) {
	reg := NewRegistry()
	suite := fakeSuite{name: "write_trace"}
	if err := reg.Register(suite); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if err := reg.Register(suite); err == nil {
		t.Fatal("duplicate register should fail")
	}
	if got, ok := reg.Lookup("write_trace"); !ok || got.Name() != "write_trace" {
		t.Fatalf("lookup write_trace = %v/%v", got, ok)
	}
	if _, ok := reg.Lookup("missing"); ok {
		t.Fatal("missing suite should not be found")
	}
	if names := reg.Names(); len(names) != 1 || names[0] != "write_trace" {
		t.Fatalf("registry names = %#v", names)
	}
}

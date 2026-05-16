package framework

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRunnerCreatesEvaluationOutputLayout(t *testing.T) {
	tmp := t.TempDir()
	reg := NewRegistry()
	if err := reg.Register(fakeSuite{name: "write_trace"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	plan := Plan{
		RunID:     "run-001",
		OutputDir: filepath.Join(tmp, "run-001"),
		Suites: []SuitePlan{
			{Name: "write_trace", Config: json.RawMessage(`{"limit": 2}`)},
			{Name: "proof_overhead", Enabled: boolPtr(false)},
		},
	}
	clock := func() time.Time { return time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC) }

	if err := Run(context.Background(), plan, reg, RunOptions{Clock: clock}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	for _, dir := range []string{"raw", "summary", "logs"} {
		if info, err := os.Stat(filepath.Join(plan.OutputDir, dir)); err != nil || !info.IsDir() {
			t.Fatalf("expected %s directory, info=%v err=%v", dir, info, err)
		}
	}

	rawPath := filepath.Join(plan.OutputDir, "raw", "write_trace.jsonl")
	f, err := os.Open(rawPath)
	if err != nil {
		t.Fatalf("open raw output: %v", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	if !scanner.Scan() {
		t.Fatalf("expected one raw line, err=%v", scanner.Err())
	}
	var record RecordEnvelope
	if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
		t.Fatalf("unmarshal raw record: %v", err)
	}
	if record.RunID != "run-001" || record.Suite != "write_trace" || record.SchemaVersion != SchemaVersion {
		t.Fatalf("record envelope = %#v", record)
	}

	manifestBytes, err := os.ReadFile(filepath.Join(plan.OutputDir, "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatalf("unmarshal manifest: %v", err)
	}
	if manifest.RunID != "run-001" || len(manifest.Suites) != 1 || manifest.Suites[0].Name != "write_trace" {
		t.Fatalf("manifest = %#v", manifest)
	}
	if manifest.StartedAt != clock().Format(time.RFC3339Nano) || manifest.FinishedAt != clock().Format(time.RFC3339Nano) {
		t.Fatalf("manifest times = %s/%s", manifest.StartedAt, manifest.FinishedAt)
	}
}

func TestRunnerFailsForUnknownEnabledSuite(t *testing.T) {
	plan := Plan{
		RunID:     "run-unknown",
		OutputDir: filepath.Join(t.TempDir(), "run-unknown"),
		Suites:    []SuitePlan{{Name: "missing"}},
	}
	if err := Run(context.Background(), plan, NewRegistry(), RunOptions{}); err == nil {
		t.Fatal("Run should fail for unknown enabled suite")
	}
}

type fakeSuite struct {
	name string
}

func (s fakeSuite) Name() string { return s.name }

func (s fakeSuite) Run(ctx context.Context, env Env, cfg json.RawMessage) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	var parsed struct {
		Limit int `json:"limit"`
	}
	if len(cfg) > 0 {
		if err := json.Unmarshal(cfg, &parsed); err != nil {
			return err
		}
	}
	return env.WriteRecord(s.name, map[string]any{
		"limit": parsed.Limit,
	})
}

func boolPtr(v bool) *bool {
	return &v
}

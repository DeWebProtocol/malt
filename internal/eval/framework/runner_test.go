package framework

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

	summaryBytes, err := os.ReadFile(filepath.Join(plan.OutputDir, "summary", "figure_write_trace.csv"))
	if err != nil {
		t.Fatalf("read summary csv: %v", err)
	}
	if got := string(summaryBytes); !strings.Contains(got, "limit") || !strings.Contains(got, "run-001") {
		t.Fatalf("summary csv did not include flattened fake record: %s", got)
	}
}

func TestRunnerRefreshesOutputDirectoriesBeforeRun(t *testing.T) {
	tmp := t.TempDir()
	reg := NewRegistry()
	if err := reg.Register(fakeSuite{name: "write_trace"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	plan := Plan{
		RunID:     "rerun",
		OutputDir: filepath.Join(tmp, "rerun"),
		Suites: []SuitePlan{{
			Name:   "write_trace",
			Config: json.RawMessage(`{"limit": 1}`),
		}},
	}

	if err := Run(context.Background(), plan, reg, RunOptions{}); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	if err := os.WriteFile(filepath.Join(plan.OutputDir, "summary", "stale.csv"), []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("write stale summary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(plan.OutputDir, "logs", "stale.log"), []byte("stale\n"), 0o644); err != nil {
		t.Fatalf("write stale log: %v", err)
	}

	plan.Suites[0].Config = json.RawMessage(`{"limit": 2}`)
	if err := Run(context.Background(), plan, reg, RunOptions{}); err != nil {
		t.Fatalf("second Run: %v", err)
	}

	rawBytes, err := os.ReadFile(filepath.Join(plan.OutputDir, "raw", "write_trace.jsonl"))
	if err != nil {
		t.Fatalf("read raw output: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(rawBytes)), "\n")
	if len(lines) != 1 {
		t.Fatalf("raw line count after rerun = %d, want 1\n%s", len(lines), rawBytes)
	}
	var envelope RecordEnvelope
	if err := json.Unmarshal([]byte(lines[0]), &envelope); err != nil {
		t.Fatalf("unmarshal rerun envelope: %v", err)
	}
	if strings.Contains(string(envelope.Record), `"limit":1`) || !strings.Contains(string(envelope.Record), `"limit":2`) {
		t.Fatalf("rerun record = %s, want only latest limit", envelope.Record)
	}
	if _, err := os.Stat(filepath.Join(plan.OutputDir, "summary", "stale.csv")); !os.IsNotExist(err) {
		t.Fatalf("stale summary file still present or stat failed unexpectedly: %v", err)
	}
	if _, err := os.Stat(filepath.Join(plan.OutputDir, "logs", "stale.log")); !os.IsNotExist(err) {
		t.Fatalf("stale log file still present or stat failed unexpectedly: %v", err)
	}
}

func TestRunnerRemovesStaleManifestBeforeFailedRerun(t *testing.T) {
	tmp := t.TempDir()
	successRegistry := NewRegistry()
	if err := successRegistry.Register(fakeSuite{name: "write_trace"}); err != nil {
		t.Fatalf("Register success suite: %v", err)
	}
	plan := Plan{
		RunID:     "failed-rerun",
		OutputDir: filepath.Join(tmp, "failed-rerun"),
		Suites: []SuitePlan{{
			Name:   "write_trace",
			Config: json.RawMessage(`{"limit": 1}`),
		}},
	}

	if err := Run(context.Background(), plan, successRegistry, RunOptions{}); err != nil {
		t.Fatalf("first Run: %v", err)
	}
	manifestPath := filepath.Join(plan.OutputDir, "manifest.json")
	if _, err := os.Stat(manifestPath); err != nil {
		t.Fatalf("initial manifest missing: %v", err)
	}

	failingRegistry := NewRegistry()
	if err := failingRegistry.Register(failingSuite{name: "write_trace"}); err != nil {
		t.Fatalf("Register failing suite: %v", err)
	}
	if err := Run(context.Background(), plan, failingRegistry, RunOptions{}); err == nil {
		t.Fatal("second Run should fail")
	}
	if _, err := os.Stat(manifestPath); !os.IsNotExist(err) {
		t.Fatalf("stale manifest should be removed before failed rerun, stat err=%v", err)
	}
}

func TestRunnerPreflightsSuitesBeforeRefreshingOutput(t *testing.T) {
	tmp := t.TempDir()
	outputDir := filepath.Join(tmp, "preflight")
	for _, dir := range []string{"raw", "summary", "logs"} {
		if err := os.MkdirAll(filepath.Join(outputDir, dir), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	sentinels := map[string]string{
		filepath.Join(outputDir, "manifest.json"):            "previous manifest\n",
		filepath.Join(outputDir, "raw", "write_trace.jsonl"): "previous raw\n",
		filepath.Join(outputDir, "summary", "stale.csv"):     "previous summary\n",
		filepath.Join(outputDir, "logs", "previous-run.log"): "previous logs\n",
	}
	for path, content := range sentinels {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write sentinel %s: %v", path, err)
		}
	}
	plan := Plan{
		RunID:     "preflight",
		OutputDir: outputDir,
		Suites:    []SuitePlan{{Name: "missing"}},
	}

	if err := Run(context.Background(), plan, NewRegistry(), RunOptions{}); err == nil {
		t.Fatal("Run should fail for unknown enabled suite")
	}
	for path, want := range sentinels {
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read sentinel %s after failed preflight: %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("sentinel %s = %q, want %q", path, got, want)
		}
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

type failingSuite struct {
	name string
}

func (s failingSuite) Name() string { return s.name }

func (s failingSuite) Run(context.Context, Env, json.RawMessage) error {
	return errors.New("suite failed")
}

func boolPtr(v bool) *bool {
	return &v
}

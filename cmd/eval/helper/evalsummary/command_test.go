package evalsummary

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestCommandRequiresInput(t *testing.T) {
	cmd := NewCommand()
	cmd.SetArgs(nil)

	if err := cmd.Execute(); err == nil {
		t.Fatal("Execute should require --input")
	}
}

func TestCommandWritesKnownSuiteFilenames(t *testing.T) {
	tmp := t.TempDir()
	runDir := filepath.Join(tmp, "run")
	rawDir := filepath.Join(runDir, "raw")
	outDir := filepath.Join(tmp, "summary")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		t.Fatalf("mkdir raw: %v", err)
	}

	wantFiles := map[string]string{
		"write_trace":      "figure_write_trace.csv",
		"read_query":       "figure_read_query.csv",
		"cas_model":        "figure_cas_model.csv",
		"proof_overhead":   "figure_proof.csv",
		"storage_overhead": "figure_storage.csv",
	}
	for suite := range wantFiles {
		writeRawEnvelope(t, rawDir, suite, `{"system":"maltflat","value":1}`)
	}

	cmd := NewCommand()
	cmd.SetArgs([]string{"--input", runDir, "--out", outDir})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	for _, name := range wantFiles {
		if _, err := os.Stat(filepath.Join(outDir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
}

func writeRawEnvelope(t *testing.T, rawDir, suite, record string) {
	t.Helper()
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(record)); err != nil {
		t.Fatalf("compact record: %v", err)
	}
	line := fmt.Sprintf(
		`{"schema_version":"malt.eval.v1","run_id":"run-1","suite":%q,"emitted_at":"2026-05-16T00:00:00Z","record":%s}`+"\n",
		suite,
		compact.String(),
	)
	path := filepath.Join(rawDir, suite+".jsonl")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open raw envelope: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatalf("write raw envelope: %v", err)
	}
}

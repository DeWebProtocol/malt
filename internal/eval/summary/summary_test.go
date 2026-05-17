package summary

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSummarizeFlattensNestedScalarFields(t *testing.T) {
	tmp := t.TempDir()
	runDir := filepath.Join(tmp, "run")
	rawDir := filepath.Join(runDir, "raw")
	outDir := filepath.Join(tmp, "summary")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		t.Fatalf("mkdir raw: %v", err)
	}
	writeEnvelope(t, rawDir, "write_trace", `{
		"system": "maltflat",
		"iteration": 7,
		"accounting": {"total": {"new_persisted_bytes": 1234}},
		"ignored": {"samples": [1, 2, 3]}
	}`)

	if err := Summarize(runDir, outDir); err != nil {
		t.Fatalf("Summarize: %v", err)
	}

	rows := readCSV(t, filepath.Join(outDir, "figure_write_trace.csv"))
	wantHeader := []string{
		"schema_version",
		"run_id",
		"suite",
		"emitted_at",
		"accounting.total.new_persisted_bytes",
		"iteration",
		"system",
	}
	if !reflect.DeepEqual(rows[0], wantHeader) {
		t.Fatalf("header = %#v, want %#v", rows[0], wantHeader)
	}
	wantRow := []string{
		"malt.eval.v1",
		"run-1",
		"write_trace",
		"2026-05-16T00:00:00Z",
		"1234",
		"7",
		"maltflat",
	}
	if !reflect.DeepEqual(rows[1], wantRow) {
		t.Fatalf("row = %#v, want %#v", rows[1], wantRow)
	}
}

func TestSummarizeUsesDeterministicHeader(t *testing.T) {
	tmp := t.TempDir()
	runDir := filepath.Join(tmp, "run")
	rawDir := filepath.Join(runDir, "raw")
	outDir := filepath.Join(tmp, "summary")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		t.Fatalf("mkdir raw: %v", err)
	}
	writeEnvelope(t, rawDir, "read_query", `{"z":1,"a":2,"nested":{"b":3}}`)
	writeEnvelope(t, rawDir, "read_query", `{"m":4,"nested":{"a":5}}`)

	if err := Summarize(runDir, outDir); err != nil {
		t.Fatalf("Summarize: %v", err)
	}

	rows := readCSV(t, filepath.Join(outDir, "figure_read_query.csv"))
	wantHeader := []string{
		"schema_version",
		"run_id",
		"suite",
		"emitted_at",
		"a",
		"m",
		"nested.a",
		"nested.b",
		"z",
	}
	if !reflect.DeepEqual(rows[0], wantHeader) {
		t.Fatalf("header = %#v, want %#v", rows[0], wantHeader)
	}
	wantFirstRow := []string{"malt.eval.v1", "run-1", "read_query", "2026-05-16T00:00:00Z", "2", "", "", "3", "1"}
	if !reflect.DeepEqual(rows[1], wantFirstRow) {
		t.Fatalf("first row = %#v, want %#v", rows[1], wantFirstRow)
	}
}

func TestSummarizeRefreshesGeneratedFigureCSVs(t *testing.T) {
	tmp := t.TempDir()
	runDir := filepath.Join(tmp, "run")
	rawDir := filepath.Join(runDir, "raw")
	outDir := filepath.Join(runDir, "summary")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		t.Fatalf("mkdir raw: %v", err)
	}
	writeEnvelope(t, rawDir, "write_trace", `{"iteration":1}`)
	if err := Summarize(runDir, outDir); err != nil {
		t.Fatalf("first Summarize: %v", err)
	}
	stalePath := filepath.Join(outDir, "figure_write_trace.csv")
	if _, err := os.Stat(stalePath); err != nil {
		t.Fatalf("initial figure missing: %v", err)
	}
	notesPath := filepath.Join(outDir, "notes.txt")
	if err := os.WriteFile(notesPath, []byte("keep\n"), 0o644); err != nil {
		t.Fatalf("write notes: %v", err)
	}
	if err := os.Remove(filepath.Join(rawDir, "write_trace.jsonl")); err != nil {
		t.Fatalf("remove write trace raw: %v", err)
	}
	writeEnvelope(t, rawDir, "read_query", `{"query":"/a"}`)

	if err := Summarize(runDir, outDir); err != nil {
		t.Fatalf("second Summarize: %v", err)
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("stale write trace figure still present or stat failed unexpectedly: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "figure_read_query.csv")); err != nil {
		t.Fatalf("current read query figure missing: %v", err)
	}
	notes, err := os.ReadFile(notesPath)
	if err != nil {
		t.Fatalf("read notes: %v", err)
	}
	if string(notes) != "keep\n" {
		t.Fatalf("notes = %q, want keep", notes)
	}
}

func TestSummarizeRejectsUnsafeEnvelopeSuiteName(t *testing.T) {
	tmp := t.TempDir()
	runDir := filepath.Join(tmp, "run")
	rawDir := filepath.Join(runDir, "raw")
	outDir := filepath.Join(runDir, "summary")
	if err := os.MkdirAll(rawDir, 0o755); err != nil {
		t.Fatalf("mkdir raw: %v", err)
	}
	line := `{"schema_version":"malt.eval.v1","run_id":"run-1","suite":"x/../../escaped","emitted_at":"2026-05-16T00:00:00Z","record":{"value":1}}` + "\n"
	if err := os.WriteFile(filepath.Join(rawDir, "safe.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatalf("write malicious raw envelope: %v", err)
	}

	if err := Summarize(runDir, outDir); err == nil {
		t.Fatal("Summarize should reject unsafe suite names from raw envelopes")
	}
	if _, err := os.Stat(filepath.Join(runDir, "escaped.csv")); !os.IsNotExist(err) {
		t.Fatalf("escaped summary file exists or stat failed unexpectedly: %v", err)
	}
}

func writeEnvelope(t *testing.T, rawDir, suite, record string) {
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

func readCSV(t *testing.T, path string) [][]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open csv: %v", err)
	}
	defer f.Close()
	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("read csv: %v", err)
	}
	return rows
}

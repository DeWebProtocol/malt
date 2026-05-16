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

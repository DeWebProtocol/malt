package flatindexcardinality

import (
	"bufio"
	"context"
	"encoding/csv"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"

	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/framework"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/readbench"
)

func TestSuiteNameIsFlatIndexCardinality(t *testing.T) {
	if got := (Suite{}).Name(); got != Name {
		t.Fatalf("Suite.Name() = %q, want %q", got, Name)
	}
}

func TestParseConfigDefaultsUseFlatIndexSystems(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil {
		t.Fatalf("parseConfig(nil) error = %v", err)
	}
	if want := []string{"maltflat", "flathamt"}; !reflect.DeepEqual(cfg.Systems, want) {
		t.Fatalf("Systems = %v, want %v", cfg.Systems, want)
	}
	if cfg.PathDepth != 4 {
		t.Fatalf("PathDepth = %d, want 4", cfg.PathDepth)
	}
	if cfg.PathsPerKeyCount != 5 {
		t.Fatalf("PathsPerKeyCount = %d, want 5", cfg.PathsPerKeyCount)
	}
}

func TestRunWritesFlatIndexCardinalityMatrixAndAggregate(t *testing.T) {
	env := newSuiteTestEnv(t, "run-flat-index-cardinality")
	raw := json.RawMessage(`{
		"systems": ["maltflat", "flathamt"],
		"dataset": "flat-index-unit",
		"key_counts": [2, 4],
		"path_depth": 3,
		"paths_per_key_count": 2,
		"small_bytes": 64,
		"cas_latency_ms": [0, 1],
		"iterations": 1
	}`)

	if err := (Suite{}).Run(context.Background(), env, raw); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	envelopes := readRawEnvelopes(t, env.RawPath(Name))
	if len(envelopes) != 16 {
		t.Fatalf("raw envelope count = %d, want 16", len(envelopes))
	}
	seenSystems := map[readbench.SystemName]bool{}
	seenFileCounts := map[int]bool{}
	for _, envelope := range envelopes {
		if envelope.Suite != Name {
			t.Fatalf("suite = %q, want %q", envelope.Suite, Name)
		}
		var result readbench.Result
		if err := json.Unmarshal(envelope.Record, &result); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		seenSystems[result.System] = true
		seenFileCounts[result.FileCount] = true
		if result.PathDepth != 3 {
			t.Fatalf("path_depth = %d, want 3", result.PathDepth)
		}
		if result.FileCount != 2 && result.FileCount != 4 {
			t.Fatalf("file_count = %d, want 2 or 4", result.FileCount)
		}
		if result.OperationKind != readbench.OperationResolvePath {
			t.Fatalf("operation_kind = %q, want resolve_path", result.OperationKind)
		}
		if result.Workload != readbench.WorkloadDeepPathLookup {
			t.Fatalf("workload = %q, want deep_path_lookup", result.Workload)
		}
		if result.ContentBytes == nil || *result.ContentBytes != 64 {
			t.Fatalf("content_bytes = %v, want 64", result.ContentBytes)
		}
	}
	for _, system := range []readbench.SystemName{readbench.SystemMALTFlat, readbench.SystemFlatHAMT} {
		if !seenSystems[system] {
			t.Fatalf("missing system %q", system)
		}
	}
	for _, fileCount := range []int{2, 4} {
		if !seenFileCounts[fileCount] {
			t.Fatalf("missing file_count %d", fileCount)
		}
	}

	rows := readAggregateRows(t, aggregatePath(env, Name))
	if len(rows) != 8 {
		t.Fatalf("aggregate row count = %d, want 8", len(rows))
	}
	for _, row := range rows {
		if row["samples"] != "2" {
			t.Fatalf("aggregate samples = %q, want 2 in row %+v", row["samples"], row)
		}
		if parseInt64Field(t, row, "median_elapsed_ns") <= 0 {
			t.Fatalf("median_elapsed_ns should be positive in row %+v", row)
		}
		if parseInt64Field(t, row, "p95_elapsed_ns") <= 0 {
			t.Fatalf("p95_elapsed_ns should be positive in row %+v", row)
		}
	}
}

type recordEnvelope struct {
	SchemaVersion string          `json:"schema_version"`
	RunID         string          `json:"run_id"`
	Suite         string          `json:"suite"`
	EmittedAt     string          `json:"emitted_at"`
	Record        json.RawMessage `json:"record"`
}

func newSuiteTestEnv(t *testing.T, runID string) framework.Env {
	t.Helper()
	tmp := t.TempDir()
	resultDir := filepath.Join(tmp, "result")
	if err := os.MkdirAll(filepath.Join(resultDir, "raw"), 0o755); err != nil {
		t.Fatalf("create raw dir: %v", err)
	}
	return framework.Env{
		RunID:     runID,
		ResultDir: resultDir,
	}
}

func readRawEnvelopes(t *testing.T, path string) []recordEnvelope {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open raw output: %v", err)
	}
	defer f.Close()

	var out []recordEnvelope
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var envelope recordEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			t.Fatalf("decode envelope: %v", err)
		}
		out = append(out, envelope)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan raw output: %v", err)
	}
	return out
}

func readAggregateRows(t *testing.T, path string) []map[string]string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open aggregate output: %v", err)
	}
	defer f.Close()

	records, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("read aggregate csv: %v", err)
	}
	if len(records) < 1 {
		t.Fatal("aggregate csv missing header")
	}
	header := records[0]
	out := make([]map[string]string, 0, len(records)-1)
	for _, record := range records[1:] {
		row := make(map[string]string, len(header))
		for i, column := range header {
			row[column] = record[i]
		}
		out = append(out, row)
	}
	return out
}

func parseInt64Field(t *testing.T, row map[string]string, field string) int64 {
	t.Helper()
	value, err := strconv.ParseInt(row[field], 10, 64)
	if err != nil {
		t.Fatalf("parse %s=%q: %v", field, row[field], err)
	}
	return value
}

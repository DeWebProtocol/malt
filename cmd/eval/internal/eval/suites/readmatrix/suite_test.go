package readmatrix

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
	"github.com/dewebprotocol/malt/runtime/metrics"
)

func TestSuiteNameIsReadMatrix(t *testing.T) {
	if got := (Suite{}).Name(); got != Name {
		t.Fatalf("Suite.Name() = %q, want %q", got, Name)
	}
}

func TestParseConfigDefaultsUsePaperCASLatencyBuckets(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil {
		t.Fatalf("parseConfig(nil) error = %v", err)
	}
	wantDepths := []int{1, 2, 3, 4, 5, 6}
	if !reflect.DeepEqual(cfg.Depths, wantDepths) {
		t.Fatalf("Depths = %v, want %v", cfg.Depths, wantDepths)
	}
	wantLatencies := []int{0, 25, 50, 100, 200}
	if !reflect.DeepEqual(cfg.CASLatencyMS, wantLatencies) {
		t.Fatalf("CASLatencyMS = %v, want %v", cfg.CASLatencyMS, wantLatencies)
	}
	if cfg.PathsPerDepth != 5 {
		t.Fatalf("PathsPerDepth = %d, want 5", cfg.PathsPerDepth)
	}
}

func TestRunWritesResolveOnlyDepthLatencyMatrix(t *testing.T) {
	env := newSuiteTestEnv(t, "run-read-matrix")
	raw := json.RawMessage(`{
		"systems": ["maltflat", "merkledag", "flathamt"],
		"dataset": "matrix-unit",
		"depths": [2, 4],
		"small_bytes": 128,
		"cas_latency_ms": [0, 1],
		"iterations": 1,
		"paths_per_depth": 2
	}`)

	if err := (Suite{}).Run(context.Background(), env, raw); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	envelopes := readRawEnvelopes(t, env.RawPath(Name))
	if len(envelopes) != 24 {
		t.Fatalf("envelope count = %d, want 24", len(envelopes))
	}

	seenSystems := map[readbench.SystemName]bool{}
	seenDepths := map[int]bool{}
	seenLatencies := map[int]bool{}
	for _, envelope := range envelopes {
		if envelope.Suite != Name {
			t.Fatalf("suite = %q, want %q", envelope.Suite, Name)
		}
		var result readbench.Result
		if err := json.Unmarshal(envelope.Record, &result); err != nil {
			t.Fatalf("decode result: %v", err)
		}
		seenSystems[result.System] = true
		seenDepths[result.PathDepth] = true
		seenLatencies[result.CASLatencyMS] = true

		if result.OperationKind != readbench.OperationResolvePath {
			t.Fatalf("operation_kind = %q, want %q", result.OperationKind, readbench.OperationResolvePath)
		}
		if result.Workload != readbench.WorkloadDeepPathLookup {
			t.Fatalf("workload = %q, want %q", result.Workload, readbench.WorkloadDeepPathLookup)
		}
		if result.RangeHeader != "" {
			t.Fatalf("range_header = %q, want empty", result.RangeHeader)
		}
		if result.DatasetName != "matrix-unit-depth2-4" {
			t.Fatalf("dataset = %q", result.DatasetName)
		}
		if result.FileCount != 4 {
			t.Fatalf("file_count = %d, want one lookup file per depth sample", result.FileCount)
		}
		if result.DirectoryCount == 0 || result.PathCount == 0 {
			t.Fatalf("directory/path counts should be populated: dirs=%d paths=%d", result.DirectoryCount, result.PathCount)
		}
		if result.SmallFileBytes != 128 || result.LargeFileBytes != 0 {
			t.Fatalf("file bytes = small %d large %d, want small 128 and no large/list payload", result.SmallFileBytes, result.LargeFileBytes)
		}
		switch result.System {
		case readbench.SystemMALTFlat:
			if result.ContentBytes == nil || *result.ContentBytes != 128 {
				t.Fatalf("malt content_bytes = %v, want target blob size 128", result.ContentBytes)
			}
			if result.CAS.GetCount != 1 {
				t.Fatalf("malt CAS get_count = %d, want one target blob fetch", result.CAS.GetCount)
			}
		case readbench.SystemMerkleDAG:
			if result.ContentBytes != nil {
				t.Fatalf("merkledag content_bytes = %v, want nil for path-resolution record", result.ContentBytes)
			}
			if result.CAS.GetCount < uint64(result.PathDepth+1) {
				t.Fatalf("merkledag CAS get_count = %d, want at least depth+1 for root/path edges", result.CAS.GetCount)
			}
		case readbench.SystemFlatHAMT:
			if result.ContentBytes == nil || *result.ContentBytes != 128 {
				t.Fatalf("flathamt content_bytes = %v, want target blob size 128", result.ContentBytes)
			}
		}
	}
	for _, system := range []readbench.SystemName{readbench.SystemMALTFlat, readbench.SystemMerkleDAG, readbench.SystemFlatHAMT} {
		if !seenSystems[system] {
			t.Fatalf("missing system %q", system)
		}
	}
	for _, depth := range []int{2, 4} {
		if !seenDepths[depth] {
			t.Fatalf("missing depth %d", depth)
		}
	}
	for _, latencyMS := range []int{0, 1} {
		if !seenLatencies[latencyMS] {
			t.Fatalf("missing cas latency %dms", latencyMS)
		}
	}

	rows := readAggregateRows(t, aggregatePath(env, Name))
	if len(rows) != 12 {
		t.Fatalf("aggregate row count = %d, want 12", len(rows))
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

func TestAggregateResultsReportsMedianAndP95(t *testing.T) {
	results := []readbench.Result{
		{System: readbench.SystemMALTFlat, PathDepth: 2, CASLatencyMS: 50, ElapsedNS: 100, CAS: metricsCAS(1)},
		{System: readbench.SystemMALTFlat, PathDepth: 2, CASLatencyMS: 50, ElapsedNS: 500, CAS: metricsCAS(5)},
		{System: readbench.SystemMALTFlat, PathDepth: 2, CASLatencyMS: 50, ElapsedNS: 300, CAS: metricsCAS(3)},
		{System: readbench.SystemMALTFlat, PathDepth: 2, CASLatencyMS: 50, ElapsedNS: 200, CAS: metricsCAS(2)},
		{System: readbench.SystemMALTFlat, PathDepth: 2, CASLatencyMS: 50, ElapsedNS: 400, CAS: metricsCAS(4)},
	}

	rows := aggregateResults(results)
	if len(rows) != 1 {
		t.Fatalf("aggregate row count = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Samples != 5 {
		t.Fatalf("samples = %d, want 5", row.Samples)
	}
	if row.MedianElapsedNS != 300 || row.P95ElapsedNS != 500 {
		t.Fatalf("elapsed aggregate = median %d p95 %d, want 300/500", row.MedianElapsedNS, row.P95ElapsedNS)
	}
	if row.MedianCASGetCount != 3 || row.P95CASGetCount != 5 {
		t.Fatalf("cas aggregate = median %d p95 %d, want 3/5", row.MedianCASGetCount, row.P95CASGetCount)
	}
}

func metricsCAS(getCount uint64) metrics.CASStats {
	return metrics.CASStats{GetCount: getCount}
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

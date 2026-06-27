package readmatrix

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/framework"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/readbench"
)

func TestSuiteNameIsReadMatrix(t *testing.T) {
	if got := (Suite{}).Name(); got != Name {
		t.Fatalf("Suite.Name() = %q, want %q", got, Name)
	}
}

func TestRunWritesMatrixRecordsWithDatasetMetadata(t *testing.T) {
	env := newSuiteTestEnv(t, "run-read-matrix")
	raw := json.RawMessage(`{
		"systems": ["maltflat", "merkledag"],
		"dataset": "matrix-unit",
		"file_counts": [4],
		"depths": [2, 4],
		"small_bytes": 128,
		"large_bytes": 262145,
		"range": "bytes=7-18",
		"iterations": 1
	}`)

	if err := (Suite{}).Run(context.Background(), env, raw); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	envelopes := readRawEnvelopes(t, env.RawPath(Name))
	if len(envelopes) != 12 {
		t.Fatalf("envelope count = %d, want 12", len(envelopes))
	}

	seenSystems := map[readbench.SystemName]bool{}
	seenDepths := map[int]bool{}
	seenWorkloads := map[readbench.WorkloadKind]bool{}
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
		seenWorkloads[result.Workload] = true

		if result.DatasetName != "matrix-unit-files4-depth2-4" {
			t.Fatalf("dataset = %q", result.DatasetName)
		}
		if result.FileCount != 4 {
			t.Fatalf("file_count = %d, want 4", result.FileCount)
		}
		if result.DirectoryCount == 0 || result.PathCount == 0 {
			t.Fatalf("directory/path counts should be populated: dirs=%d paths=%d", result.DirectoryCount, result.PathCount)
		}
		if result.LogicalPayloadBytes <= 262145 {
			t.Fatalf("logical_payload_bytes = %d, want dataset-level total > large file", result.LogicalPayloadBytes)
		}
		if result.SmallFileBytes != 128 || result.LargeFileBytes != 262145 {
			t.Fatalf("file bytes = small %d large %d", result.SmallFileBytes, result.LargeFileBytes)
		}
		if result.OperationKind == readbench.OperationContentRange {
			if result.RangeHeader != "bytes=7-18" {
				t.Fatalf("range_header = %q, want bytes=7-18", result.RangeHeader)
			}
			if result.ContentBytes == nil || *result.ContentBytes != 12 {
				t.Fatalf("content_bytes = %v, want 12", result.ContentBytes)
			}
		}
	}
	for _, system := range []readbench.SystemName{readbench.SystemMALTFlat, readbench.SystemMerkleDAG} {
		if !seenSystems[system] {
			t.Fatalf("missing system %q", system)
		}
	}
	for _, depth := range []int{2, 4} {
		if !seenDepths[depth] {
			t.Fatalf("missing depth %d", depth)
		}
	}
	for _, workload := range []readbench.WorkloadKind{
		readbench.WorkloadDeepPathLookup,
		readbench.WorkloadSmallFileRead,
		readbench.WorkloadLargeFileRangeRead,
	} {
		if !seenWorkloads[workload] {
			t.Fatalf("missing workload %q", workload)
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

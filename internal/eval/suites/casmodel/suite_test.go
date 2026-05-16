package casmodel

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dewebprotocol/malt/internal/eval/framework"
)

func TestSuiteEmitsDeterministicCASModelRecords(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "raw"), 0o755); err != nil {
		t.Fatalf("mkdir raw: %v", err)
	}

	cfg := json.RawMessage(`{
		"get_latency_ms": 0,
		"put_latency_ms": 0,
		"has_latency_ms": 0,
		"chain_lengths": [2],
		"batch_sizes": [3],
		"iterations": 1
	}`)
	suite := Suite{}
	if suite.Name() != "cas_model" {
		t.Fatalf("Name() = %q, want cas_model", suite.Name())
	}

	if err := suite.Run(context.Background(), framework.Env{
		RunID:     "run-cas",
		OutputDir: tmp,
	}, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	records := readCASModelRecords(t, filepath.Join(tmp, "raw", "cas_model.jsonl"))
	if len(records) != 6 {
		t.Fatalf("record count = %d, want 6", len(records))
	}

	var putChain, hasBatch bool
	for _, record := range records {
		if record.ConfiguredJitterMS != 0 {
			t.Fatalf("configured_jitter_ms = %d, want default 0", record.ConfiguredJitterMS)
		}
		if record.ElapsedNS < 0 {
			t.Fatalf("elapsed_ns = %d, want non-negative", record.ElapsedNS)
		}
		if record.Operation == "put" && record.DependencyShape == "chain" {
			putChain = true
			if record.Size != 2 || record.Iteration != 0 {
				t.Fatalf("put chain size/iteration = %d/%d, want 2/0", record.Size, record.Iteration)
			}
			if record.CAS.PutCount != 2 || record.CAS.BytesPut == 0 {
				t.Fatalf("put chain CAS stats = %+v, want two puts with bytes", record.CAS)
			}
		}
		if record.Operation == "has" && record.DependencyShape == "batch" {
			hasBatch = true
			if record.Size != 3 {
				t.Fatalf("has batch size = %d, want 3", record.Size)
			}
			if record.CAS.HasCount != 3 {
				t.Fatalf("has batch CAS stats = %+v, want three has calls", record.CAS)
			}
		}
	}
	if !putChain || !hasBatch {
		t.Fatalf("missing put chain=%v or has batch=%v records", putChain, hasBatch)
	}
}

func readCASModelRecords(t *testing.T, path string) []Result {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open records: %v", err)
	}
	defer f.Close()

	var out []Result
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var envelope framework.RecordEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			t.Fatalf("unmarshal envelope: %v", err)
		}
		if envelope.Suite != "cas_model" {
			t.Fatalf("suite = %q, want cas_model", envelope.Suite)
		}
		var record Result
		if err := json.Unmarshal(envelope.Record, &record); err != nil {
			t.Fatalf("unmarshal record: %v", err)
		}
		out = append(out, record)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan records: %v", err)
	}
	return out
}

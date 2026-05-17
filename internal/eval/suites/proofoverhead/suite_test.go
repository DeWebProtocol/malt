package proofoverhead

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dewebprotocol/malt/internal/eval/framework"
)

func TestSuiteMeasuresMapAndListProofOverhead(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "raw"), 0o755); err != nil {
		t.Fatalf("mkdir raw: %v", err)
	}

	cfg := json.RawMessage(`{
		"structures": ["map", "list"],
		"sizes": [2],
		"iterations": 1,
		"commitment": ["ipa"]
	}`)
	suite := Suite{}
	if suite.Name() != "proof_overhead" {
		t.Fatalf("Name() = %q, want proof_overhead", suite.Name())
	}

	if err := suite.Run(context.Background(), framework.Env{
		RunID:     "run-proof",
		OutputDir: tmp,
	}, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	records := readProofRecords(t, filepath.Join(tmp, "raw", "proof_overhead.jsonl"))
	if len(records) != 2 {
		t.Fatalf("record count = %d, want 2", len(records))
	}
	for _, record := range records {
		if record.Method != "measured" {
			t.Fatalf("method = %q, want measured for %+v", record.Method, record)
		}
		if record.Commitment != "ipa" || record.Size != 2 || record.Iteration != 0 {
			t.Fatalf("record dimensions = %+v, want ipa size 2 iteration 0", record)
		}
		switch record.Structure {
		case "map":
			if record.MapBackend != "radix" {
				t.Fatalf("map backend = %q, want radix", record.MapBackend)
			}
		case "list":
			if record.ListBackend != "tree" {
				t.Fatalf("list backend = %q, want tree", record.ListBackend)
			}
		default:
			t.Fatalf("unexpected structure = %q", record.Structure)
		}
		if record.ArcTableMode != "versioned" {
			t.Fatalf("arctable mode = %q, want versioned", record.ArcTableMode)
		}
		if record.CommitElapsedNS < 0 || record.ProveElapsedNS < 0 || record.VerifyElapsedNS < 0 {
			t.Fatalf("elapsed fields must be non-negative: %+v", record)
		}
		if record.ProofBytes == 0 || record.EvidenceCount == 0 {
			t.Fatalf("proof accounting = proof_bytes %d evidence_count %d, want non-zero", record.ProofBytes, record.EvidenceCount)
		}
		if record.Verified == nil || !*record.Verified {
			t.Fatalf("verified = %v, want true", record.Verified)
		}
	}
}

func TestSuiteUsesRuntimeRadixMapForLargeMapProofs(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "raw"), 0o755); err != nil {
		t.Fatalf("mkdir raw: %v", err)
	}

	cfg := json.RawMessage(`{
		"structures": ["map"],
		"sizes": [257],
		"iterations": 1,
		"commitment": ["ipa"]
	}`)
	if err := (Suite{}).Run(context.Background(), framework.Env{
		RunID:     "run-proof-radix-map",
		OutputDir: tmp,
	}, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	records := readProofRecords(t, filepath.Join(tmp, "raw", "proof_overhead.jsonl"))
	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1", len(records))
	}
	record := records[0]
	if record.Method != "measured" {
		t.Fatalf("method = %q, want measured for runtime radix map: %+v", record.Method, record)
	}
	if record.MapBackend != "radix" || record.ArcTableMode != "versioned" {
		t.Fatalf("runtime backend labels = %+v, want radix/versioned", record)
	}
}

func TestSuiteReportsUnsupportedProofMethodWithoutFabricatingMetrics(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "raw"), 0o755); err != nil {
		t.Fatalf("mkdir raw: %v", err)
	}

	cfg := json.RawMessage(`{
		"structures": ["map"],
		"sizes": [1],
		"iterations": 1,
		"commitment": ["unknown"]
	}`)
	if err := (Suite{}).Run(context.Background(), framework.Env{
		RunID:     "run-proof-unsupported",
		OutputDir: tmp,
	}, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	records := readProofRecords(t, filepath.Join(tmp, "raw", "proof_overhead.jsonl"))
	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1", len(records))
	}
	record := records[0]
	if record.Method != "unsupported" {
		t.Fatalf("method = %q, want unsupported", record.Method)
	}
	if record.ProofBytes != 0 || record.EvidenceCount != 0 || record.Verified != nil {
		t.Fatalf("unsupported record fabricated metrics: %+v", record)
	}
	if record.Error == "" {
		t.Fatalf("unsupported record should explain why: %+v", record)
	}
}

func TestSuiteAcceptsSingleCommitmentConfigValue(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "raw"), 0o755); err != nil {
		t.Fatalf("mkdir raw: %v", err)
	}

	cfg := json.RawMessage(`{
		"structures": ["map"],
		"sizes": [1],
		"iterations": 1,
		"commitment": "ipa"
	}`)
	if err := (Suite{}).Run(context.Background(), framework.Env{
		RunID:     "run-proof-single-commitment",
		OutputDir: tmp,
	}, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	records := readProofRecords(t, filepath.Join(tmp, "raw", "proof_overhead.jsonl"))
	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1", len(records))
	}
	if records[0].Commitment != "ipa" || records[0].Method != "measured" {
		t.Fatalf("record = %+v, want measured ipa record", records[0])
	}
}

func readProofRecords(t *testing.T, path string) []Result {
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
		if envelope.Suite != "proof_overhead" {
			t.Fatalf("suite = %q, want proof_overhead", envelope.Suite)
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

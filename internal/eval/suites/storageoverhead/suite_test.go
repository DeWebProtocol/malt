package storageoverhead

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
	"github.com/dewebprotocol/malt/internal/eval/framework"
)

func TestSuiteRecordsPersistedAndLogicalStorageOverhead(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "raw"), 0o755); err != nil {
		t.Fatalf("mkdir raw: %v", err)
	}

	cfg := json.RawMessage(`{
		"structures": ["map", "list"],
		"sizes": [2],
		"payload_bytes": 8
	}`)
	suite := Suite{}
	if suite.Name() != "storage_overhead" {
		t.Fatalf("Name() = %q, want storage_overhead", suite.Name())
	}

	if err := suite.Run(context.Background(), framework.Env{
		RunID:     "run-storage",
		OutputDir: tmp,
	}, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	records := readStorageRecords(t, filepath.Join(tmp, "raw", "storage_overhead.jsonl"))
	if len(records) != 2 {
		t.Fatalf("record count = %d, want 2", len(records))
	}
	for _, record := range records {
		if record.Method != "measured" {
			t.Fatalf("method = %q, want measured", record.Method)
		}
		if record.Size != 2 || record.PayloadBytes != 8 {
			t.Fatalf("record dimensions = %+v, want size 2 payload 8", record)
		}
		if record.LogicalPayloadBytes != 16 {
			t.Fatalf("logical_payload_bytes = %d, want 16", record.LogicalPayloadBytes)
		}
		if record.LogicalProofBytes == 0 {
			t.Fatalf("logical_proof_bytes = 0, want measured proof bytes")
		}
		if record.PersistedBytes == 0 || record.Accounting.Total.NewPersistedBytes != record.PersistedBytes {
			t.Fatalf("persisted/accounting mismatch: %+v", record)
		}
		if record.Accounting.Categories[evalstore.CategoryCASPayload].NewObjectCount != 2 {
			t.Fatalf("cas payload category = %+v, want two payload objects", record.Accounting.Categories[evalstore.CategoryCASPayload])
		}
		if record.Accounting.Categories[evalstore.CategoryArcTable].NewPersistedBytes == 0 {
			t.Fatalf("arctable category = %+v, want persisted ArcTable bytes", record.Accounting.Categories[evalstore.CategoryArcTable])
		}
		if record.Accounting.Categories[evalstore.CategoryRootHead].NewPersistedBytes == 0 {
			t.Fatalf("root_head category = %+v, want root publication bytes", record.Accounting.Categories[evalstore.CategoryRootHead])
		}
		if _, ok := record.Accounting.Categories[evalstore.CategoryCASMetadata]; !ok {
			t.Fatalf("accounting categories should explicitly include cas_metadata")
		}
	}
}

func TestSuiteReportsUnsupportedStorageStructure(t *testing.T) {
	tmp := t.TempDir()
	if err := os.MkdirAll(filepath.Join(tmp, "raw"), 0o755); err != nil {
		t.Fatalf("mkdir raw: %v", err)
	}

	cfg := json.RawMessage(`{
		"structures": ["unknown"],
		"sizes": [1],
		"payload_bytes": 4
	}`)
	if err := (Suite{}).Run(context.Background(), framework.Env{
		RunID:     "run-storage-unsupported",
		OutputDir: tmp,
	}, cfg); err != nil {
		t.Fatalf("Run: %v", err)
	}

	records := readStorageRecords(t, filepath.Join(tmp, "raw", "storage_overhead.jsonl"))
	if len(records) != 1 {
		t.Fatalf("record count = %d, want 1", len(records))
	}
	record := records[0]
	if record.Method != "unsupported" {
		t.Fatalf("method = %q, want unsupported", record.Method)
	}
	if record.PersistedBytes != 0 || record.LogicalPayloadBytes != 0 || record.LogicalProofBytes != 0 {
		t.Fatalf("unsupported record fabricated storage metrics: %+v", record)
	}
	if _, ok := record.Accounting.Categories[evalstore.CategoryCASPayload]; !ok {
		t.Fatalf("unsupported record should include zero accounting categories: %+v", record.Accounting.Categories)
	}
	if record.Error == "" {
		t.Fatalf("unsupported record should explain why: %+v", record)
	}
}

func readStorageRecords(t *testing.T, path string) []Result {
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
		if envelope.Suite != "storage_overhead" {
			t.Fatalf("suite = %q, want storage_overhead", envelope.Suite)
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

package framework

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Env gives suites access to their run metadata and output directories.
type Env struct {
	RunID     string
	OutputDir string
	clock     func() time.Time
}

// RawPath returns the JSONL path for a suite's raw result stream.
func (e Env) RawPath(suite string) string {
	return filepath.Join(e.OutputDir, "raw", suite+".jsonl")
}

// WriteRecord appends one raw result record using the common envelope.
func (e Env) WriteRecord(suite string, record any) error {
	if suite == "" {
		return fmt.Errorf("suite is empty")
	}
	if e.clock == nil {
		e.clock = time.Now
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal record: %w", err)
	}
	envelope := RecordEnvelope{
		SchemaVersion: SchemaVersion,
		RunID:         e.RunID,
		Suite:         suite,
		EmittedAt:     e.clock().UTC().Format(time.RFC3339Nano),
		Record:        payload,
	}
	f, err := os.OpenFile(e.RawPath(suite), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	return enc.Encode(envelope)
}

// RecordEnvelope wraps suite-specific records with run metadata.
type RecordEnvelope struct {
	SchemaVersion string          `json:"schema_version"`
	RunID         string          `json:"run_id"`
	Suite         string          `json:"suite"`
	EmittedAt     string          `json:"emitted_at"`
	Record        json.RawMessage `json:"record"`
}

// Package summary converts framework raw result envelopes into figure CSVs.
package summary

import (
	"bufio"
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

var metadataColumns = []string{"schema_version", "run_id", "suite", "emitted_at"}

var figureNames = map[string]string{
	"write_trace":      "figure_write_trace.csv",
	"read_query":       "figure_read_query.csv",
	"cas_model":        "figure_cas_model.csv",
	"proof_overhead":   "figure_proof.csv",
	"storage_overhead": "figure_storage.csv",
}

type suiteTable struct {
	rows       []map[string]string
	recordKeys map[string]struct{}
}

type recordEnvelope struct {
	SchemaVersion string          `json:"schema_version"`
	RunID         string          `json:"run_id"`
	Suite         string          `json:"suite"`
	EmittedAt     string          `json:"emitted_at"`
	Record        json.RawMessage `json:"record"`
}

// Summarize reads framework raw envelopes from inputDir/raw and writes figure
// CSVs into outDir.
func Summarize(inputDir, outDir string) error {
	if strings.TrimSpace(inputDir) == "" {
		return fmt.Errorf("input directory is required")
	}
	if strings.TrimSpace(outDir) == "" {
		outDir = filepath.Join(inputDir, "summary")
	}

	rawDir := filepath.Join(inputDir, "raw")
	entries, err := os.ReadDir(rawDir)
	if err != nil {
		return fmt.Errorf("read raw directory: %w", err)
	}

	tables := make(map[string]*suiteTable)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		path := filepath.Join(rawDir, entry.Name())
		fileSuite := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		if err := readRawFile(path, fileSuite, tables); err != nil {
			return err
		}
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("create summary directory: %w", err)
	}

	suites := make([]string, 0, len(tables))
	for suite := range tables {
		suites = append(suites, suite)
	}
	sort.Strings(suites)
	for _, suite := range suites {
		if err := writeSuiteCSV(outDir, suite, tables[suite]); err != nil {
			return err
		}
	}
	return nil
}

func readRawFile(path, fileSuite string, tables map[string]*suiteTable) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open raw file %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		envelope, err := decodeEnvelope([]byte(line))
		if err != nil {
			return fmt.Errorf("decode %s:%d: %w", path, lineNumber, err)
		}
		suite := strings.TrimSpace(envelope.Suite)
		if suite == "" {
			suite = fileSuite
		}
		if suite == "" {
			return fmt.Errorf("decode %s:%d: suite is empty", path, lineNumber)
		}
		table := tables[suite]
		if table == nil {
			table = &suiteTable{recordKeys: map[string]struct{}{}}
			tables[suite] = table
		}
		row, err := envelopeRow(envelope, table.recordKeys)
		if err != nil {
			return fmt.Errorf("decode record %s:%d: %w", path, lineNumber, err)
		}
		row["suite"] = suite
		table.rows = append(table.rows, row)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan raw file %s: %w", path, err)
	}
	return nil
}

func decodeEnvelope(data []byte) (recordEnvelope, error) {
	var envelope recordEnvelope
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(&envelope); err != nil {
		return recordEnvelope{}, err
	}
	if dec.More() {
		return recordEnvelope{}, fmt.Errorf("unexpected trailing JSON")
	}
	return envelope, nil
}

func envelopeRow(envelope recordEnvelope, recordKeys map[string]struct{}) (map[string]string, error) {
	row := map[string]string{
		"schema_version": envelope.SchemaVersion,
		"run_id":         envelope.RunID,
		"suite":          envelope.Suite,
		"emitted_at":     envelope.EmittedAt,
	}
	if len(bytes.TrimSpace(envelope.Record)) == 0 {
		return row, nil
	}

	var record map[string]any
	dec := json.NewDecoder(bytes.NewReader(envelope.Record))
	dec.UseNumber()
	if err := dec.Decode(&record); err != nil {
		return nil, err
	}
	for key, value := range record {
		flattenValue(key, value, row, recordKeys)
	}
	return row, nil
}

func flattenValue(key string, value any, row map[string]string, recordKeys map[string]struct{}) {
	if key == "" {
		return
	}
	switch typed := value.(type) {
	case map[string]any:
		for childKey, childValue := range typed {
			flattenValue(key+"."+childKey, childValue, row, recordKeys)
		}
	case string:
		row[key] = typed
		recordKeys[key] = struct{}{}
	case json.Number:
		row[key] = typed.String()
		recordKeys[key] = struct{}{}
	case bool:
		if typed {
			row[key] = "true"
		} else {
			row[key] = "false"
		}
		recordKeys[key] = struct{}{}
	case nil:
		row[key] = ""
		recordKeys[key] = struct{}{}
	}
}

func writeSuiteCSV(outDir, suite string, table *suiteTable) error {
	path := filepath.Join(outDir, figureCSVName(suite))
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer f.Close()

	header := append([]string{}, metadataColumns...)
	recordColumns := make([]string, 0, len(table.recordKeys))
	metadata := make(map[string]struct{}, len(metadataColumns))
	for _, column := range metadataColumns {
		metadata[column] = struct{}{}
	}
	for column := range table.recordKeys {
		if _, ok := metadata[column]; ok {
			continue
		}
		recordColumns = append(recordColumns, column)
	}
	sort.Strings(recordColumns)
	header = append(header, recordColumns...)

	writer := csv.NewWriter(f)
	if err := writer.Write(header); err != nil {
		return fmt.Errorf("write %s header: %w", path, err)
	}
	for _, row := range table.rows {
		values := make([]string, len(header))
		for i, column := range header {
			values[i] = row[column]
		}
		if err := writer.Write(values); err != nil {
			return fmt.Errorf("write %s row: %w", path, err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return fmt.Errorf("flush %s: %w", path, err)
	}
	return nil
}

func figureCSVName(suite string) string {
	if name, ok := figureNames[suite]; ok {
		return name
	}
	return "figure_" + suite + ".csv"
}

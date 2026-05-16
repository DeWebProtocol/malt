package readquery

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/dewebprotocol/malt/internal/eval/framework"
	"github.com/dewebprotocol/malt/internal/eval/readbench"
)

func TestSuiteNameIsReadQuery(t *testing.T) {
	if got := (Suite{}).Name(); got != Name {
		t.Fatalf("Suite.Name() = %q, want %q", got, Name)
	}
	if Name != "read_query" {
		t.Fatalf("Name = %q, want read_query", Name)
	}
}

func TestParseConfigDefaultsMatchReadBenchAndEvalRead(t *testing.T) {
	cfg, err := parseConfig(nil)
	if err != nil {
		t.Fatalf("parseConfig() error = %v", err)
	}

	if !reflect.DeepEqual(cfg.Systems, readbench.DefaultSystems()) {
		t.Fatalf("systems = %q, want %q", cfg.Systems, readbench.DefaultSystems())
	}
	if cfg.Fixture != "" {
		t.Fatalf("fixture = %q, want empty for generated readbench fixture", cfg.Fixture)
	}
	if cfg.Depth != readbench.DefaultDirectoryDepth {
		t.Fatalf("depth = %d, want %d", cfg.Depth, readbench.DefaultDirectoryDepth)
	}
	if cfg.SmallBytes != readbench.DefaultSmallFileBytes {
		t.Fatalf("small bytes = %d, want %d", cfg.SmallBytes, readbench.DefaultSmallFileBytes)
	}
	if cfg.LargeBytes != readbench.DefaultLargeFileBytes {
		t.Fatalf("large bytes = %d, want %d", cfg.LargeBytes, readbench.DefaultLargeFileBytes)
	}
	if cfg.Range != readbench.DefaultRangeHeader {
		t.Fatalf("range = %q, want %q", cfg.Range, readbench.DefaultRangeHeader)
	}
	if cfg.Iterations != readbench.DefaultIterations {
		t.Fatalf("iterations = %d, want %d", cfg.Iterations, readbench.DefaultIterations)
	}
	if cfg.APIBaseURL != "" {
		t.Fatalf("api base url = %q, want empty until maltflat execution needs config default", cfg.APIBaseURL)
	}
	if len(cfg.Arcs) != 0 {
		t.Fatalf("arcs = %#v, want empty", cfg.Arcs)
	}
}

func TestParseConfigSupportsReadQueryFields(t *testing.T) {
	cfg, err := parseConfig(json.RawMessage(`{
		"systems": ["merkledag", "hamt"],
		"fixture": "custom-read-fixture",
		"depth": 0,
		"small_bytes": 128,
		"large_bytes": 262145,
		"range": "bytes=5-9",
		"iterations": 0,
		"api_base_url": "http://127.0.0.1:4317/",
		"arcs": {
			"@payload": "bafyPayload",
			"seed": "bafySeed"
		}
	}`))
	if err != nil {
		t.Fatalf("parseConfig() error = %v", err)
	}

	wantSystems := []readbench.SystemName{readbench.SystemMerkleDAG, readbench.SystemHAMT}
	if !reflect.DeepEqual(cfg.Systems, wantSystems) {
		t.Fatalf("systems = %q, want %q", cfg.Systems, wantSystems)
	}
	if cfg.Fixture != "custom-read-fixture" {
		t.Fatalf("fixture = %q", cfg.Fixture)
	}
	if cfg.Depth != 0 {
		t.Fatalf("depth = %d, want explicit zero", cfg.Depth)
	}
	if cfg.SmallBytes != 128 || cfg.LargeBytes != 262145 {
		t.Fatalf("bytes = small %d large %d", cfg.SmallBytes, cfg.LargeBytes)
	}
	if cfg.Range != "bytes=5-9" || cfg.Iterations != 0 {
		t.Fatalf("range/iterations = %q/%d", cfg.Range, cfg.Iterations)
	}
	if cfg.APIBaseURL != "http://127.0.0.1:4317/" {
		t.Fatalf("api base url = %q", cfg.APIBaseURL)
	}
	if cfg.Arcs["@payload"] != "bafyPayload" || cfg.Arcs["seed"] != "bafySeed" {
		t.Fatalf("arcs = %#v", cfg.Arcs)
	}
}

func TestRunAllowsBaselineOnlyWithoutDaemonArcs(t *testing.T) {
	env := newSuiteTestEnv(t, "run-baseline")
	err := (Suite{}).Run(context.Background(), env, json.RawMessage(`{
		"systems": ["merkledag"],
		"fixture": "baseline-only",
		"api_base_url": "http://127.0.0.1:1"
	}`))
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	envelopes := readRawEnvelopes(t, env.RawPath(Name))
	if len(envelopes) != 2 {
		t.Fatalf("envelope count = %d, want 2", len(envelopes))
	}
	for _, envelope := range envelopes {
		result := decodeEnvelopeResult(t, envelope)
		if result.System != readbench.SystemMerkleDAG {
			t.Fatalf("system = %q, want merkledag", result.System)
		}
		if result.FixtureName != "baseline-only" {
			t.Fatalf("fixture = %q, want baseline-only", result.FixtureName)
		}
	}
}

func TestRunRejectsMALTFlatWithoutPayloadArc(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
	}{
		{
			name: "missing arcs",
			raw:  json.RawMessage(`{"systems":["maltflat"],"fixture":"missing-arcs"}`),
		},
		{
			name: "missing payload",
			raw:  json.RawMessage(`{"systems":["maltflat"],"fixture":"missing-payload","arcs":{"seed":"bafySeed"}}`),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newSuiteTestEnv(t, "run-maltflat-missing-arcs")
			err := (Suite{}).Run(context.Background(), env, tt.raw)
			if err == nil {
				t.Fatal("Run() should reject maltflat config without arcs[@payload]")
			}
			if !bytes.Contains([]byte(err.Error()), []byte(`read_query requires arcs["@payload"] when maltflat is selected`)) {
				t.Fatalf("error = %q", err)
			}
		})
	}
}

func TestFrameworkRunWritesEnvelopedReadQueryRecords(t *testing.T) {
	tmp := t.TempDir()
	reg := framework.NewRegistry()
	if err := reg.Register(Suite{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	plan := framework.Plan{
		RunID:     "run-read-query",
		OutputDir: filepath.Join(tmp, "out"),
		Suites: []framework.SuitePlan{{
			Name: Name,
			Config: json.RawMessage(`{
				"systems": ["hamt"],
				"fixture": "read-query-envelope",
				"range": "bytes=3-6"
			}`),
		}},
	}

	if err := framework.Run(context.Background(), plan, reg, framework.RunOptions{}); err != nil {
		t.Fatalf("framework.Run() error = %v", err)
	}

	envelopes := readRawEnvelopes(t, filepath.Join(plan.OutputDir, "raw", "read_query.jsonl"))
	if len(envelopes) != 2 {
		t.Fatalf("envelope count = %d, want 2", len(envelopes))
	}

	for _, envelope := range envelopes {
		if envelope.SchemaVersion != framework.SchemaVersion {
			t.Fatalf("schema version = %q, want %q", envelope.SchemaVersion, framework.SchemaVersion)
		}
		if envelope.RunID != "run-read-query" {
			t.Fatalf("run id = %q, want run-read-query", envelope.RunID)
		}
		if envelope.Suite != Name {
			t.Fatalf("suite = %q, want %q", envelope.Suite, Name)
		}
		result := decodeEnvelopeResult(t, envelope)
		if result.System != readbench.SystemHAMT {
			t.Fatalf("record system = %q, want hamt", result.System)
		}
		if result.ElapsedNS <= 0 {
			t.Fatalf("elapsed_ns = %d, want > 0", result.ElapsedNS)
		}
		if result.CAS.GetCount == 0 || result.CAS.BytesGet == 0 {
			t.Fatalf("CAS metrics not preserved in envelope record: %+v", result.CAS)
		}
	}
	rangeResult := decodeEnvelopeResult(t, envelopes[1])
	if rangeResult.OperationKind != readbench.OperationContentRange {
		t.Fatalf("second operation = %q, want content_range", rangeResult.OperationKind)
	}
	if rangeResult.RangeHeader != "bytes=3-6" {
		t.Fatalf("range header = %q, want bytes=3-6", rangeResult.RangeHeader)
	}
	if rangeResult.ContentBytes == nil || *rangeResult.ContentBytes != 4 {
		t.Fatalf("content bytes = %v, want 4", rangeResult.ContentBytes)
	}
}

func TestResultSchemaIsCheckedInForReadBenchResult(t *testing.T) {
	schema := readResultSchema(t)
	if schema.Schema == "" || schema.ID == "" {
		t.Fatalf("schema must declare $schema and $id: %+v", schema)
	}
	if schema.Type != "object" {
		t.Fatalf("schema type = %q, want object", schema.Type)
	}
	if schema.AdditionalProperties {
		t.Fatal("read query result schema should reject unknown top-level fields")
	}

	wantFields, wantRequired := readbenchResultJSONFields(t)
	for fieldName := range wantFields {
		if _, ok := schema.Properties[fieldName]; !ok {
			t.Fatalf("schema missing Result field %q", fieldName)
		}
	}
	for fieldName := range schema.Properties {
		if _, ok := wantFields[fieldName]; !ok {
			t.Fatalf("schema has unknown top-level field %q", fieldName)
		}
	}
	for fieldName := range wantRequired {
		if !containsString(schema.Required, fieldName) {
			t.Fatalf("schema required list missing %q", fieldName)
		}
	}
}

func newSuiteTestEnv(t *testing.T, runID string) framework.Env {
	t.Helper()

	outputDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(outputDir, "raw"), 0o755); err != nil {
		t.Fatalf("create raw dir: %v", err)
	}
	return framework.Env{
		RunID:     runID,
		OutputDir: outputDir,
	}
}

func readRawEnvelopes(t *testing.T, path string) []framework.RecordEnvelope {
	t.Helper()

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open raw output: %v", err)
	}
	defer f.Close()

	var out []framework.RecordEnvelope
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		var envelope framework.RecordEnvelope
		if err := json.Unmarshal(scanner.Bytes(), &envelope); err != nil {
			t.Fatalf("decode envelope %q: %v", scanner.Text(), err)
		}
		out = append(out, envelope)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan raw output: %v", err)
	}
	return out
}

func decodeEnvelopeResult(t *testing.T, envelope framework.RecordEnvelope) readbench.Result {
	t.Helper()

	var result readbench.Result
	if err := json.Unmarshal(envelope.Record, &result); err != nil {
		t.Fatalf("decode readbench result: %v", err)
	}
	return result
}

type jsonSchema struct {
	Schema               string                  `json:"$schema"`
	ID                   string                  `json:"$id"`
	Title                string                  `json:"title"`
	Type                 string                  `json:"type"`
	AdditionalProperties bool                    `json:"additionalProperties"`
	Required             []string                `json:"required"`
	Properties           map[string]schemaObject `json:"properties"`
}

type schemaObject struct {
	Type string `json:"type"`
	Ref  string `json:"$ref"`
}

func readResultSchema(t *testing.T) jsonSchema {
	t.Helper()

	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", "cmd", "eval", "schemas", "read-query-result.schema.json"))
	if err != nil {
		t.Fatalf("read schema %s: %v", ResultSchemaPath, err)
	}
	var schema jsonSchema
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("parse schema %s: %v", ResultSchemaPath, err)
	}
	return schema
}

func readbenchResultJSONFields(t *testing.T) (map[string]struct{}, map[string]struct{}) {
	t.Helper()

	fields := make(map[string]struct{})
	required := make(map[string]struct{})
	resultType := reflect.TypeOf(readbench.Result{})
	for i := 0; i < resultType.NumField(); i++ {
		field := resultType.Field(i)
		name, omitempty := jsonFieldName(field)
		if name == "" || name == "-" {
			continue
		}
		fields[name] = struct{}{}
		if !omitempty {
			required[name] = struct{}{}
		}
	}
	return fields, required
}

func jsonFieldName(field reflect.StructField) (string, bool) {
	tag := field.Tag.Get("json")
	if tag == "" {
		return field.Name, false
	}
	parts := bytes.Split([]byte(tag), []byte(","))
	name := string(parts[0])
	if name == "" {
		name = field.Name
	}
	for _, opt := range parts[1:] {
		if string(opt) == "omitempty" {
			return name, true
		}
	}
	return name, false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

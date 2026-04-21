package eval_test

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dewebprotocol/malt/core/cas/mock"
	"github.com/dewebprotocol/malt/core/commitment/kzg"
	"github.com/dewebprotocol/malt/core/eat/overwrite"
	kvstore_memory "github.com/dewebprotocol/malt/core/kvstore/memory"
	mappingindexed "github.com/dewebprotocol/malt/core/structure/mapping/indexed"
	"github.com/dewebprotocol/malt/eval"
)

// newTestEAT creates a new EAT for testing.
func newTestEAT() *overwrite.EAT {
	kv := kvstore_memory.New()
	e, err := overwrite.NewEAT(overwrite.WithKVStore(kv))
	if err != nil {
		panic(err)
	}
	return e
}

const testBucketId = "test-graph"

func TestBenchmarkRunner(t *testing.T) {
	// Create components
	e := newTestEAT()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	semantic, err := mappingindexed.NewMap(scheme, e)
	if err != nil {
		t.Fatalf("indexed.NewMap failed: %v", err)
	}
	c := mock.NewCAS(mock.WithoutLatency())

	// Create benchmark runner with small config for quick test
	cfg := &eval.BenchmarkConfig{
		ArcCounts:    []int{10, 100},
		UpdateRounds: 10,
		RandomSeed:   42,
		Backend:      eval.BackendKZG,
		EATType:      eval.EATOverwrite,
	}

	runner := eval.NewBenchmarkRunner(cfg, testBucketId, e, semantic, c)

	ctx := context.Background()

	// Run append benchmark
	appendResults, err := runner.RunAppendBenchmark(ctx)
	if err != nil {
		t.Fatalf("Append benchmark failed: %v", err)
	}

	if len(appendResults) != 2 {
		t.Errorf("Expected 2 results, got %d", len(appendResults))
	}

	// Check metrics are reasonable
	for arcCount, metrics := range appendResults {
		if metrics.ArcCount != arcCount {
			t.Errorf("ArcCount mismatch: got %d, want %d", metrics.ArcCount, arcCount)
		}
		if metrics.Backend != eval.BackendKZG {
			t.Errorf("Backend mismatch: got %q, want %q", metrics.Backend, eval.BackendKZG)
		}
		if metrics.ProofSize <= 0 {
			t.Errorf("ProofSize should be positive: %d", metrics.ProofSize)
		}
		if metrics.RewriteAmp != 1.0 {
			t.Errorf("RewriteAmp should be 1.0 for MALT: %f", metrics.RewriteAmp)
		}
	}

	// Run random benchmark
	randomResults, err := runner.RunRandomBenchmark(ctx)
	if err != nil {
		t.Fatalf("Random benchmark failed: %v", err)
	}

	if len(randomResults) != 2 {
		t.Errorf("Expected 2 results, got %d", len(randomResults))
	}

	// Run bulk benchmark
	bulkResults, err := runner.RunBulkBenchmark(ctx)
	if err != nil {
		t.Fatalf("Bulk benchmark failed: %v", err)
	}

	if len(bulkResults) != 2 {
		t.Errorf("Expected 2 results, got %d", len(bulkResults))
	}

	// Print results
	eval.PrintResults(appendResults, "Append")
	eval.PrintResults(randomResults, "Random")
	eval.PrintResults(bulkResults, "Bulk")
}

func TestAllBackends(t *testing.T) {
	ctx := context.Background()
	cfg := &eval.BenchmarkConfig{
		ArcCounts:    []int{10},
		UpdateRounds: 5,
		RandomSeed:   42,
	}

	runner := eval.NewBenchmarkRunner(cfg, testBucketId, newTestEAT(), nil, nil)

	allResults, err := runner.RunAllBackends(ctx, "append")
	if err != nil {
		t.Fatalf("RunAllBackends failed: %v", err)
	}

	if want := len(eval.AllBackends()); len(allResults) != want {
		t.Errorf("Expected %d backends, got %d", want, len(allResults))
	}

	for _, backend := range eval.AllBackends() {
		results, ok := allResults[backend]
		if !ok {
			t.Errorf("Missing results for backend %s", backend)
			continue
		}
		if len(results) != 1 {
			t.Errorf("Expected 1 arc count for %s, got %d", backend, len(results))
		}
		for ac, m := range results {
			if m.Backend != backend {
				t.Errorf("Backend mismatch for %s at arc %d: got %q", backend, ac, m.Backend)
			}
			if m.ProofSize <= 0 {
				t.Errorf("ProofSize should be positive for %s: %d", backend, m.ProofSize)
			}
		}
	}

	eval.PrintBackendComparison(allResults, "Append")
}

func TestNewTestComponents(t *testing.T) {
	for _, backend := range eval.AllBackends() {
		tc, err := eval.NewTestComponents(backend, "test-bucket")
		if err != nil {
			t.Fatalf("NewTestComponents(%s) failed: %v", backend, err)
		}
		if tc.EAT == nil {
			t.Errorf("EAT is nil for %s", backend)
		}
		if tc.Semantic == nil {
			t.Errorf("Semantic is nil for %s", backend)
		}
		if tc.CAS == nil {
			t.Errorf("CAS is nil for %s", backend)
		}
		if tc.BucketID != "test-bucket" {
			t.Errorf("BucketID mismatch for %s: got %q", backend, tc.BucketID)
		}
	}
}

func TestNewScheme(t *testing.T) {
	for _, backend := range eval.AllBackends() {
		s, err := eval.NewScheme(backend)
		if err != nil {
			t.Fatalf("NewScheme(%s) failed: %v", backend, err)
		}
		if s == nil {
			t.Errorf("Scheme is nil for %s", backend)
		}
	}

	_, err := eval.NewScheme("invalid")
	if err == nil {
		t.Error("NewScheme should fail for invalid backend type")
	}
}

func TestAllEATTypes(t *testing.T) {
	for _, eatType := range eval.AllEATTypes() {
		tc, err := eval.NewTestComponentsWithEAT(eval.BackendKZG, eatType, "test-bucket")
		if err != nil {
			t.Fatalf("NewTestComponentsWithEAT(kzg, %s) failed: %v", eatType, err)
		}
		if tc.EAT == nil {
			t.Errorf("EAT is nil for %s", eatType)
		}
		if tc.Semantic == nil {
			t.Errorf("Semantic is nil for %s", eatType)
		}
	}

	_, err := eval.NewEAT("invalid", nil)
	if err == nil {
		t.Error("NewEAT should fail for invalid EAT type")
	}
}

func TestEvalRunner(t *testing.T) {
	ctx := context.Background()
	cfg := &eval.EvalConfig{
		ArcCounts:    []int{10},
		UpdateRounds: 5,
		RandomSeed:   42,
		Backends:     []eval.BackendType{eval.BackendKZG},
		EATTypes:     []eval.EATType{eval.EATOverwrite},
		Workloads:    []string{"append"},
	}

	runner := eval.NewEvalRunner(cfg, "test-eval-bucket")
	results, err := runner.RunAll(ctx)
	if err != nil {
		t.Fatalf("RunAll failed: %v", err)
	}

	m, ok := results[eval.BackendKZG][eval.EATOverwrite]["append"]
	if !ok {
		t.Fatal("Missing results for kzg/overwrite/append")
	}
	if len(m) != 1 {
		t.Errorf("Expected 1 arc count, got %d", len(m))
	}
	for ac, metrics := range m {
		if metrics.ArcCount != ac {
			t.Errorf("ArcCount mismatch: got %d, want %d", metrics.ArcCount, ac)
		}
		if metrics.Backend != eval.BackendKZG {
			t.Errorf("Backend mismatch: got %q", metrics.Backend)
		}
		if metrics.EATType != eval.EATOverwrite {
			t.Errorf("EATType mismatch: got %q", metrics.EATType)
		}
		if metrics.EndToEndLatency <= 0 {
			t.Errorf("EndToEndLatency should be positive: %v", metrics.EndToEndLatency)
		}
	}
}

func TestEvalRunner_DefaultConfig(t *testing.T) {
	// Test that default config doesn't crash (use small counts)
	cfg := eval.DefaultEvalConfig()
	cfg.ArcCounts = []int{5}
	cfg.UpdateRounds = 2
	cfg.Backends = []eval.BackendType{eval.BackendKZG}
	cfg.EATTypes = []eval.EATType{eval.EATOverwrite}
	cfg.Workloads = []string{"append"}

	runner := eval.NewEvalRunner(cfg, "test-default")
	ctx := context.Background()
	results, err := runner.RunAll(ctx)
	if err != nil {
		t.Fatalf("RunAll with default config failed: %v", err)
	}

	eval.PrintFullResults(results)
}

func TestComputeSummaryStats(t *testing.T) {
	m := &eval.Metrics{
		Backend:         eval.BackendKZG,
		EATType:         eval.EATOverwrite,
		ArcCount:        100,
		CommitTime:      10 * time.Millisecond,
		ProveTime:       80 * time.Millisecond,
		VerifyTime:      2 * time.Millisecond,
		EndToEndLatency: 100 * time.Millisecond,
		ProofSize:       84,
		RootSize:        55,
		RewriteAmp:      1.0,
		UpdateTimes: []time.Duration{
			5 * time.Millisecond,
			7 * time.Millisecond,
			6 * time.Millisecond,
			8 * time.Millisecond,
			10 * time.Millisecond,
		},
	}

	s := eval.ComputeSummaryStats(m)
	if s.Backend != eval.BackendKZG {
		t.Errorf("Backend mismatch: got %q", s.Backend)
	}
	if s.ArcCount != 100 {
		t.Errorf("ArcCount mismatch: got %d", s.ArcCount)
	}
	if s.Update.Min != 5*time.Millisecond {
		t.Errorf("Update min mismatch: got %v, want 5ms", s.Update.Min)
	}
	if s.Update.Max != 10*time.Millisecond {
		t.Errorf("Update max mismatch: got %v, want 10ms", s.Update.Max)
	}
}

func TestExportJSON(t *testing.T) {
	ctx := context.Background()
	cfg := &eval.EvalConfig{
		ArcCounts:    []int{10},
		UpdateRounds: 5,
		RandomSeed:   42,
		Backends:     []eval.BackendType{eval.BackendKZG},
		EATTypes:     []eval.EATType{eval.EATOverwrite},
		Workloads:    []string{"append"},
	}

	runner := eval.NewEvalRunner(cfg, "test-json")
	results, err := runner.RunAll(ctx)
	if err != nil {
		t.Fatalf("RunAll failed: %v", err)
	}

	// Write JSON to temp file
	tmpFile := t.TempDir() + "/results.json"
	err = eval.ExportJSON(results, tmpFile)
	if err != nil {
		t.Fatalf("ExportJSON failed: %v", err)
	}

	// Verify file was created
	f, err := os.Open(tmpFile)
	if err != nil {
		t.Fatalf("Cannot open JSON file: %v", err)
	}
	defer f.Close()

	var parsed []struct {
		Backend  eval.BackendType     `json:"backend"`
		EATType  eval.EATType         `json:"eat_type"`
		Workload string               `json:"workload"`
		ArcCount int                  `json:"arc_count"`
		Metrics  *eval.MetricsSummary `json:"metrics"`
	}
	if err := json.NewDecoder(f).Decode(&parsed); err != nil {
		t.Fatalf("Cannot parse JSON file: %v", err)
	}

	if len(parsed) != 1 {
		t.Errorf("Expected 1 result, got %d", len(parsed))
	}
	if parsed[0].Backend != eval.BackendKZG {
		t.Errorf("Backend mismatch: got %q", parsed[0].Backend)
	}
	if parsed[0].Metrics.ProofSize <= 0 {
		t.Errorf("ProofSize should be positive: %d", parsed[0].Metrics.ProofSize)
	}
}

func TestGenerateLatexTable(t *testing.T) {
	ctx := context.Background()
	cfg := &eval.EvalConfig{
		ArcCounts:    []int{10},
		UpdateRounds: 5,
		RandomSeed:   42,
		Backends:     []eval.BackendType{eval.BackendKZG},
		EATTypes:     []eval.EATType{eval.EATOverwrite},
		Workloads:    []string{"append"},
	}

	runner := eval.NewEvalRunner(cfg, "test-latex")
	results, err := runner.RunAll(ctx)
	if err != nil {
		t.Fatalf("RunAll failed: %v", err)
	}

	latex := eval.GenerateLatexTable(results, "append")
	if latex == "" {
		t.Fatal("GenerateLatexTable returned empty string")
	}
	if !strings.Contains(latex, "\\begin{table}") {
		t.Error("LaTeX table missing \\begin{table}")
	}
	if !strings.Contains(latex, "\\begin{tabular}") {
		t.Error("LaTeX table missing \\begin{tabular}")
	}
	if !strings.Contains(latex, "kzg") {
		t.Error("LaTeX table missing backend 'kzg'")
	}
	proofSizeFound := false
	for _, eatResults := range results[eval.BackendKZG] {
		if metricsByArc, ok := eatResults["append"]; ok {
			for _, metrics := range metricsByArc {
				if strings.Contains(latex, strconv.Itoa(metrics.ProofSize)) {
					proofSizeFound = true
					break
				}
			}
		}
		if proofSizeFound {
			break
		}
	}
	if !proofSizeFound {
		t.Error("LaTeX table missing proof size")
	}
}

func BenchmarkAppend(b *testing.B) {
	e := newTestEAT()
	scheme, _ := kzg.NewScheme()
	semantic, _ := mappingindexed.NewMap(scheme, e)
	c := mock.NewCAS(mock.WithoutLatency())

	cfg := &eval.BenchmarkConfig{
		ArcCounts:    []int{1000},
		UpdateRounds: 100,
		RandomSeed:   42,
		Backend:      eval.BackendKZG,
		EATType:      eval.EATOverwrite,
	}

	runner := eval.NewBenchmarkRunner(cfg, testBucketId, e, semantic, c)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runner.RunAppendBenchmark(ctx)
	}
}

func BenchmarkRandom(b *testing.B) {
	e := newTestEAT()
	scheme, _ := kzg.NewScheme()
	semantic, _ := mappingindexed.NewMap(scheme, e)
	c := mock.NewCAS(mock.WithoutLatency())

	cfg := &eval.BenchmarkConfig{
		ArcCounts:    []int{1000},
		UpdateRounds: 100,
		RandomSeed:   42,
		Backend:      eval.BackendKZG,
		EATType:      eval.EATOverwrite,
	}

	runner := eval.NewBenchmarkRunner(cfg, testBucketId, e, semantic, c)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runner.RunRandomBenchmark(ctx)
	}
}

func BenchmarkBulk(b *testing.B) {
	e := newTestEAT()
	scheme, _ := kzg.NewScheme()
	semantic, _ := mappingindexed.NewMap(scheme, e)
	c := mock.NewCAS(mock.WithoutLatency())

	cfg := &eval.BenchmarkConfig{
		ArcCounts:    []int{1000},
		UpdateRounds: 100,
		RandomSeed:   42,
		Backend:      eval.BackendKZG,
		EATType:      eval.EATOverwrite,
	}

	runner := eval.NewBenchmarkRunner(cfg, testBucketId, e, semantic, c)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runner.RunBulkBenchmark(ctx)
	}
}

package eval_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/core/cas/mock"
	"github.com/dewebprotocol/malt/core/eat/overwrite"
	kvstore_memory "github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/eval"
	"github.com/dewebprotocol/malt/core/sce"
	"github.com/dewebprotocol/malt/core/sce/commitment/kzg"
)

// newTestEAT creates a new EAT for testing.
func newTestEAT() *overwrite.EAT {
	kv := kvstore_memory.New()
	e, err := overwrite.NewEAT(kv)
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
	s := sce.NewEngine(scheme)
	c := mock.NewCAS()

	// Create benchmark runner with small config for quick test
	cfg := &eval.BenchmarkConfig{
		ArcCounts:    []int{10, 100},
		UpdateRounds: 10,
		RandomSeed:   42,
		Backend:      eval.BackendKZG,
	}

	runner := eval.NewBenchmarkRunner(cfg, testBucketId, e, s, c)

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

	if len(allResults) != 3 {
		t.Errorf("Expected 3 backends, got %d", len(allResults))
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
		if tc.SCE == nil {
			t.Errorf("SCE is nil for %s", backend)
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

func BenchmarkAppend(b *testing.B) {
	e := newTestEAT()
	scheme, _ := kzg.NewScheme()
	s := sce.NewEngine(scheme)
	c := mock.NewCAS()

	cfg := &eval.BenchmarkConfig{
		ArcCounts:    []int{1000},
		UpdateRounds: 100,
		RandomSeed:   42,
		Backend:      eval.BackendKZG,
	}

	runner := eval.NewBenchmarkRunner(cfg, testBucketId, e, s, c)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runner.RunAppendBenchmark(ctx)
	}
}

func BenchmarkRandom(b *testing.B) {
	e := newTestEAT()
	scheme, _ := kzg.NewScheme()
	s := sce.NewEngine(scheme)
	c := mock.NewCAS()

	cfg := &eval.BenchmarkConfig{
		ArcCounts:    []int{1000},
		UpdateRounds: 100,
		RandomSeed:   42,
		Backend:      eval.BackendKZG,
	}

	runner := eval.NewBenchmarkRunner(cfg, testBucketId, e, s, c)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runner.RunRandomBenchmark(ctx)
	}
}

func BenchmarkBulk(b *testing.B) {
	e := newTestEAT()
	scheme, _ := kzg.NewScheme()
	s := sce.NewEngine(scheme)
	c := mock.NewCAS()

	cfg := &eval.BenchmarkConfig{
		ArcCounts:    []int{1000},
		UpdateRounds: 100,
		RandomSeed:   42,
		Backend:      eval.BackendKZG,
	}

	runner := eval.NewBenchmarkRunner(cfg, testBucketId, e, s, c)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runner.RunBulkBenchmark(ctx)
	}
}

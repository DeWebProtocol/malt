package eval_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/cas/mock"
	"github.com/dewebprotocol/malt/core/eat/overwrite"
	kvstore_memory "github.com/dewebprotocol/malt/core/types/kvstore/memory"
	"github.com/dewebprotocol/malt/eval"
	"github.com/dewebprotocol/malt/core/sce"
	"github.com/dewebprotocol/malt/core/sce/commitment/kzg"
)

// newTestEAT creates a new EAT for testing.
func newTestEAT() *overwrite.EAT {
	kv := kvstore_memory.New()
	e, err := overwrite.NewEAT(kv, "test-graph")
	if err != nil {
		panic(err)
	}
	return e
}

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
	}

	runner := eval.NewBenchmarkRunner(cfg, e, s, c)

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

func BenchmarkAppend(b *testing.B) {
	e := newTestEAT()
	scheme, _ := kzg.NewScheme()
	s := sce.NewEngine(scheme)
	c := mock.NewCAS()

	cfg := &eval.BenchmarkConfig{
		ArcCounts:    []int{1000},
		UpdateRounds: 100,
		RandomSeed:   42,
	}

	runner := eval.NewBenchmarkRunner(cfg, e, s, c)
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
	}

	runner := eval.NewBenchmarkRunner(cfg, e, s, c)
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
	}

	runner := eval.NewBenchmarkRunner(cfg, e, s, c)
	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		runner.RunBulkBenchmark(ctx)
	}
}
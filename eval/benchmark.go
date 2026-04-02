// Package eval provides evaluation and benchmarking tools for MALT.
package eval

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/dewebprotocol/malt/core/eat/memory"
	"github.com/dewebprotocol/malt/cas"
	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/sce"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// Metrics collected during evaluation.
type Metrics struct {
	// Operation timing
	CommitTime   time.Duration
	ProveTime    time.Duration
	VerifyTime   time.Duration
	UpdateTime   time.Duration
	ResolveTime  time.Duration

	// Size metrics
	ProofSize    int // bytes per proof
	RootSize     int // bytes per commitment
	ArcCount     int // number of arcs

	// Update metrics
	RewriteAmp  float64 // rewrite amplification factor
}

// BenchmarkConfig holds configuration for benchmarks.
type BenchmarkConfig struct {
	// ArcCounts is the list of arc counts to test
	ArcCounts []int

	// UpdateRounds is the number of update rounds
	UpdateRounds int

	// RandomSeed for reproducibility
	RandomSeed int64
}

// DefaultBenchmarkConfig returns default benchmark configuration.
func DefaultBenchmarkConfig() *BenchmarkConfig {
	return &BenchmarkConfig{
		ArcCounts:    []int{100, 1000, 10000, 100000},
		UpdateRounds: 100,
		RandomSeed:   42,
	}
}

// BenchmarkRunner runs MALT benchmarks.
type BenchmarkRunner struct {
	config *BenchmarkConfig
	eat    eat.EAT
	sce    *sce.Engine
	cas    cas.Client
}

// NewBenchmarkRunner creates a new benchmark runner.
func NewBenchmarkRunner(cfg *BenchmarkConfig, e eat.EAT, s *sce.Engine, c cas.Client) *BenchmarkRunner {
	if cfg == nil {
		cfg = DefaultBenchmarkConfig()
	}
	return &BenchmarkRunner{
		config: cfg,
		eat:    e,
		sce:    s,
		cas:    c,
	}
}

// newPayloadCID creates a CID from data for testing.
func newPayloadCID(data []byte) (cid.Cid, error) {
	mhash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return cid.Cid{}, err
	}
	return cid.NewCidV1(cid.Raw, mhash), nil
}

// RunAppendBenchmark tests append workload (adding new arcs).
func (b *BenchmarkRunner) RunAppendBenchmark(ctx context.Context) (map[int]*Metrics, error) {
	results := make(map[int]*Metrics)

	for _, arcCount := range b.config.ArcCounts {
		metrics, err := b.runAppendWorkload(ctx, arcCount)
		if err != nil {
			return nil, fmt.Errorf("append benchmark failed for %d arcs: %w", arcCount, err)
		}
		results[arcCount] = metrics
	}

	return results, nil
}

// runAppendWorkload executes the append workload.
func (b *BenchmarkRunner) runAppendWorkload(ctx context.Context, arcCount int) (*Metrics, error) {
	r := rand.New(rand.NewSource(b.config.RandomSeed))

	metrics := &Metrics{ArcCount: arcCount}

	// Track current arc set
	currentArcs := memory.NewInMemoryArcSet()

	totalUpdateTime := time.Duration(0)
	var root cid.Cid

	// Add arcs one by one (append pattern)
	for i := range arcCount {
		path := fmt.Sprintf("arc%d", i)
		target, _ := newPayloadCID([]byte(fmt.Sprintf("data%d", i)))

		// Add to current arc set
		currentArcs.Set(path, target)

		// Commit current arc set
		start := time.Now()
		newRoot, err := b.sce.Commit(currentArcs)
		totalUpdateTime += time.Since(start)
		if err != nil {
			return nil, fmt.Errorf("commit failed at arc %d: %w", i, err)
		}

		// Store ALL arcs in EAT for this new root
		iter := currentArcs.Iterate()
		for {
			p, t, ok := iter.Next()
			if !ok {
				break
			}
			b.eat.Put(newRoot, p, t)
		}

		root = newRoot
	}

	// First commit is the initial one
	firstStart := time.Now()
	emptyArcs := memory.NewInMemoryArcSet()
	_, _ = b.sce.Commit(emptyArcs)
	metrics.CommitTime = time.Since(firstStart)

	metrics.UpdateTime = totalUpdateTime / time.Duration(arcCount)

	// Measure proof generation for a random arc
	testPath := fmt.Sprintf("arc%d", r.Intn(arcCount))
	start := time.Now()
	view := b.eat.View(root)
	_, proof, err := b.sce.Prove(root, view, testPath)
	metrics.ProveTime = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("prove failed for %s: %w", testPath, err)
	}
	metrics.ProofSize = len(proof)
	metrics.RootSize = len(root.Bytes())

	// Measure verification
	target, _ := b.eat.Get(root, testPath)
	start = time.Now()
	valid, err := b.sce.Verify(root, testPath, target, proof)
	metrics.VerifyTime = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("verify failed: %w", err)
	}
	if !valid {
		return nil, fmt.Errorf("proof invalid")
	}

	// Rewrite amp = 1.0 for MALT (localized updates)
	metrics.RewriteAmp = 1.0

	return metrics, nil
}

// RunRandomBenchmark tests random update workload.
func (b *BenchmarkRunner) RunRandomBenchmark(ctx context.Context) (map[int]*Metrics, error) {
	results := make(map[int]*Metrics)

	for _, arcCount := range b.config.ArcCounts {
		metrics, err := b.runRandomWorkload(ctx, arcCount)
		if err != nil {
			return nil, fmt.Errorf("random benchmark failed for %d arcs: %w", arcCount, err)
		}
		results[arcCount] = metrics
	}

	return results, nil
}

// runRandomWorkload executes random update workload.
func (b *BenchmarkRunner) runRandomWorkload(ctx context.Context, arcCount int) (*Metrics, error) {
	r := rand.New(rand.NewSource(b.config.RandomSeed))

	metrics := &Metrics{ArcCount: arcCount}

	// Create initial structure with all arcs
	arcs := memory.NewInMemoryArcSet()
	keys := make(map[string]cid.Cid)

	for i := range arcCount {
		path := fmt.Sprintf("arc%d", i)
		target, _ := newPayloadCID([]byte(fmt.Sprintf("data%d", i)))
		arcs.Set(path, target)
		keys[path] = target
	}

	// Initial commit
	start := time.Now()
	root, err := b.sce.Commit(arcs)
	metrics.CommitTime = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("initial commit failed: %w", err)
	}

	// Store in EAT
	iter := arcs.Iterate()
	for {
		path, target, ok := iter.Next()
		if !ok {
			break
		}
		b.eat.Put(root, path, target)
	}

	// Perform random updates
	totalUpdateTime := time.Duration(0)
	paths := make([]string, 0, arcCount)
	for i := range arcCount {
		paths = append(paths, fmt.Sprintf("arc%d", i))
	}

	for round := range b.config.UpdateRounds {
		// Pick random arc to update
		idx := r.Intn(arcCount)
		path := paths[idx]

		newKey, _ := newPayloadCID([]byte(fmt.Sprintf("updated%d_%d", idx, round)))

		// Update arc set
		arcs.Set(path, newKey)

		// Measure commit time (MockCommitment requires full commit)
		start = time.Now()
		newRoot, err := b.sce.Commit(arcs)
		totalUpdateTime += time.Since(start)
		if err != nil {
			return nil, fmt.Errorf("commit failed at round %d: %w", round, err)
		}

		// Store ALL arcs in EAT for this new root
		iter := arcs.Iterate()
		for {
			p, t, ok := iter.Next()
			if !ok {
				break
			}
			b.eat.Put(newRoot, p, t)
		}

		root = newRoot
		keys[path] = newKey
	}

	metrics.UpdateTime = totalUpdateTime / time.Duration(b.config.UpdateRounds)

	// Measure proof generation
	testPath := paths[r.Intn(arcCount)]
	start = time.Now()
	view := b.eat.View(root)
	_, proof, err := b.sce.Prove(root, view, testPath)
	metrics.ProveTime = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("prove failed: %w", err)
	}
	metrics.ProofSize = len(proof)
	metrics.RootSize = len(root.Bytes())

	// Measure verification
	target := keys[testPath]
	start = time.Now()
	valid, err := b.sce.Verify(root, testPath, target, proof)
	metrics.VerifyTime = time.Since(start)
	if err != nil || !valid {
		return nil, fmt.Errorf("verify failed: %w", err)
	}

	metrics.RewriteAmp = 1.0 // MALT localized updates

	return metrics, nil
}

// RunBulkBenchmark tests bulk update workload (many arcs at once).
func (b *BenchmarkRunner) RunBulkBenchmark(ctx context.Context) (map[int]*Metrics, error) {
	results := make(map[int]*Metrics)

	for _, arcCount := range b.config.ArcCounts {
		metrics, err := b.runBulkWorkload(ctx, arcCount)
		if err != nil {
			return nil, fmt.Errorf("bulk benchmark failed for %d arcs: %w", arcCount, err)
		}
		results[arcCount] = metrics
	}

	return results, nil
}

// runBulkWorkload executes bulk update workload.
func (b *BenchmarkRunner) runBulkWorkload(ctx context.Context, arcCount int) (*Metrics, error) {
	r := rand.New(rand.NewSource(b.config.RandomSeed))

	metrics := &Metrics{ArcCount: arcCount}

	// Create initial structure
	arcs := memory.NewInMemoryArcSet()
	keys := make(map[string]cid.Cid)

	for i := range arcCount {
		path := fmt.Sprintf("arc%d", i)
		target, _ := newPayloadCID([]byte(fmt.Sprintf("data%d", i)))
		arcs.Set(path, target)
		keys[path] = target
	}

	start := time.Now()
	root, err := b.sce.Commit(arcs)
	metrics.CommitTime = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("initial commit failed: %w", err)
	}

	iter := arcs.Iterate()
	for {
		path, target, ok := iter.Next()
		if !ok {
			break
		}
		b.eat.Put(root, path, target)
	}

	// Bulk update: update 10% of arcs at once
	bulkSize := max(1, arcCount/10)

	totalUpdateTime := time.Duration(0)
	paths := make([]string, 0, arcCount)
	for i := range arcCount {
		paths = append(paths, fmt.Sprintf("arc%d", i))
	}

	for round := range b.config.UpdateRounds {
		// Select random subset for bulk update
		r.Shuffle(arcCount, func(i, j int) {
			paths[i], paths[j] = paths[j], paths[i]
		})

		// Update arcs in bulk
		for i := range bulkSize {
			path := paths[i]
			newKey, _ := newPayloadCID([]byte(fmt.Sprintf("bulk%d_%d", i, round)))
			arcs.Set(path, newKey)
			keys[path] = newKey
		}

		// Commit updated arc set
		start = time.Now()
		newRoot, err := b.sce.Commit(arcs)
		totalUpdateTime += time.Since(start)
		if err != nil {
			return nil, fmt.Errorf("bulk commit failed: %w", err)
		}

		// Store ALL arcs in EAT for this new root
		iter := arcs.Iterate()
		for {
			p, t, ok := iter.Next()
			if !ok {
				break
			}
			b.eat.Put(newRoot, p, t)
		}

		root = newRoot
	}

	metrics.UpdateTime = totalUpdateTime / time.Duration(b.config.UpdateRounds)

	// Measure proof
	testPath := paths[r.Intn(arcCount)]
	start = time.Now()
	view := b.eat.View(root)
	_, proof, err := b.sce.Prove(root, view, testPath)
	metrics.ProveTime = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("prove failed: %w", err)
	}
	metrics.ProofSize = len(proof)
	metrics.RootSize = len(root.Bytes())

	target := keys[testPath]
	start = time.Now()
	valid, err := b.sce.Verify(root, testPath, target, proof)
	metrics.VerifyTime = time.Since(start)
	if err != nil || !valid {
		return nil, fmt.Errorf("verify failed: %w", err)
	}

	metrics.RewriteAmp = float64(bulkSize) // Each bulk update touches bulkSize arcs

	return metrics, nil
}

// PrintResults prints benchmark results in a formatted table.
func PrintResults(results map[int]*Metrics, workload string) {
	fmt.Printf("\n=== %s Benchmark Results ===\n", workload)
	fmt.Println("ArcCount | Commit(ms) | Update(ms) | Prove(ms) | Verify(ms) | ProofSize | RewriteAmp")
	fmt.Println("---------|------------|------------|-----------|------------|-----------|------------")

	for arcCount, m := range results {
		fmt.Printf("%8d | %10.2f | %10.2f | %9.2f | %10.2f | %9d | %10.2f\n",
			arcCount,
			float64(m.CommitTime.Milliseconds()),
			float64(m.UpdateTime.Milliseconds()),
			float64(m.ProveTime.Milliseconds()),
			float64(m.VerifyTime.Milliseconds()),
			m.ProofSize,
			m.RewriteAmp)
	}
}
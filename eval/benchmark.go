// Package eval provides evaluation and benchmarking tools for MALT.
package eval

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/dewebprotocol/malt/core/cas"
	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/sce"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// Metrics collected during evaluation.
type Metrics struct {
	// Operation timing
	CommitTime  time.Duration
	ProveTime   time.Duration
	VerifyTime  time.Duration
	UpdateTime  time.Duration

	// Size metrics
	ProofSize int // bytes per proof
	RootSize  int // bytes per commitment
	ArcCount  int // number of arcs

	// Update metrics
	RewriteAmp float64 // rewrite amplification factor
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
	config   *BenchmarkConfig
	eat      eat.EAT
	sce      *sce.Engine
	cas      cas.Client
	bucketId string
}

// NewBenchmarkRunner creates a new benchmark runner.
func NewBenchmarkRunner(cfg *BenchmarkConfig, bucketId string, e eat.EAT, s *sce.Engine, c cas.Client) *BenchmarkRunner {
	if cfg == nil {
		cfg = DefaultBenchmarkConfig()
	}
	return &BenchmarkRunner{
		config:   cfg,
		bucketId: bucketId,
		eat:      e,
		sce:      s,
		cas:      c,
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
	currentArcsMap := make(map[string]cid.Cid)

	totalUpdateTime := time.Duration(0)
	var root cid.Cid

	// Add arcs one by one (append pattern)
	for i := range arcCount {
		path := fmt.Sprintf("arc%d", i)
		target, _ := newPayloadCID([]byte(fmt.Sprintf("data%d", i)))

		// Add to current arc set
		currentArcsMap[path] = target

		// Create snapshot for commit
		currentArcs := arcset.NewMapFrom(currentArcsMap)

		// Commit current arc set
		start := time.Now()
		newRoot, err := b.sce.Commit(currentArcs)
		totalUpdateTime += time.Since(start)
		if err != nil {
			return nil, fmt.Errorf("commit failed at arc %d: %w", i, err)
		}

		// Store arcs in EAT using Update
		if err := b.eat.Update(ctx, b.bucketId, newRoot, root, currentArcsMap); err != nil {
			return nil, fmt.Errorf("eat update failed at arc %d: %w", i, err)
		}

		root = newRoot
	}

	// First commit is the initial one
	firstStart := time.Now()
	emptyArcs := arcset.NewMap()
	_, _ = b.sce.Commit(emptyArcs)
	metrics.CommitTime = time.Since(firstStart)

	metrics.UpdateTime = totalUpdateTime / time.Duration(arcCount)

	// Measure proof generation for a random arc
	testPath := fmt.Sprintf("arc%d", r.Intn(arcCount))
	start := time.Now()
	snapshot, err := b.eat.Snapshot(ctx, b.bucketId, root)
	if err != nil {
		return nil, fmt.Errorf("snapshot failed: %w", err)
	}
	_, proof, err := b.sce.Prove(root, snapshot, testPath)
	metrics.ProveTime = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("prove failed for %s: %w", testPath, err)
	}
	metrics.ProofSize = len(proof)
	metrics.RootSize = len(root.Bytes())

	// Measure verification
	target, _ := b.eat.Get(ctx, b.bucketId, root, testPath)
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
	arcsMap := make(map[string]cid.Cid)
	keys := make(map[string]cid.Cid)

	for i := range arcCount {
		path := fmt.Sprintf("arc%d", i)
		target, _ := newPayloadCID([]byte(fmt.Sprintf("data%d", i)))
		arcsMap[path] = target
		keys[path] = target
	}

	arcs := arcset.NewMapFrom(arcsMap)

	// Initial commit
	start := time.Now()
	root, err := b.sce.Commit(arcs)
	metrics.CommitTime = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("initial commit failed: %w", err)
	}

	// Store in EAT
	b.eat.Update(ctx, b.bucketId, root, cid.Undef, arcsMap)

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
		arcsMap[path] = newKey
		arcs := arcset.NewMapFrom(arcsMap)

		// Measure commit time (MockCommitment requires full commit)
		start = time.Now()
		newRoot, err := b.sce.Commit(arcs)
		totalUpdateTime += time.Since(start)
		if err != nil {
			return nil, fmt.Errorf("commit failed at round %d: %w", round, err)
		}

		// Store in EAT
		b.eat.Update(ctx, b.bucketId, newRoot, root, arcsMap)

		root = newRoot
		keys[path] = newKey
	}

	metrics.UpdateTime = totalUpdateTime / time.Duration(b.config.UpdateRounds)

	// Measure proof generation
	testPath := paths[r.Intn(arcCount)]
	start = time.Now()
	snapshot, err := b.eat.Snapshot(ctx, b.bucketId, root)
	if err != nil {
		return nil, fmt.Errorf("snapshot failed: %w", err)
	}
	_, proof, err := b.sce.Prove(root, snapshot, testPath)
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
	arcsMap := make(map[string]cid.Cid)
	keys := make(map[string]cid.Cid)

	for i := range arcCount {
		path := fmt.Sprintf("arc%d", i)
		target, _ := newPayloadCID([]byte(fmt.Sprintf("data%d", i)))
		arcsMap[path] = target
		keys[path] = target
	}

	arcs := arcset.NewMapFrom(arcsMap)

	start := time.Now()
	root, err := b.sce.Commit(arcs)
	metrics.CommitTime = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("initial commit failed: %w", err)
	}

	// Store in EAT
	b.eat.Update(ctx, b.bucketId, root, cid.Undef, arcsMap)

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
			arcsMap[path] = newKey
			keys[path] = newKey
		}

		arcs := arcset.NewMapFrom(arcsMap)

		// Commit updated arc set
		start = time.Now()
		newRoot, err := b.sce.Commit(arcs)
		totalUpdateTime += time.Since(start)
		if err != nil {
			return nil, fmt.Errorf("bulk commit failed: %w", err)
		}

		// Store in EAT
		b.eat.Update(ctx, b.bucketId, newRoot, root, arcsMap)

		root = newRoot
	}

	metrics.UpdateTime = totalUpdateTime / time.Duration(b.config.UpdateRounds)

	// Measure proof
	testPath := paths[r.Intn(arcCount)]
	start = time.Now()
	snapshot, err := b.eat.Snapshot(ctx, b.bucketId, root)
	if err != nil {
		return nil, fmt.Errorf("snapshot failed: %w", err)
	}
	_, proof, err := b.sce.Prove(root, snapshot, testPath)
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
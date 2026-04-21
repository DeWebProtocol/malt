// Package eval provides evaluation and benchmarking tools for MALT.
package eval

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dewebprotocol/malt/core/cas"
	"github.com/dewebprotocol/malt/core/cas/mock"
	"github.com/dewebprotocol/malt/core/commitment"
	"github.com/dewebprotocol/malt/core/commitment/kzg"
	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/eat/bloom"
	"github.com/dewebprotocol/malt/core/eat/overwrite"
	"github.com/dewebprotocol/malt/core/eat/versioned"
	"github.com/dewebprotocol/malt/core/kvstore"
	kvstore_memory "github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	mappingindexed "github.com/dewebprotocol/malt/core/structure/mapping/indexed"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// BackendType selects the commitment scheme.
type BackendType string

const (
	BackendKZG BackendType = "kzg"
)

// AllBackends returns all available backend types.
func AllBackends() []BackendType {
	return []BackendType{BackendKZG}
}

// EATType selects the EAT implementation.
type EATType string

const (
	EATOverwrite EATType = "overwrite"
	EATVersioned EATType = "versioned"
	EATBloom     EATType = "bloom"
)

// AllEATTypes returns all available EAT types.
func AllEATTypes() []EATType {
	return []EATType{EATOverwrite, EATVersioned, EATBloom}
}

// NewEAT creates an eat.EAT for the given EAT type.
func NewEAT(t EATType, kv kvstore.KVStore) (eat.EAT, error) {
	switch t {
	case EATOverwrite:
		return overwrite.NewEAT(overwrite.WithKVStore(kv))
	case EATVersioned:
		return versioned.NewEAT(versioned.WithKVStore(kv))
	case EATBloom:
		bloomCache := bloom.NewBloomCache(kv, 16*1024*1024) // 16MB default
		return versioned.NewEAT(versioned.WithKVStore(kv), versioned.WithBloomCache(bloomCache))
	default:
		return nil, fmt.Errorf("unknown EAT type: %s", t)
	}
}

// NewScheme creates an index commitment backend for the given backend type.
func NewScheme(b BackendType) (commitment.IndexCommitment, error) {
	switch b {
	case BackendKZG:
		return kzg.NewScheme()
	default:
		return nil, fmt.Errorf("unknown backend type: %s", b)
	}
}

// Metrics collected during evaluation.
type Metrics struct {
	// Backend used
	Backend BackendType

	// EAT type used
	EATType EATType

	// Operation timing
	CommitTime      time.Duration
	ProveTime       time.Duration
	VerifyTime      time.Duration
	UpdateTime      time.Duration
	EATLookupTime   time.Duration // time for EAT Get/Snapshot
	EndToEndLatency time.Duration // full resolve: EAT + Prove + Verify

	// Per-round timing (for workloads with multiple rounds)
	UpdateTimes []time.Duration // individual update times
	ProveTimes  []time.Duration // individual prove times (for distribution)
	VerifyTimes []time.Duration // individual verify times

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

	// Backend selects the commitment scheme (default: kzg).
	Backend BackendType

	// EATType selects the EAT implementation (default: overwrite).
	EATType EATType
}

// DefaultBenchmarkConfig returns default benchmark configuration.
func DefaultBenchmarkConfig() *BenchmarkConfig {
	return &BenchmarkConfig{
		ArcCounts:    []int{100, 1000, 10000, 100000},
		UpdateRounds: 100,
		RandomSeed:   42,
		Backend:      BackendKZG,
		EATType:      EATOverwrite,
	}
}

// BenchmarkRunner runs MALT benchmarks.
type BenchmarkRunner struct {
	config   *BenchmarkConfig
	eat      eat.EAT
	semantic mapping.Semantic
	cas      cas.Client
	bucketId string
}

// NewBenchmarkRunner creates a new benchmark runner.
func NewBenchmarkRunner(cfg *BenchmarkConfig, bucketId string, e eat.EAT, semantic mapping.Semantic, c cas.Client) *BenchmarkRunner {
	if cfg == nil {
		cfg = DefaultBenchmarkConfig()
	}
	return &BenchmarkRunner{
		config:   cfg,
		bucketId: bucketId,
		eat:      e,
		semantic: semantic,
		cas:      c,
	}
}

// TestComponents holds the components needed for benchmarking.
type TestComponents struct {
	EAT      eat.EAT
	Semantic mapping.Semantic
	CAS      cas.Client
	BucketID string
}

// NewTestComponents creates a fresh set of benchmark components for the given backend.
func NewTestComponents(backend BackendType, bucketID string) (*TestComponents, error) {
	return NewTestComponentsWithEAT(backend, EATOverwrite, bucketID)
}

// NewTestComponentsWithEAT creates benchmark components with a specific EAT type.
func NewTestComponentsWithEAT(backend BackendType, eatType EATType, bucketID string) (*TestComponents, error) {
	scheme, err := NewScheme(backend)
	if err != nil {
		return nil, fmt.Errorf("new scheme: %w", err)
	}
	kv := kvstore_memory.New()
	e, err := NewEAT(eatType, kv)
	if err != nil {
		return nil, fmt.Errorf("new EAT: %w", err)
	}
	s, err := mappingindexed.NewMap(scheme)
	if err != nil {
		return nil, fmt.Errorf("new mapping semantic: %w", err)
	}
	c := mock.NewCAS()
	return &TestComponents{
		EAT:      e,
		Semantic: s,
		CAS:      c,
		BucketID: bucketID,
	}, nil
}

// newPayloadCID creates a CID from data for testing.
func newPayloadCID(data []byte) (cid.Cid, error) {
	mhash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return cid.Cid{}, err
	}
	return cid.NewCidV1(cid.Raw, mhash), nil
}

func stringifyArcSet(arcs arcset.ArcSet) map[string]cid.Cid {
	out := make(map[string]cid.Cid, arcs.Len())
	iter := arcs.Iterate()
	for {
		path, target, ok := iter.Next()
		if !ok {
			break
		}
		out[path.String()] = target
	}
	return out
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
	bucketID := fmt.Sprintf("%s-append-%d", b.bucketId, arcCount)

	metrics := &Metrics{ArcCount: arcCount, Backend: b.config.Backend, EATType: b.config.EATType}

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
		// Commit current arc set
		start := time.Now()
		newRoot, err := b.semantic.Commit(ctx, mapping.NewViewFrom(currentArcsMap))
		totalUpdateTime += time.Since(start)
		if err != nil {
			return nil, fmt.Errorf("commit failed at arc %d: %w", i, err)
		}

		// Store arcs in EAT using Update
		if err := b.eat.Update(ctx, bucketID, newRoot, root, currentArcsMap); err != nil {
			return nil, fmt.Errorf("eat update failed at arc %d: %w", i, err)
		}

		root = newRoot
	}

	// First commit is the initial one
	firstStart := time.Now()
	_, _ = b.semantic.Commit(ctx, mapping.NewViewFrom(map[string]cid.Cid{}))
	metrics.CommitTime = time.Since(firstStart)

	metrics.UpdateTime = totalUpdateTime / time.Duration(arcCount)

	// Measure proof generation for a random arc
	testPath := fmt.Sprintf("arc%d", r.Intn(arcCount))
	start := time.Now()
	snapshot, err := b.eat.Snapshot(ctx, bucketID, root)
	metrics.EATLookupTime = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("snapshot failed: %w", err)
	}
	start = time.Now()
	binding, proof, err := b.semantic.Prove(ctx, root, mapping.NewViewFrom(stringifyArcSet(snapshot)), arcset.CanonicalizePath(testPath))
	metrics.ProveTime = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("prove failed for %s: %w", testPath, err)
	}
	if !binding.Present {
		return nil, fmt.Errorf("prove returned non-membership for %s", testPath)
	}
	metrics.ProofSize = len(proof)
	metrics.RootSize = len(root.Bytes())

	// Measure verification
	target, _ := b.eat.Get(ctx, bucketID, root, testPath)
	start = time.Now()
	valid, err := b.semantic.Verify(root, arcset.CanonicalizePath(testPath), mapping.Binding{Value: target, Present: true}, proof)
	metrics.VerifyTime = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("verify failed: %w", err)
	}
	if !valid {
		return nil, fmt.Errorf("proof invalid")
	}

	// End-to-end latency: EAT lookup + Prove + Verify
	metrics.EndToEndLatency = metrics.EATLookupTime + metrics.ProveTime + metrics.VerifyTime

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
	bucketID := fmt.Sprintf("%s-random-%d", b.bucketId, arcCount)

	metrics := &Metrics{ArcCount: arcCount, Backend: b.config.Backend, EATType: b.config.EATType}

	// Create initial structure with all arcs
	arcsMap := make(map[string]cid.Cid)
	keys := make(map[string]cid.Cid)

	for i := range arcCount {
		path := fmt.Sprintf("arc%d", i)
		target, _ := newPayloadCID([]byte(fmt.Sprintf("data%d", i)))
		arcsMap[path] = target
		keys[path] = target
	}

	// Initial commit
	start := time.Now()
	root, err := b.semantic.Commit(ctx, mapping.NewViewFrom(arcsMap))
	metrics.CommitTime = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("initial commit failed: %w", err)
	}

	// Store in EAT
	b.eat.Update(ctx, bucketID, root, cid.Undef, arcsMap)

	// Perform random updates
	totalUpdateTime := time.Duration(0)
	metrics.UpdateTimes = make([]time.Duration, 0, b.config.UpdateRounds)
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
		// Measure commit time (MockCommitment requires full commit)
		start = time.Now()
		newRoot, err := b.semantic.Commit(ctx, mapping.NewViewFrom(arcsMap))
		roundTime := time.Since(start)
		totalUpdateTime += roundTime
		metrics.UpdateTimes = append(metrics.UpdateTimes, roundTime)
		if err != nil {
			return nil, fmt.Errorf("commit failed at round %d: %w", round, err)
		}

		// Store in EAT
		b.eat.Update(ctx, bucketID, newRoot, root, arcsMap)

		root = newRoot
		keys[path] = newKey
	}

	metrics.UpdateTime = totalUpdateTime / time.Duration(b.config.UpdateRounds)

	// Measure proof generation
	testPath := paths[r.Intn(arcCount)]
	start = time.Now()
	snapshot, err := b.eat.Snapshot(ctx, bucketID, root)
	metrics.EATLookupTime = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("snapshot failed: %w", err)
	}
	start = time.Now()
	binding, proof, err := b.semantic.Prove(ctx, root, mapping.NewViewFrom(stringifyArcSet(snapshot)), arcset.CanonicalizePath(testPath))
	metrics.ProveTime = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("prove failed: %w", err)
	}
	if !binding.Present {
		return nil, fmt.Errorf("prove returned non-membership")
	}
	metrics.ProofSize = len(proof)
	metrics.RootSize = len(root.Bytes())

	// Measure verification
	target := keys[testPath]
	start = time.Now()
	valid, err := b.semantic.Verify(root, arcset.CanonicalizePath(testPath), mapping.Binding{Value: target, Present: true}, proof)
	metrics.VerifyTime = time.Since(start)
	if err != nil || !valid {
		return nil, fmt.Errorf("verify failed: %w", err)
	}

	// End-to-end latency
	metrics.EndToEndLatency = metrics.EATLookupTime + metrics.ProveTime + metrics.VerifyTime

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
	bucketID := fmt.Sprintf("%s-bulk-%d", b.bucketId, arcCount)

	metrics := &Metrics{ArcCount: arcCount, Backend: b.config.Backend, EATType: b.config.EATType}

	// Create initial structure
	arcsMap := make(map[string]cid.Cid)
	keys := make(map[string]cid.Cid)

	for i := range arcCount {
		path := fmt.Sprintf("arc%d", i)
		target, _ := newPayloadCID([]byte(fmt.Sprintf("data%d", i)))
		arcsMap[path] = target
		keys[path] = target
	}

	start := time.Now()
	root, err := b.semantic.Commit(ctx, mapping.NewViewFrom(arcsMap))
	metrics.CommitTime = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("initial commit failed: %w", err)
	}

	// Store in EAT
	b.eat.Update(ctx, bucketID, root, cid.Undef, arcsMap)

	// Bulk update: update 10% of arcs at once
	bulkSize := max(1, arcCount/10)

	totalUpdateTime := time.Duration(0)
	metrics.UpdateTimes = make([]time.Duration, 0, b.config.UpdateRounds)
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

		// Commit updated arc set
		start = time.Now()
		newRoot, err := b.semantic.Commit(ctx, mapping.NewViewFrom(arcsMap))
		roundTime := time.Since(start)
		totalUpdateTime += roundTime
		metrics.UpdateTimes = append(metrics.UpdateTimes, roundTime)
		if err != nil {
			return nil, fmt.Errorf("bulk commit failed: %w", err)
		}

		// Store in EAT
		b.eat.Update(ctx, bucketID, newRoot, root, arcsMap)

		root = newRoot
	}

	metrics.UpdateTime = totalUpdateTime / time.Duration(b.config.UpdateRounds)

	// Measure proof
	testPath := paths[r.Intn(arcCount)]
	start = time.Now()
	snapshot, err := b.eat.Snapshot(ctx, bucketID, root)
	metrics.EATLookupTime = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("snapshot failed: %w", err)
	}
	start = time.Now()
	binding, proof, err := b.semantic.Prove(ctx, root, mapping.NewViewFrom(stringifyArcSet(snapshot)), arcset.CanonicalizePath(testPath))
	metrics.ProveTime = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("prove failed: %w", err)
	}
	if !binding.Present {
		return nil, fmt.Errorf("prove returned non-membership")
	}
	metrics.ProofSize = len(proof)
	metrics.RootSize = len(root.Bytes())

	target := keys[testPath]
	start = time.Now()
	valid, err := b.semantic.Verify(root, arcset.CanonicalizePath(testPath), mapping.Binding{Value: target, Present: true}, proof)
	metrics.VerifyTime = time.Since(start)
	if err != nil || !valid {
		return nil, fmt.Errorf("verify failed: %w", err)
	}

	// End-to-end latency
	metrics.EndToEndLatency = metrics.EATLookupTime + metrics.ProveTime + metrics.VerifyTime

	metrics.RewriteAmp = float64(bulkSize) // Each bulk update touches bulkSize arcs

	return metrics, nil
}

// PrintResults prints benchmark results in a formatted table.
func PrintResults(results map[int]*Metrics, workload string) {
	fmt.Printf("\n=== %s Benchmark Results ===\n", workload)
	fmt.Println("Backend  | EATType    | ArcCount | Commit(ms) | Update(ms) | EATLookup(ms) | Prove(ms) | Verify(ms) | EndToEnd(ms) | ProofSize | RewriteAmp")
	fmt.Println("---------|------------|----------|------------|------------|---------------|-----------|------------|--------------|-----------|------------")

	// Sort by arcCount for consistent output
	type entry struct {
		arcCount int
		m        *Metrics
	}
	entries := make([]entry, 0, len(results))
	for ac, m := range results {
		entries = append(entries, entry{ac, m})
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].arcCount < entries[j].arcCount
	})

	for _, e := range entries {
		fmt.Printf("%-8s | %-10s | %8d | %10.2f | %10.2f | %13.2f | %9.2f | %10.2f | %12.2f | %9d | %10.2f\n",
			e.m.Backend,
			e.m.EATType,
			e.arcCount,
			float64(e.m.CommitTime.Milliseconds()),
			float64(e.m.UpdateTime.Milliseconds()),
			float64(e.m.EATLookupTime.Milliseconds()),
			float64(e.m.ProveTime.Milliseconds()),
			float64(e.m.VerifyTime.Milliseconds()),
			float64(e.m.EndToEndLatency.Milliseconds()),
			e.m.ProofSize,
			e.m.RewriteAmp)
	}
}

// RunAllBackends runs all benchmarks across all backends.
// Returns results grouped by backend: map[BackendType]map[ArcCount]*Metrics.
func (b *BenchmarkRunner) RunAllBackends(ctx context.Context, workload string) (map[BackendType]map[int]*Metrics, error) {
	allResults := make(map[BackendType]map[int]*Metrics)

	for _, backend := range AllBackends() {
		tc, err := NewTestComponents(backend, b.bucketId)
		if err != nil {
			return nil, fmt.Errorf("create components for %s: %w", backend, err)
		}

		cfg := *b.config
		cfg.Backend = backend
		if cfg.EATType == "" {
			cfg.EATType = EATOverwrite
		}
		runner := NewBenchmarkRunner(&cfg, tc.BucketID, tc.EAT, tc.Semantic, tc.CAS)

		var results map[int]*Metrics
		switch workload {
		case "append":
			results, err = runner.RunAppendBenchmark(ctx)
		case "random":
			results, err = runner.RunRandomBenchmark(ctx)
		case "bulk":
			results, err = runner.RunBulkBenchmark(ctx)
		default:
			return nil, fmt.Errorf("unknown workload: %s", workload)
		}
		if err != nil {
			return nil, fmt.Errorf("%s benchmark for %s: %w", workload, backend, err)
		}
		allResults[backend] = results
	}
	return allResults, nil
}

// PrintBackendComparison prints multi-backend comparison results.
func PrintBackendComparison(allResults map[BackendType]map[int]*Metrics, workload string) {
	fmt.Printf("\n=== %s Benchmark: Backend Comparison ===\n", workload)
	fmt.Println("Backend  | EATType    | ArcCount | Commit(ms) | Update(ms) | EATLookup(ms) | Prove(ms) | Verify(ms) | EndToEnd(ms) | ProofSize | RewriteAmp")
	fmt.Println("---------|------------|----------|------------|------------|---------------|-----------|------------|--------------|-----------|------------")

	for _, backend := range AllBackends() {
		results, ok := allResults[backend]
		if !ok {
			continue
		}
		// Sort by arcCount
		type entry struct {
			arcCount int
			m        *Metrics
		}
		entries := make([]entry, 0, len(results))
		for ac, m := range results {
			entries = append(entries, entry{ac, m})
		}
		sort.Slice(entries, func(i, j int) bool {
			return entries[i].arcCount < entries[j].arcCount
		})

		for _, e := range entries {
			fmt.Printf("%-8s | %-10s | %8d | %10.2f | %10.2f | %13.2f | %9.2f | %10.2f | %12.2f | %9d | %10.2f\n",
				e.m.Backend,
				e.m.EATType,
				e.arcCount,
				float64(e.m.CommitTime.Milliseconds()),
				float64(e.m.UpdateTime.Milliseconds()),
				float64(e.m.EATLookupTime.Milliseconds()),
				float64(e.m.ProveTime.Milliseconds()),
				float64(e.m.VerifyTime.Milliseconds()),
				float64(e.m.EndToEndLatency.Milliseconds()),
				e.m.ProofSize,
				e.m.RewriteAmp)
		}
	}
}

// ExportCSV writes all metrics to a CSV file for further analysis.
// Returns the number of rows written.
func ExportCSV(results map[int]*Metrics, workload string, backend BackendType, eatType EATType, path string) (int, error) {
	f, err := os.Create(path)
	if err != nil {
		return 0, fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	// Header
	header := []string{
		"workload", "backend", "eat_type", "arc_count",
		"commit_ms", "update_ms", "eat_lookup_ms", "prove_ms",
		"verify_ms", "end_to_end_ms", "proof_size", "root_size", "rewrite_amp",
	}
	if err := w.Write(header); err != nil {
		return 0, fmt.Errorf("write header: %w", err)
	}

	rows := 0
	for arcCount, m := range results {
		record := []string{
			workload,
			string(backend),
			string(eatType),
			strconv.Itoa(arcCount),
			strconv.FormatInt(m.CommitTime.Milliseconds(), 10),
			strconv.FormatInt(m.UpdateTime.Milliseconds(), 10),
			strconv.FormatInt(m.EATLookupTime.Microseconds(), 10),
			strconv.FormatInt(m.ProveTime.Microseconds(), 10),
			strconv.FormatInt(m.VerifyTime.Microseconds(), 10),
			strconv.FormatInt(m.EndToEndLatency.Microseconds(), 10),
			strconv.Itoa(m.ProofSize),
			strconv.Itoa(m.RootSize),
			strconv.FormatFloat(m.RewriteAmp, 'f', 4, 64),
		}
		if err := w.Write(record); err != nil {
			return rows, fmt.Errorf("write row: %w", err)
		}
		rows++
	}
	return rows, nil
}

// EvalConfig holds configuration for comprehensive evaluation.
type EvalConfig struct {
	// ArcCounts is the list of arc counts to test
	ArcCounts []int

	// UpdateRounds is the number of update rounds
	UpdateRounds int

	// RandomSeed for reproducibility
	RandomSeed int64

	// Backends to test (default: all)
	Backends []BackendType

	// EATTypes to test (default: all)
	EATTypes []EATType

	// Workloads to run (default: all)
	Workloads []string

	// CSVDir is the directory to write CSV results
	CSVDir string
}

// DefaultEvalConfig returns default evaluation configuration.
func DefaultEvalConfig() *EvalConfig {
	return &EvalConfig{
		ArcCounts:    []int{50, 100, 200},
		UpdateRounds: 100,
		RandomSeed:   42,
		Backends:     AllBackends(),
		EATTypes:     AllEATTypes(),
		Workloads:    []string{"append", "random", "bulk"},
	}
}

// EvalRunner runs comprehensive evaluation across all backends, EAT types, and workloads.
type EvalRunner struct {
	config   *EvalConfig
	bucketId string
}

// NewEvalRunner creates a new evaluation runner.
func NewEvalRunner(cfg *EvalConfig, bucketId string) *EvalRunner {
	if cfg == nil {
		cfg = DefaultEvalConfig()
	}
	if cfg.Backends == nil {
		cfg.Backends = AllBackends()
	}
	if cfg.EATTypes == nil {
		cfg.EATTypes = AllEATTypes()
	}
	if cfg.Workloads == nil {
		cfg.Workloads = []string{"append", "random", "bulk"}
	}
	return &EvalRunner{
		config:   cfg,
		bucketId: bucketId,
	}
}

// RunAll runs the full evaluation matrix: backends x EAT types x workloads x arc counts.
// Returns results as map[BackendType]map[EATType]map[workload]map[ArcCount]*Metrics.
func (e *EvalRunner) RunAll(ctx context.Context) (map[BackendType]map[EATType]map[string]map[int]*Metrics, error) {
	allResults := make(map[BackendType]map[EATType]map[string]map[int]*Metrics)

	totalConfigs := len(e.config.Backends) * len(e.config.EATTypes) * len(e.config.Workloads)
	current := 0

	for _, backend := range e.config.Backends {
		for _, eatType := range e.config.EATTypes {
			if _, ok := allResults[backend]; !ok {
				allResults[backend] = make(map[EATType]map[string]map[int]*Metrics)
			}
			if _, ok := allResults[backend][eatType]; !ok {
				allResults[backend][eatType] = make(map[string]map[int]*Metrics)
			}

			tc, err := NewTestComponentsWithEAT(backend, eatType, e.bucketId)
			if err != nil {
				return nil, fmt.Errorf("create components for %s/%s: %w", backend, eatType, err)
			}

			for _, workload := range e.config.Workloads {
				current++
				fmt.Printf("[%d/%d] Running %s / %s / %s (arcs: %v, rounds: %d)\n",
					current, totalConfigs, backend, eatType, workload,
					e.config.ArcCounts, e.config.UpdateRounds)

				cfg := &BenchmarkConfig{
					ArcCounts:    e.config.ArcCounts,
					UpdateRounds: e.config.UpdateRounds,
					RandomSeed:   e.config.RandomSeed,
					Backend:      backend,
					EATType:      eatType,
				}

				runner := NewBenchmarkRunner(cfg, tc.BucketID, tc.EAT, tc.Semantic, tc.CAS)

				var results map[int]*Metrics
				switch workload {
				case "append":
					results, err = runner.RunAppendBenchmark(ctx)
				case "random":
					results, err = runner.RunRandomBenchmark(ctx)
				case "bulk":
					results, err = runner.RunBulkBenchmark(ctx)
				default:
					return nil, fmt.Errorf("unknown workload: %s", workload)
				}
				if err != nil {
					return nil, fmt.Errorf("%s benchmark for %s/%s: %w", workload, backend, eatType, err)
				}

				allResults[backend][eatType][workload] = results

				// Write CSV if directory specified
				if e.config.CSVDir != "" {
					filename := fmt.Sprintf("%s/%s_%s_%s.csv", e.config.CSVDir, backend, eatType, workload)
					rows, err := ExportCSV(results, workload, backend, eatType, filename)
					if err != nil {
						fmt.Printf("  Warning: failed to export CSV for %s/%s/%s: %v\n", backend, eatType, workload, err)
					} else {
						fmt.Printf("  CSV: %s (%d rows)\n", filename, rows)
					}
				}
			}
		}
	}

	return allResults, nil
}

// PrintFullResults prints the complete evaluation results.
func PrintFullResults(allResults map[BackendType]map[EATType]map[string]map[int]*Metrics) {
	fmt.Println("\n" + "=" + strings.Repeat("=", 79))
	fmt.Println("  FULL EVALUATION RESULTS")
	fmt.Println("=" + strings.Repeat("=", 79))

	for _, backend := range AllBackends() {
		backendResults, ok := allResults[backend]
		if !ok {
			continue
		}

		fmt.Printf("\n%s Backend %s%s\n", strings.Repeat("=", 40), backend, strings.Repeat("=", 40))

		for _, eatType := range AllEATTypes() {
			eatResults, ok := backendResults[eatType]
			if !ok {
				continue
			}

			fmt.Printf("\n  EAT: %s\n", eatType)

			for _, workload := range []string{"append", "random", "bulk"} {
				results, ok := eatResults[workload]
				if !ok {
					continue
				}

				PrintResults(results, fmt.Sprintf("%s %s/%s", workload, backend, eatType))
			}
		}
	}
}

// DurationStats holds summary statistics for a set of durations.
type DurationStats struct {
	Avg    time.Duration `json:"avg_ms"`
	Min    time.Duration `json:"min_ms"`
	Max    time.Duration `json:"max_ms"`
	StdDev time.Duration `json:"stddev_ms"`
	Median time.Duration `json:"median_ms"`
	P95    time.Duration `json:"p95_ms"`
}

// computeStats computes summary statistics from a slice of durations.
func computeStats(durations []time.Duration) DurationStats {
	if len(durations) == 0 {
		return DurationStats{}
	}

	n := len(durations)
	var sum time.Duration
	for _, d := range durations {
		sum += d
	}
	avg := sum / time.Duration(n)

	// Min, max
	minVal, maxVal := durations[0], durations[0]
	for _, d := range durations {
		if d < minVal {
			minVal = d
		}
		if d > maxVal {
			maxVal = d
		}
	}

	// Std dev
	varSum := int64(0)
	for _, d := range durations {
		diff := d.Nanoseconds() - avg.Nanoseconds()
		varSum += diff * diff
	}
	stddev := time.Duration(int64(float64(varSum) / float64(n)))

	// Sort for median and percentiles
	sorted := make([]time.Duration, n)
	copy(sorted, durations)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	median := sorted[n/2]
	p95Idx := int(float64(n) * 0.95)
	if p95Idx >= n {
		p95Idx = n - 1
	}

	return DurationStats{
		Avg:    avg,
		Min:    minVal,
		Max:    maxVal,
		StdDev: stddev,
		Median: median,
		P95:    sorted[p95Idx],
	}
}

// MetricsSummary holds the summary statistics for all timing metrics.
type MetricsSummary struct {
	Backend    BackendType   `json:"backend"`
	EATType    EATType       `json:"eat_type"`
	ArcCount   int           `json:"arc_count"`
	Workload   string        `json:"workload"`
	Commit     DurationStats `json:"commit"`
	Update     DurationStats `json:"update"`
	EATLookup  DurationStats `json:"eat_lookup"`
	Prove      DurationStats `json:"prove"`
	Verify     DurationStats `json:"verify"`
	EndToEnd   DurationStats `json:"end_to_end"`
	ProofSize  int           `json:"proof_size"`
	RootSize   int           `json:"root_size"`
	RewriteAmp float64       `json:"rewrite_amp"`
}

// ComputeSummaryStats computes summary statistics from metrics.
func ComputeSummaryStats(m *Metrics) *MetricsSummary {
	s := &MetricsSummary{
		Backend:    m.Backend,
		EATType:    m.EATType,
		ArcCount:   m.ArcCount,
		ProofSize:  m.ProofSize,
		RootSize:   m.RootSize,
		RewriteAmp: m.RewriteAmp,
	}

	if len(m.UpdateTimes) > 0 {
		s.Update = computeStats(m.UpdateTimes)
	}
	if len(m.ProveTimes) > 0 {
		s.Prove = computeStats(m.ProveTimes)
	}
	if len(m.VerifyTimes) > 0 {
		s.Verify = computeStats(m.VerifyTimes)
	}

	// Single-point metrics (commit, prove, verify, lookup, end-to-end from final proof)
	s.Commit = DurationStats{Avg: m.CommitTime, Min: m.CommitTime, Max: m.CommitTime}
	s.EATLookup = DurationStats{Avg: m.EATLookupTime, Min: m.EATLookupTime, Max: m.EATLookupTime}
	s.Prove.Avg = max(s.Prove.Avg, m.ProveTime)
	s.Prove.Min = min(s.Prove.Min, m.ProveTime)
	s.Prove.Max = max(s.Prove.Max, m.ProveTime)
	s.Verify.Avg = max(s.Verify.Avg, m.VerifyTime)
	s.Verify.Min = min(s.Verify.Min, m.VerifyTime)
	s.Verify.Max = max(s.Verify.Max, m.VerifyTime)
	s.EndToEnd = DurationStats{Avg: m.EndToEndLatency, Min: m.EndToEndLatency, Max: m.EndToEndLatency}

	return s
}

// ExportJSON writes all results to a JSON file.
func ExportJSON(allResults map[BackendType]map[EATType]map[string]map[int]*Metrics, path string) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	type resultEntry struct {
		Backend  BackendType     `json:"backend"`
		EATType  EATType         `json:"eat_type"`
		Workload string          `json:"workload"`
		ArcCount int             `json:"arc_count"`
		Metrics  *MetricsSummary `json:"metrics"`
	}

	var results []resultEntry
	for backend, eatMap := range allResults {
		for eatType, workMap := range eatMap {
			for workload, arcMap := range workMap {
				for arcCount, m := range arcMap {
					results = append(results, resultEntry{
						Backend:  backend,
						EATType:  eatType,
						Workload: workload,
						ArcCount: arcCount,
						Metrics:  ComputeSummaryStats(m),
					})
				}
			}
		}
	}

	// Sort for consistent output
	sort.Slice(results, func(i, j int) bool {
		if results[i].Backend != results[j].Backend {
			return results[i].Backend < results[j].Backend
		}
		if results[i].EATType != results[j].EATType {
			return results[i].EATType < results[j].EATType
		}
		if results[i].Workload != results[j].Workload {
			return results[i].Workload < results[j].Workload
		}
		return results[i].ArcCount < results[j].ArcCount
	})

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	return enc.Encode(results)
}

// PrintSummaryStats prints summary statistics in a compact table.
func PrintSummaryStats(allResults map[BackendType]map[EATType]map[string]map[int]*Metrics) {
	fmt.Println("\n=== Summary Statistics (Update Time Distribution) ===")
	fmt.Println("Backend  | EATType    | Workload | ArcCount | Avg(ms) | P95(ms) | Median(ms) | Min(ms) | Max(ms)")
	fmt.Println("---------|------------|----------|----------|---------|---------|------------|---------|--------")

	type entry struct {
		backend  BackendType
		eatType  EATType
		workload string
		arcCount int
		stats    DurationStats
	}
	var entries []entry

	for _, backend := range AllBackends() {
		eatMap, ok := allResults[backend]
		if !ok {
			continue
		}
		for _, eatType := range AllEATTypes() {
			workMap, ok := eatMap[eatType]
			if !ok {
				continue
			}
			for workload, arcMap := range workMap {
				for arcCount, m := range arcMap {
					if len(m.UpdateTimes) > 0 {
						entries = append(entries, entry{
							backend:  backend,
							eatType:  eatType,
							workload: workload,
							arcCount: arcCount,
							stats:    computeStats(m.UpdateTimes),
						})
					}
				}
			}
		}
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].backend != entries[j].backend {
			return entries[i].backend < entries[j].backend
		}
		if entries[i].workload != entries[j].workload {
			return entries[i].workload < entries[j].workload
		}
		return entries[i].arcCount < entries[j].arcCount
	})

	for _, e := range entries {
		fmt.Printf("%-8s | %-10s | %-8s | %8d | %7.2f | %7.2f | %10.2f | %7.2f | %6.2f\n",
			e.backend, e.eatType, e.workload, e.arcCount,
			float64(e.stats.Avg.Milliseconds()),
			float64(e.stats.P95.Milliseconds()),
			float64(e.stats.Median.Milliseconds()),
			float64(e.stats.Min.Milliseconds()),
			float64(e.stats.Max.Milliseconds()))
	}
}

// GenerateLatexTable generates a LaTeX table for the paper.
func GenerateLatexTable(allResults map[BackendType]map[EATType]map[string]map[int]*Metrics, workload string) string {
	var sb strings.Builder
	sb.WriteString("\\begin{table}[htbp]\n")
	sb.WriteString("\\centering\n")
	sb.WriteString("\\caption{MALT " + workload + " benchmark results}\n")
	sb.WriteString("\\label{tab:" + workload + "}\n")
	sb.WriteString("\\begin{tabular}{lrrrrrr}\n")
	sb.WriteString("\\toprule\n")
	sb.WriteString("Backend & EAT & Arcs & Update (ms) & Prove (ms) & Verify (ms) & Proof Size (B) \\\\\n")
	sb.WriteString("\\midrule\n")

	for _, backend := range AllBackends() {
		eatMap, ok := allResults[backend]
		if !ok {
			continue
		}
		for _, eatType := range AllEATTypes() {
			workMap, ok := eatMap[eatType]
			if !ok {
				continue
			}
			results, ok := workMap[workload]
			if !ok {
				continue
			}
			for arcCount, m := range results {
				sb.WriteString(fmt.Sprintf("%s & %s & %d & %.2f & %.2f & %.2f & %d \\\\\n",
					backend, eatType, arcCount,
					float64(m.UpdateTime.Microseconds())/1000.0,
					float64(m.ProveTime.Microseconds())/1000.0,
					float64(m.VerifyTime.Microseconds())/1000.0,
					m.ProofSize))
			}
		}
	}

	sb.WriteString("\\bottomrule\n")
	sb.WriteString("\\end{tabular}\n")
	sb.WriteString("\\end{table}\n")
	return sb.String()
}

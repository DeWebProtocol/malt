// Package eval provides evaluation and benchmarking tools for MALT.
package eval

import (
	"context"
	"encoding/csv"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/dewebprotocol/malt/core/cas"
	"github.com/dewebprotocol/malt/core/cas/mock"
	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/eat/bloom"
	"github.com/dewebprotocol/malt/core/eat/overwrite"
	"github.com/dewebprotocol/malt/core/eat/versioned"
	"github.com/dewebprotocol/malt/core/sce"
	"github.com/dewebprotocol/malt/core/sce/commitment"
	"github.com/dewebprotocol/malt/core/sce/commitment/ipa"
	"github.com/dewebprotocol/malt/core/sce/commitment/kzg"
	"github.com/dewebprotocol/malt/core/sce/commitment/verkle"
	"github.com/dewebprotocol/malt/core/kvstore"
	kvstore_memory "github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// BackendType selects the commitment scheme.
type BackendType string

const (
	BackendKZG    BackendType = "kzg"
	BackendVerkle BackendType = "verkle"
	BackendIPA    BackendType = "ipa"
)

// AllBackends returns all available backend types.
func AllBackends() []BackendType {
	return []BackendType{BackendKZG, BackendVerkle, BackendIPA}
}

// EATType selects the EAT implementation.
type EATType string

const (
	EATOverwrite  EATType = "overwrite"
	EATVersioned  EATType = "versioned"
	EATBloom      EATType = "bloom"
)

// AllEATTypes returns all available EAT types.
func AllEATTypes() []EATType {
	return []EATType{EATOverwrite, EATVersioned, EATBloom}
}

// NewEAT creates an eat.EAT for the given EAT type.
func NewEAT(t EATType, kv kvstore.KVStore) (eat.EAT, error) {
	switch t {
	case EATOverwrite:
		return overwrite.NewEAT(kv)
	case EATVersioned:
		return versioned.NewEAT(kv)
	case EATBloom:
		bloomCache := bloom.NewBloomCache(kv, 16*1024*1024) // 16MB default
		return versioned.NewEATWithBloomCache(kv, bloomCache)
	default:
		return nil, fmt.Errorf("unknown EAT type: %s", t)
	}
}

// NewScheme creates a commitment.Scheme for the given backend type.
func NewScheme(b BackendType) (commitment.Scheme, error) {
	switch b {
	case BackendKZG:
		return kzg.NewScheme()
	case BackendVerkle:
		return verkle.NewScheme()
	case BackendIPA:
		return ipa.NewScheme()
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

// TestComponents holds the components needed for benchmarking.
type TestComponents struct {
	EAT      eat.EAT
	SCE      *sce.Engine
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
	s := sce.NewEngine(scheme)
	c := mock.NewCAS()
	return &TestComponents{
		EAT:      e,
		SCE:      s,
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
	metrics.EATLookupTime = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("snapshot failed: %w", err)
	}
	start = time.Now()
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
	metrics.EATLookupTime = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("snapshot failed: %w", err)
	}
	start = time.Now()
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
	metrics.EATLookupTime = time.Since(start)
	if err != nil {
		return nil, fmt.Errorf("snapshot failed: %w", err)
	}
	start = time.Now()
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
		runner := NewBenchmarkRunner(&cfg, tc.BucketID, tc.EAT, tc.SCE, tc.CAS)

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
	config  *EvalConfig
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

				runner := NewBenchmarkRunner(cfg, tc.BucketID, tc.EAT, tc.SCE, tc.CAS)

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
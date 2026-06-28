// Package readbench provides read benchmark runners for MALT and IPLD UnixFS baselines.
package readbench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"
	"sync/atomic"
	"time"

	httpapi "github.com/dewebprotocol/malt/api/http"
	"github.com/dewebprotocol/malt/auth/proof/prooflist"
	"github.com/dewebprotocol/malt/runtime/metrics"
	daemonclient "github.com/dewebprotocol/malt/sdk/client"
)

const (
	// DefaultFixtureName is the prefix used for generated benchmark fixture fixtures.
	DefaultFixtureName    = "readbench"
	DefaultDirectoryDepth = 2
	DefaultSmallFileBytes = 64
	DefaultLargeFileBytes = 300 * 1024
	DefaultRangeHeader    = "bytes=0-4095"
	DefaultIterations     = 1

	minListBackedFileBytes = 256*1024 + 1
)

var generatedFixtureCounter atomic.Uint64

// OperationKind identifies the measured read operation.
type OperationKind string

const (
	OperationResolvePath  OperationKind = "resolve_path"
	OperationContentFull  OperationKind = "content_full"
	OperationContentRange OperationKind = "content_range"
)

// WorkloadKind identifies the paper-facing read scenario measured by a record.
type WorkloadKind string

const (
	WorkloadDeepPathLookup     WorkloadKind = "deep_path_lookup"
	WorkloadSmallFileRead      WorkloadKind = "small_file_read"
	WorkloadLargeFileRangeRead WorkloadKind = "large_file_range_read"
)

// FixtureConfig controls the deterministic fixture fixture.
type FixtureConfig struct {
	FixtureName    string
	DirectoryDepth int
	SmallFileBytes int
	LargeFileBytes int
	// Arcs is passed to CreateRootStructure. It must be non-empty and include
	// a valid @payload CID that already exists in the daemon's CAS.
	Arcs map[string]string
}

// RunConfig controls one JSONL benchmark run.
type RunConfig struct {
	Systems     []SystemName
	Fixture     FixtureConfig
	RangeHeader string
	Iterations  int
}

// Fixture describes the deterministic read benchmark dataset.
type Fixture struct {
	FixtureName string
	SmallPath   string
	LargePath   string
	Root        string
}

// Result is one benchmark JSONL record.
type Result struct {
	System              SystemName            `json:"system"`
	OperationKind       OperationKind         `json:"operation_kind"`
	Workload            WorkloadKind          `json:"workload"`
	Iteration           int                   `json:"iteration"`
	FixtureName         string                `json:"fixture"`
	DatasetName         string                `json:"dataset,omitempty"`
	FileCount           int                   `json:"file_count,omitempty"`
	DirectoryCount      int                   `json:"directory_count,omitempty"`
	PathCount           int                   `json:"path_count,omitempty"`
	PathDepth           int                   `json:"path_depth,omitempty"`
	PathSample          int                   `json:"path_sample,omitempty"`
	LogicalPayloadBytes int64                 `json:"logical_payload_bytes,omitempty"`
	SmallFileBytes      int                   `json:"small_file_bytes,omitempty"`
	LargeFileBytes      int                   `json:"large_file_bytes,omitempty"`
	CASLatencyMS        int                   `json:"cas_latency_ms"`
	Path                string                `json:"path"`
	RangeHeader         string                `json:"range_header,omitempty"`
	ElapsedNS           int64                 `json:"elapsed_ns"`
	VerifyElapsedNS     *int64                `json:"verify_elapsed_ns,omitempty"`
	ContentBytes        *int                  `json:"content_bytes,omitempty"`
	ProofListStepCount  int                   `json:"prooflist_step_count"`
	EvidenceItemCount   int                   `json:"evidence_item_count"`
	Target              string                `json:"target,omitempty"`
	CAS                 metrics.CASStats      `json:"cas"`
	ArcTable            metrics.ArcTableStats `json:"arctable"`
	Proof               metrics.ProofStats    `json:"proof"`
}

type operation struct {
	kind        OperationKind
	workload    WorkloadKind
	path        string
	rangeHeader string
}

// Runner drives fixture setup and measured daemon reads.
type Runner struct {
	client *daemonclient.Client
	root   string
}

// NewRunner creates a benchmark runner for a daemon API v1 base URL.
func NewRunner(baseURL string) *Runner {
	trimmed := strings.TrimRight(baseURL, "/")
	return &Runner{
		client: daemonclient.NewWithBaseURL(trimmed),
	}
}

// PrepareFixture creates a deterministic MALT read fixture under an explicit root.
func (r *Runner) PrepareFixture(ctx context.Context, cfg FixtureConfig) (*Fixture, error) {
	if r == nil || r.client == nil {
		return nil, fmt.Errorf("read benchmark runner is nil")
	}
	normalized, err := normalizeFixtureConfig(cfg)
	if err != nil {
		return nil, err
	}
	data := newFixtureData(normalized)

	if len(cfg.Arcs) == 0 {
		return nil, fmt.Errorf("create root structure: Arcs is required in FixtureConfig")
	}
	rootResp, err := r.client.CreateRootStructure(ctx, cfg.Arcs)
	if err != nil {
		return nil, fmt.Errorf("create root structure: %w", err)
	}
	r.root = rootResp.Root

	if writeResp, err := r.client.AddUnixFSFileWithLegacyMigration(ctx, r.root, data.smallPath, data.smallData); err != nil {
		return nil, fmt.Errorf("write small fixture: %w", err)
	} else {
		r.root = writeResp.NewRoot
	}
	if writeResp, err := r.client.AddUnixFSFile(ctx, r.root, data.largePath, data.largeData); err != nil {
		return nil, fmt.Errorf("write large fixture: %w", err)
	} else {
		r.root = writeResp.NewRoot
	}

	return &Fixture{
		FixtureName: data.fixtureName,
		SmallPath:   data.smallPath,
		LargePath:   data.largePath,
		Root:        r.root,
	}, nil
}

// RunJSONL prepares the fixture, runs the measured reads, and writes one JSON
// object per operation.
func (r *Runner) RunJSONL(ctx context.Context, cfg RunConfig, w io.Writer) error {
	if w == nil {
		return fmt.Errorf("output writer is nil")
	}
	normalized, err := normalizeRunConfig(cfg)
	if err != nil {
		return err
	}
	if normalized.Iterations == 0 {
		return nil
	}

	data := newFixtureData(normalized.Fixture)
	fixture := &Fixture{
		FixtureName: data.fixtureName,
		SmallPath:   data.smallPath,
		LargePath:   data.largePath,
	}
	baselines := make(map[SystemName]*BaselineSystem)
	for _, system := range normalized.Systems {
		switch system {
		case SystemMALTFlat:
			if fixture.Root == "" {
				prepared, err := r.PrepareFixture(ctx, normalized.Fixture)
				if err != nil {
					return err
				}
				fixture = prepared
			}
		case SystemMerkleDAG, SystemHAMT:
			if _, ok := baselines[system]; ok {
				continue
			}
			baseline, err := newBaselineSystem(ctx, system, data)
			if err != nil {
				return err
			}
			baselines[system] = baseline
		default:
			return fmt.Errorf("unknown system %q", system)
		}
	}

	ops := []operation{
		{kind: OperationResolvePath, workload: WorkloadDeepPathLookup, path: fixture.SmallPath},
		{kind: OperationContentFull, workload: WorkloadSmallFileRead, path: fixture.SmallPath},
		{kind: OperationContentRange, workload: WorkloadLargeFileRangeRead, path: fixture.LargePath, rangeHeader: normalized.RangeHeader},
	}

	enc := json.NewEncoder(w)
	for iteration := 0; iteration < normalized.Iterations; iteration++ {
		for _, system := range normalized.Systems {
			for _, op := range ops {
				var (
					result *Result
					err    error
				)
				switch system {
				case SystemMALTFlat:
					result, err = r.measureOperation(ctx, iteration, fixture.FixtureName, op)
				case SystemMerkleDAG, SystemHAMT:
					result, err = baselines[system].measureOperation(ctx, iteration, fixture.FixtureName, op)
				default:
					return fmt.Errorf("unknown system %q", system)
				}
				if err != nil {
					return err
				}
				if err := enc.Encode(result); err != nil {
					return fmt.Errorf("write JSONL result: %w", err)
				}
			}
		}
	}
	return nil
}

func (r *Runner) measureOperation(ctx context.Context, iteration int, fixture string, op operation) (*Result, error) {
	if err := r.resetMetrics(ctx); err != nil {
		return nil, fmt.Errorf("reset metrics before %s: %w", op.kind, err)
	}

	start := time.Now()
	var (
		target          string
		contentBytes    *int
		stepCount       int
		pl              *prooflist.ProofList
		verifyElapsedNS *int64
	)
	switch op.kind {
	case OperationResolvePath:
		resp, err := r.client.ResolveRoot(ctx, r.root, op.path)
		if err != nil {
			return nil, fmt.Errorf("resolve path %q: %w", op.path, err)
		}
		if resp.ProofList == nil {
			return nil, fmt.Errorf("resolve path %q returned no prooflist", op.path)
		}
		target = resp.Target
		pl = resp.ProofList
		stepCount = len(pl.Steps)
	case OperationContentFull, OperationContentRange:
		content, _, headers, err := r.client.GetContent(ctx, r.root, op.path, op.rangeHeader)
		if err != nil {
			return nil, fmt.Errorf("%s read %q: %w", op.kind, op.path, err)
		}
		pl, err = daemonclient.ProofListFromHeaders(headers)
		if err != nil {
			return nil, fmt.Errorf("%s prooflist %q: %w", op.kind, op.path, err)
		}
		count := len(content)
		contentBytes = &count
		stepCount = len(pl.Steps)
	default:
		return nil, fmt.Errorf("unsupported operation kind %q", op.kind)
	}
	elapsed := positiveElapsedNS(start, time.Now())

	snapshot, err := r.metricsSnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshot metrics after %s: %w", op.kind, err)
	}
	verifyElapsedNS, err = r.verifyProofList(ctx, op, pl)
	if err != nil {
		return nil, err
	}

	return &Result{
		System:             SystemMALTFlat,
		OperationKind:      op.kind,
		Workload:           op.workload,
		Iteration:          iteration,
		FixtureName:        fixture,
		Path:               op.path,
		RangeHeader:        op.rangeHeader,
		ElapsedNS:          elapsed,
		VerifyElapsedNS:    verifyElapsedNS,
		ContentBytes:       contentBytes,
		ProofListStepCount: stepCount,
		EvidenceItemCount:  stepCount,
		Target:             target,
		CAS:                snapshot.CAS,
		ArcTable:           snapshot.ArcTable,
		Proof:              snapshot.Proof,
	}, nil
}

func (r *Runner) verifyProofList(ctx context.Context, op operation, pl *prooflist.ProofList) (*int64, error) {
	if pl == nil {
		return nil, fmt.Errorf("%s prooflist is nil", op.kind)
	}
	start := time.Now()
	resp, err := r.client.Verify(ctx, &httpapi.VerifyRequest{ProofList: *pl})
	elapsed := positiveElapsedNS(start, time.Now())
	if err != nil {
		return nil, fmt.Errorf("%s verify prooflist %q: %w", op.kind, op.path, err)
	}
	if !resp.Valid {
		return nil, fmt.Errorf("%s prooflist %q did not verify", op.kind, op.path)
	}
	return &elapsed, nil
}

func positiveElapsedNS(start, end time.Time) int64 {
	elapsed := end.Sub(start).Nanoseconds()
	if elapsed <= 0 {
		return 1
	}
	return elapsed
}

func (r *Runner) resetMetrics(ctx context.Context) error {
	if r == nil || r.client == nil {
		return fmt.Errorf("read benchmark runner is nil")
	}
	_, err := r.client.ResetMetrics(ctx)
	return err
}

func (r *Runner) metricsSnapshot(ctx context.Context) (metrics.Snapshot, error) {
	var zero metrics.Snapshot
	if r == nil || r.client == nil {
		return zero, fmt.Errorf("read benchmark runner is nil")
	}
	resp, err := r.client.MetricsSnapshot(ctx)
	if err != nil {
		return zero, err
	}
	return metrics.Snapshot{
		CAS: metrics.CASStats{
			PutCount: resp.Snapshot.CAS.PutCount,
			GetCount: resp.Snapshot.CAS.GetCount,
			HasCount: resp.Snapshot.CAS.HasCount,
			BytesPut: resp.Snapshot.CAS.BytesPut,
			BytesGet: resp.Snapshot.CAS.BytesGet,
		},
		ArcTable: metrics.ArcTableStats{
			GetCount:          resp.Snapshot.ArcTable.GetCount,
			BatchGetCount:     resp.Snapshot.ArcTable.BatchGetCount,
			BatchGetPathCount: resp.Snapshot.ArcTable.BatchGetPathCount,
			UpdateCount:       resp.Snapshot.ArcTable.UpdateCount,
			UpdateArcCount:    resp.Snapshot.ArcTable.UpdateArcCount,
			SnapshotCount:     resp.Snapshot.ArcTable.SnapshotCount,
			SnapshotArcCount:  resp.Snapshot.ArcTable.SnapshotArcCount,
			IterateCount:      resp.Snapshot.ArcTable.IterateCount,
		},
		Proof: metrics.ProofStats{
			ProofListCount: resp.Snapshot.Proof.ProofListCount,
			StepCount:      resp.Snapshot.Proof.StepCount,
			EvidenceBytes:  resp.Snapshot.Proof.EvidenceBytes,
			ProofBytes:     resp.Snapshot.Proof.ProofBytes,
			TotalBytes:     resp.Snapshot.Proof.TotalBytes,
		},
	}, nil
}

func normalizeRunConfig(cfg RunConfig) (RunConfig, error) {
	systems, err := normalizeSystems(cfg.Systems)
	if err != nil {
		return RunConfig{}, err
	}
	for _, system := range systems {
		if system == SystemFlatHAMT {
			return RunConfig{}, fmt.Errorf("system %q is only supported by read_matrix", system)
		}
	}
	fixture, err := normalizeFixtureConfig(cfg.Fixture)
	if err != nil {
		return RunConfig{}, err
	}
	if cfg.Iterations < 0 {
		return RunConfig{}, fmt.Errorf("iterations must be non-negative")
	}
	if strings.TrimSpace(cfg.RangeHeader) == "" {
		cfg.RangeHeader = DefaultRangeHeader
	}
	cfg.Systems = systems
	cfg.Fixture = fixture
	return cfg, nil
}

func normalizeFixtureConfig(cfg FixtureConfig) (FixtureConfig, error) {
	if fixture := strings.TrimSpace(cfg.FixtureName); fixture == "" {
		cfg.FixtureName = defaultFixtureFixtureName()
	} else {
		cfg.FixtureName = fixture
	}
	if cfg.DirectoryDepth < 0 {
		return FixtureConfig{}, fmt.Errorf("directory depth must be non-negative")
	}
	if cfg.SmallFileBytes == 0 {
		cfg.SmallFileBytes = DefaultSmallFileBytes
	}
	if cfg.SmallFileBytes < 0 {
		return FixtureConfig{}, fmt.Errorf("small file bytes must be non-negative")
	}
	if cfg.LargeFileBytes == 0 {
		cfg.LargeFileBytes = DefaultLargeFileBytes
	}
	if cfg.LargeFileBytes < minListBackedFileBytes {
		return FixtureConfig{}, fmt.Errorf("large file bytes must be at least %d for list-backed storage", minListBackedFileBytes)
	}
	return cfg, nil
}

func defaultFixtureFixtureName() string {
	return fmt.Sprintf("%s-%d-%d", DefaultFixtureName, time.Now().UTC().UnixNano(), generatedFixtureCounter.Add(1))
}

func fixturePath(depth int, filename string) string {
	if depth <= 0 {
		return filename
	}
	parts := make([]string, 0, depth+1)
	for i := 0; i < depth; i++ {
		parts = append(parts, fmt.Sprintf("dir%02d", i))
	}
	parts = append(parts, filename)
	return path.Join(parts...)
}

func deterministicBytes(label string, size int) []byte {
	out := make([]byte, size)
	if size == 0 {
		return out
	}
	seed := []byte(label)
	for i := range out {
		out[i] = byte('a' + ((int(seed[i%len(seed)]) + i) % 26))
	}
	return out
}

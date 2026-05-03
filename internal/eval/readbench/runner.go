// Package readbench provides a MALT-only read benchmark runner.
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

	daemonclient "github.com/dewebprotocol/malt/client"
	"github.com/dewebprotocol/malt/core/metrics"
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
	OperationProofListPath OperationKind = "prooflist_path"
	OperationContentRange  OperationKind = "content_range"
)

// FixtureConfig controls the deterministic fixture fixture.
type FixtureConfig struct {
	FixtureName    string
	DirectoryDepth int
	SmallFileBytes int
	LargeFileBytes int
}

// RunConfig controls one JSONL benchmark run.
type RunConfig struct {
	Fixture     FixtureConfig
	RangeHeader string
	Iterations  int
}

// Fixture describes the deterministic MALT-only dataset.
type Fixture struct {
	FixtureName string
	SmallPath   string
	LargePath   string
}

// Result is one benchmark JSONL record.
type Result struct {
	OperationKind      OperationKind         `json:"operation_kind"`
	Iteration          int                   `json:"iteration"`
	FixtureName        string                `json:"fixture"`
	Path               string                `json:"path"`
	RangeHeader        string                `json:"range_header,omitempty"`
	ElapsedNS          int64                 `json:"elapsed_ns"`
	ContentBytes       *int                  `json:"content_bytes,omitempty"`
	ProofListStepCount int                   `json:"prooflist_step_count"`
	Target             string                `json:"target,omitempty"`
	CAS                metrics.CASStats      `json:"cas"`
	ArcTable           metrics.ArcTableStats `json:"arctable"`
	Proof              metrics.ProofStats    `json:"proof"`
}

type operation struct {
	kind        OperationKind
	path        string
	rangeHeader string
}

// Runner drives fixture setup and measured daemon reads.
type Runner struct {
	client *daemonclient.Client
}

// NewRunner creates a benchmark runner for a daemon API v1 base URL.
func NewRunner(baseURL string) *Runner {
	trimmed := strings.TrimRight(baseURL, "/")
	return &Runner{
		client: daemonclient.NewWithBaseURL(trimmed),
	}
}

// PrepareFixture creates a deterministic MALT-only read fixture under the current root.
func (r *Runner) PrepareFixture(ctx context.Context, cfg FixtureConfig) (*Fixture, error) {
	if r == nil || r.client == nil {
		return nil, fmt.Errorf("read benchmark runner is nil")
	}
	normalized, err := normalizeFixtureConfig(cfg)
	if err != nil {
		return nil, err
	}

	smallPath := fixturePath(normalized.DirectoryDepth, "small.txt")
	largePath := fixturePath(normalized.DirectoryDepth, "large.bin")
	if _, err := r.client.AddCurrentUnixFSFile(ctx, smallPath, deterministicBytes("small", normalized.SmallFileBytes)); err != nil {
		return nil, fmt.Errorf("write small fixture: %w", err)
	}
	if _, err := r.client.AddCurrentUnixFSFile(ctx, largePath, deterministicBytes("large", normalized.LargeFileBytes)); err != nil {
		return nil, fmt.Errorf("write large fixture: %w", err)
	}

	return &Fixture{
		FixtureName: normalized.FixtureName,
		SmallPath:   smallPath,
		LargePath:   largePath,
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

	fixture, err := r.PrepareFixture(ctx, normalized.Fixture)
	if err != nil {
		return err
	}

	ops := []operation{
		{kind: OperationProofListPath, path: fixture.SmallPath},
		{kind: OperationContentRange, path: fixture.LargePath, rangeHeader: normalized.RangeHeader},
	}

	enc := json.NewEncoder(w)
	for iteration := 0; iteration < normalized.Iterations; iteration++ {
		for _, op := range ops {
			result, err := r.measureOperation(ctx, iteration, fixture.FixtureName, op)
			if err != nil {
				return err
			}
			if err := enc.Encode(result); err != nil {
				return fmt.Errorf("write JSONL result: %w", err)
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
		target       string
		contentBytes *int
		stepCount    int
	)
	switch op.kind {
	case OperationProofListPath:
		resp, err := r.client.ProofListCurrent(ctx, op.path)
		if err != nil {
			return nil, fmt.Errorf("prooflist read %q: %w", op.path, err)
		}
		target = resp.Target
		stepCount = len(resp.ProofList.Steps)
	case OperationContentRange:
		resp, err := r.client.GetCurrentContentProof(ctx, op.path, op.rangeHeader)
		if err != nil {
			return nil, fmt.Errorf("content range read %q: %w", op.path, err)
		}
		count := len(resp.Content)
		contentBytes = &count
		target = resp.Key
		stepCount = len(resp.ProofList.Steps)
	default:
		return nil, fmt.Errorf("unsupported operation kind %q", op.kind)
	}
	elapsed := time.Since(start).Nanoseconds()

	snapshot, err := r.metricsSnapshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("snapshot metrics after %s: %w", op.kind, err)
	}

	return &Result{
		OperationKind:      op.kind,
		Iteration:          iteration,
		FixtureName:        fixture,
		Path:               op.path,
		RangeHeader:        op.rangeHeader,
		ElapsedNS:          elapsed,
		ContentBytes:       contentBytes,
		ProofListStepCount: stepCount,
		Target:             target,
		CAS:                snapshot.CAS,
		ArcTable:           snapshot.ArcTable,
		Proof:              snapshot.Proof,
	}, nil
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
	return resp.Snapshot, nil
}

func normalizeRunConfig(cfg RunConfig) (RunConfig, error) {
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

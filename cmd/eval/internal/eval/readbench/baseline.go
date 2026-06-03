package readbench

import (
	"context"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/dewebprotocol/malt/cmd/internal/merkledagimport"
	"github.com/dewebprotocol/malt/runtime/metrics"
	casmock "github.com/dewebprotocol/malt/storage/cas/mock"
	unixfsio "github.com/ipfs/boxo/ipld/unixfs/io"
	cid "github.com/ipfs/go-cid"
	ipld "github.com/ipfs/go-ipld-format"
)

type fixtureData struct {
	fixtureName string
	smallPath   string
	largePath   string
	smallData   []byte
	largeData   []byte
}

// BaselineSystem wraps a mock CAS + IPLD DAG for MerkleDAG/HAMT resolve.
type BaselineSystem struct {
	system SystemName
	store  *casmock.CAS
	dag    ipld.DAGService
	root   string
}

func newFixtureData(cfg FixtureConfig) fixtureData {
	return fixtureData{
		fixtureName: cfg.FixtureName,
		smallPath:   fixturePath(cfg.DirectoryDepth, "small.txt"),
		largePath:   fixturePath(cfg.DirectoryDepth, "large.bin"),
		smallData:   deterministicBytes("small", cfg.SmallFileBytes),
		largeData:   deterministicBytes("large", cfg.LargeFileBytes),
	}
}

func newBaselineSystem(ctx context.Context, system SystemName, fixture fixtureData) (*BaselineSystem, error) {
	store := casmock.NewCAS(casmock.WithoutLatency())
	return newBaselineSystemWithStore(ctx, system, fixture, store)
}

// NewBaselineSystemWithCAS creates a baseline system using the given mock CAS.
// This allows injecting a CAS with specific latency for benchmarking.
func NewBaselineSystemWithCAS(ctx context.Context, system SystemName, multiFix MultiDepthFixture, store *casmock.CAS) (*BaselineSystem, error) {
	dirLayout, err := baselineDirLayout(system)
	if err != nil {
		return nil, err
	}
	var files []merkledagimport.File
	for _, fix := range multiFix.Fixtures {
		files = append(files,
			merkledagimport.File{Path: fix.SmallPath, Data: fix.SmallData, Mode: 0o644},
			merkledagimport.File{Path: fix.LargePath, Data: fix.LargeData, Mode: 0o644},
		)
	}
	dag := merkledagimport.NewDAGService(store)
	result, err := merkledagimport.ImportFiles(ctx, store, files, merkledagimport.Options{
		Model:      merkledagimport.ModelUnixFS,
		FileLayout: merkledagimport.FileLayoutBalanced,
		DirLayout:  dirLayout,
	})
	if err != nil {
		return nil, fmt.Errorf("%s prepare fixture: %w", system, err)
	}
	return &BaselineSystem{
		system: system,
		store:  store,
		dag:    dag,
		root:   result.Root,
	}, nil
}

func newBaselineSystemWithStore(ctx context.Context, system SystemName, fixture fixtureData, store *casmock.CAS) (*BaselineSystem, error) {
	dirLayout, err := baselineDirLayout(system)
	if err != nil {
		return nil, err
	}
	dag := merkledagimport.NewDAGService(store)
	result, err := merkledagimport.ImportFiles(ctx, store, []merkledagimport.File{
		{Path: fixture.smallPath, Data: fixture.smallData, Mode: 0o644},
		{Path: fixture.largePath, Data: fixture.largeData, Mode: 0o644},
	}, merkledagimport.Options{
		Model:      merkledagimport.ModelUnixFS,
		FileLayout: merkledagimport.FileLayoutBalanced,
		DirLayout:  dirLayout,
	})
	if err != nil {
		return nil, fmt.Errorf("%s prepare fixture: %w", system, err)
	}
	return &BaselineSystem{
		system: system,
		store:  store,
		dag:    dag,
		root:   result.Root,
	}, nil
}

func baselineDirLayout(system SystemName) (string, error) {
	switch system {
	case SystemMerkleDAG:
		return merkledagimport.DirLayoutBasic, nil
	case SystemHAMT:
		return merkledagimport.DirLayoutHAMT, nil
	default:
		return "", fmt.Errorf("system %q is not an IPLD UnixFS baseline", system)
	}
}

// MeasureResolve measures a single resolve_path operation at the given path.
func (b *BaselineSystem) MeasureResolve(ctx context.Context, iteration int, fixture string, filePath string) (*Result, error) {
	op := operation{kind: OperationResolvePath, path: filePath}
	return b.measureOperation(ctx, iteration, fixture, op)
}

func (b *BaselineSystem) measureOperation(ctx context.Context, iteration int, fixture string, op operation) (*Result, error) {
	if b == nil || b.store == nil || b.dag == nil {
		return nil, fmt.Errorf("baseline system is nil")
	}
	b.store.ResetStats()

	start := time.Now()
	var (
		target       string
		contentBytes *int
	)
	switch op.kind {
	case OperationResolvePath:
		node, err := b.resolvePath(ctx, op.path)
		if err != nil {
			return nil, fmt.Errorf("%s resolve path %q: %w", b.system, op.path, err)
		}
		target = node.Cid().String()
	case OperationContentRange:
		node, err := b.resolvePath(ctx, op.path)
		if err != nil {
			return nil, fmt.Errorf("%s resolve content path %q: %w", b.system, op.path, err)
		}
		count, err := readUnixFSRange(ctx, b.dag, node, op.rangeHeader)
		if err != nil {
			return nil, fmt.Errorf("%s content range read %q: %w", b.system, op.path, err)
		}
		contentBytes = &count
	default:
		return nil, fmt.Errorf("unsupported operation kind %q", op.kind)
	}
	elapsed := positiveElapsedNS(start, time.Now())
	casStats := b.store.SnapshotStats()

	return &Result{
		System:            b.system,
		OperationKind:     op.kind,
		Iteration:         iteration,
		FixtureName:       fixture,
		Path:              op.path,
		RangeHeader:       op.rangeHeader,
		ElapsedNS:         elapsed,
		ContentBytes:      contentBytes,
		EvidenceItemCount: int(casStats.GetCount),
		Target:            target,
		CAS:               casStats,
		ArcTable:          metrics.ArcTableStats{},
		Proof:             metrics.ProofStats{},
	}, nil
}

func (b *BaselineSystem) resolvePath(ctx context.Context, queryPath string) (ipld.Node, error) {
	root, err := cid.Parse(b.root)
	if err != nil {
		return nil, fmt.Errorf("parse baseline root %q: %w", b.root, err)
	}
	node, err := b.dag.Get(ctx, root)
	if err != nil {
		return nil, err
	}

	clean := path.Clean(strings.Trim(queryPath, "/"))
	if clean == "." {
		return node, nil
	}
	for _, segment := range strings.Split(clean, "/") {
		dir, err := unixfsio.NewDirectoryFromNode(b.dag, node)
		if err != nil {
			return nil, err
		}
		node, err = dir.Find(ctx, segment)
		if err != nil {
			return nil, err
		}
	}
	return node, nil
}

func readUnixFSRange(ctx context.Context, dag ipld.NodeGetter, node ipld.Node, rawRange string) (int, error) {
	reader, err := unixfsio.NewDagReader(ctx, node, dag)
	if err != nil {
		return 0, err
	}
	defer reader.Close()

	start, endExclusive, err := parseReadRangeHeader(rawRange, int64(reader.Size()))
	if err != nil {
		return 0, err
	}
	length := endExclusive - start
	if length == 0 {
		return 0, nil
	}
	if length > int64(int(length)) {
		return 0, fmt.Errorf("range length overflows int")
	}
	if _, err := reader.Seek(start, io.SeekStart); err != nil {
		return 0, err
	}
	buf := make([]byte, int(length))
	n, err := io.ReadFull(reader, buf)
	if err != nil {
		return 0, err
	}
	return n, nil
}

func parseReadRangeHeader(raw string, size int64) (int64, int64, error) {
	if size < 0 {
		return 0, 0, fmt.Errorf("invalid size")
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, size, nil
	}
	if !strings.HasPrefix(raw, "bytes=") {
		return 0, 0, fmt.Errorf("invalid range")
	}
	spec := strings.TrimPrefix(raw, "bytes=")
	if strings.Contains(spec, ",") {
		return 0, 0, fmt.Errorf("multiple ranges are not supported")
	}
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid range")
	}
	if parts[0] == "" {
		suffix, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, fmt.Errorf("invalid range")
		}
		if size == 0 {
			return 0, 0, fmt.Errorf("unsatisfiable range")
		}
		if suffix > size {
			suffix = size
		}
		return size - suffix, size, nil
	}

	start, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || start < 0 {
		return 0, 0, fmt.Errorf("invalid range")
	}
	if start >= size {
		return 0, 0, fmt.Errorf("unsatisfiable range")
	}
	if parts[1] == "" {
		return start, size, nil
	}
	end, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || end < start {
		return 0, 0, fmt.Errorf("invalid range")
	}
	if end >= size {
		end = size - 1
	}
	return start, end + 1, nil
}

package readbench

import (
	"context"
	"fmt"
	"io/fs"
	"time"

	"github.com/dewebprotocol/malt/auth/commitment/kzg"
	"github.com/dewebprotocol/malt/auth/proof/prooflist"
	"github.com/dewebprotocol/malt/cmd/internal/merkledagimport"
	"github.com/dewebprotocol/malt/layout/unixfs"
	"github.com/dewebprotocol/malt/runtime/arctable/versioned"
	"github.com/dewebprotocol/malt/runtime/metrics"
	listtree "github.com/dewebprotocol/malt/runtime/semantic/list/tree"
	mappingradix "github.com/dewebprotocol/malt/runtime/semantic/mapping/radix"
	casmock "github.com/dewebprotocol/malt/storage/cas/mock"
	"github.com/dewebprotocol/malt/storage/kv/memory"
	cid "github.com/ipfs/go-cid"
)

// MatrixOperation is one read operation measured against a shared dataset.
type MatrixOperation struct {
	Kind        OperationKind
	Workload    WorkloadKind
	Path        string
	PathDepth   int
	RangeHeader string
}

// MatrixSystem measures read operations for one materialized representation.
type MatrixSystem interface {
	Name() SystemName
	Measure(context.Context, int, *MatrixDataset, MatrixOperation) (*Result, error)
	Close() error
}

// MatrixOperations returns the paper-facing read operations at one depth.
func MatrixOperations(dataset *MatrixDataset, depth int, rangeHeader string) ([]MatrixOperation, error) {
	if dataset == nil {
		return nil, fmt.Errorf("dataset is nil")
	}
	smallPath := dataset.SmallPaths[depth]
	largePath := dataset.LargePaths[depth]
	if smallPath == "" || largePath == "" {
		return nil, fmt.Errorf("dataset %q has no measured paths at depth %d", dataset.Name, depth)
	}
	return []MatrixOperation{
		{Kind: OperationResolvePath, Workload: WorkloadDeepPathLookup, Path: smallPath, PathDepth: depth},
		{Kind: OperationContentFull, Workload: WorkloadSmallFileRead, Path: smallPath, PathDepth: depth},
		{Kind: OperationContentRange, Workload: WorkloadLargeFileRangeRead, Path: largePath, PathDepth: depth, RangeHeader: rangeHeader},
	}, nil
}

// NewMatrixSystem materializes dataset for one read_matrix system.
func NewMatrixSystem(ctx context.Context, system SystemName, dataset *MatrixDataset) (MatrixSystem, error) {
	switch system {
	case SystemMALTFlat:
		return newMatrixMALTSystem(ctx, dataset)
	case SystemMerkleDAG, SystemHAMT:
		return newMatrixBaselineSystem(ctx, system, dataset)
	default:
		return nil, fmt.Errorf("unknown system %q", system)
	}
}

type matrixMALTSystem struct {
	store  *casmock.CAS
	table  *versioned.ArcTable
	layout *unixfs.Layout
	root   cid.Cid
}

func newMatrixMALTSystem(ctx context.Context, dataset *MatrixDataset) (*matrixMALTSystem, error) {
	if dataset == nil {
		return nil, fmt.Errorf("dataset is nil")
	}
	store := casmock.NewCAS()
	table, err := versioned.NewArcTable(versioned.WithKVStore(memory.New()))
	if err != nil {
		return nil, fmt.Errorf("create arctable: %w", err)
	}
	scheme, err := kzg.NewScheme()
	if err != nil {
		_ = table.Close()
		return nil, fmt.Errorf("create commitment scheme: %w", err)
	}
	maps, err := mappingradix.NewMap(scheme, table)
	if err != nil {
		_ = table.Close()
		return nil, fmt.Errorf("create map semantics: %w", err)
	}
	lists, err := listtree.NewList(scheme, table)
	if err != nil {
		_ = table.Close()
		return nil, fmt.Errorf("create list semantics: %w", err)
	}
	layout, err := unixfs.New(unixfs.Options{
		Namespace: "read-matrix",
		ChunkSize: unixfs.DefaultChunkSize,
		Map:       maps,
		List:      lists,
		Blocks:    store,
	})
	if err != nil {
		_ = table.Close()
		return nil, err
	}
	root := cid.Undef
	for _, file := range dataset.Files {
		root, err = layout.AddFile(ctx, root, file.Path, file.Data)
		if err != nil {
			_ = table.Close()
			return nil, fmt.Errorf("materialize %s: %w", file.Path, err)
		}
	}
	return &matrixMALTSystem{store: store, table: table, layout: layout, root: root}, nil
}

func (s *matrixMALTSystem) Name() SystemName { return SystemMALTFlat }

func (s *matrixMALTSystem) Close() error {
	if s == nil || s.table == nil {
		return nil
	}
	return s.table.Close()
}

func (s *matrixMALTSystem) Measure(ctx context.Context, iteration int, dataset *MatrixDataset, op MatrixOperation) (*Result, error) {
	if s == nil || s.store == nil || s.layout == nil {
		return nil, fmt.Errorf("malt matrix system is nil")
	}
	s.store.ResetStats()

	start := time.Now()
	resolution, err := s.layout.Resolve(ctx, s.root, op.Path)
	if err != nil {
		return nil, fmt.Errorf("malt resolve %q: %w", op.Path, err)
	}
	pl, err := unixfs.ProofListFromSteps(s.root, op.Path, resolution.Steps)
	if err != nil {
		return nil, fmt.Errorf("malt prooflist %q: %w", op.Path, err)
	}

	var contentBytes *int
	switch op.Kind {
	case OperationResolvePath:
	case OperationContentFull:
		data, err := s.store.Get(ctx, resolution.Payload)
		if err != nil {
			return nil, fmt.Errorf("malt content read %q: %w", op.Path, err)
		}
		count := len(data)
		contentBytes = &count
	case OperationContentRange:
		startOffset, endExclusive, err := parseReadRangeHeader(op.RangeHeader, int64(dataset.LargeFileBytes))
		if err != nil {
			return nil, err
		}
		length := uint64(endExclusive - startOffset)
		data, err := s.layout.ReadListPayloadRange(ctx, resolution.Payload, uint64(startOffset), length)
		if err != nil {
			return nil, fmt.Errorf("malt range read %q: %w", op.Path, err)
		}
		if err := s.layout.AppendListPayloadRangeProof(ctx, pl, op.Path, resolution.Payload, uint64(startOffset), length); err != nil {
			return nil, fmt.Errorf("malt range proof %q: %w", op.Path, err)
		}
		count := len(data)
		contentBytes = &count
	default:
		return nil, fmt.Errorf("unsupported operation kind %q", op.Kind)
	}
	elapsed := positiveElapsedNS(start, time.Now())

	proofStats := proofStatsFor(pl)
	result := &Result{
		System:             SystemMALTFlat,
		OperationKind:      op.Kind,
		Workload:           op.Workload,
		Iteration:          iteration,
		FixtureName:        dataset.Name,
		Path:               op.Path,
		RangeHeader:        op.RangeHeader,
		ElapsedNS:          elapsed,
		ContentBytes:       contentBytes,
		ProofListStepCount: len(pl.Steps),
		EvidenceItemCount:  len(pl.Steps),
		Target:             resolution.Payload.String(),
		CAS:                s.store.SnapshotStats(),
		ArcTable:           metrics.ArcTableStats{},
		Proof:              proofStats,
	}
	attachDatasetMetadata(result, dataset, op)
	return result, nil
}

type matrixBaselineSystem struct {
	inner *BaselineSystem
}

func newMatrixBaselineSystem(ctx context.Context, system SystemName, dataset *MatrixDataset) (*matrixBaselineSystem, error) {
	if dataset == nil {
		return nil, fmt.Errorf("dataset is nil")
	}
	dirLayout, err := baselineDirLayout(system)
	if err != nil {
		return nil, err
	}
	store := casmock.NewCAS()
	files := make([]merkledagimport.File, 0, len(dataset.Files))
	for _, file := range dataset.Files {
		files = append(files, merkledagimport.File{
			Path: file.Path,
			Data: file.Data,
			Mode: fs.FileMode(0o644),
		})
	}
	dag := merkledagimport.NewDAGService(store)
	imported, err := merkledagimport.ImportFiles(ctx, store, files, merkledagimport.Options{
		Model:      merkledagimport.ModelUnixFS,
		FileLayout: merkledagimport.FileLayoutBalanced,
		DirLayout:  dirLayout,
	})
	if err != nil {
		return nil, fmt.Errorf("%s prepare matrix dataset: %w", system, err)
	}
	return &matrixBaselineSystem{inner: &BaselineSystem{
		system: system,
		store:  store,
		dag:    dag,
		root:   imported.Root,
	}}, nil
}

func (s *matrixBaselineSystem) Name() SystemName {
	if s == nil || s.inner == nil {
		return ""
	}
	return s.inner.system
}

func (s *matrixBaselineSystem) Close() error { return nil }

func (s *matrixBaselineSystem) Measure(ctx context.Context, iteration int, dataset *MatrixDataset, op MatrixOperation) (*Result, error) {
	if s == nil || s.inner == nil {
		return nil, fmt.Errorf("baseline matrix system is nil")
	}
	result, err := s.inner.measureOperation(ctx, iteration, dataset.Name, operation{
		kind:        op.Kind,
		workload:    op.Workload,
		path:        op.Path,
		rangeHeader: op.RangeHeader,
	})
	if err != nil {
		return nil, err
	}
	attachDatasetMetadata(result, dataset, op)
	return result, nil
}

func proofStatsFor(pl *prooflist.ProofList) metrics.ProofStats {
	if pl == nil {
		return metrics.ProofStats{}
	}
	var recorder metrics.ProofStatsRecorder
	recorder.RecordProofList(*pl)
	return recorder.Snapshot()
}

func attachDatasetMetadata(result *Result, dataset *MatrixDataset, op MatrixOperation) {
	if result == nil || dataset == nil {
		return
	}
	result.DatasetName = dataset.Name
	result.FileCount = dataset.FileCount
	result.DirectoryCount = dataset.DirectoryCount
	result.PathCount = dataset.PathCount
	result.PathDepth = op.PathDepth
	result.LogicalPayloadBytes = dataset.LogicalPayloadBytes
	result.SmallFileBytes = dataset.SmallFileBytes
	result.LargeFileBytes = dataset.LargeFileBytes
}

var _ MatrixSystem = (*matrixMALTSystem)(nil)
var _ MatrixSystem = (*matrixBaselineSystem)(nil)

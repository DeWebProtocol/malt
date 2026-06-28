package readbench

import (
	"context"
	"fmt"
	"io/fs"
	"time"

	"github.com/dewebprotocol/malt/cmd/internal/merkledagimport"
	casmock "github.com/dewebprotocol/malt/storage/cas/mock"
)

// MatrixOperation is one read operation measured against a shared dataset.
type MatrixOperation struct {
	Kind       OperationKind
	Workload   WorkloadKind
	Path       string
	PathDepth  int
	PathSample int
}

// MatrixSystem measures read operations for one materialized representation.
type MatrixSystem interface {
	Name() SystemName
	Measure(context.Context, int, *MatrixDataset, MatrixOperation) (*Result, error)
	Close() error
}

// MatrixOperations returns the paper-facing path lookup operation at one depth.
func MatrixOperations(dataset *MatrixDataset, depth int) ([]MatrixOperation, error) {
	if dataset == nil {
		return nil, fmt.Errorf("dataset is nil")
	}
	lookupPaths := dataset.LookupPaths[depth]
	if len(lookupPaths) == 0 {
		return nil, fmt.Errorf("dataset %q has no measured path at depth %d", dataset.Name, depth)
	}
	ops := make([]MatrixOperation, 0, len(lookupPaths))
	for i, lookupPath := range lookupPaths {
		ops = append(ops, MatrixOperation{
			Kind:       OperationResolvePath,
			Workload:   WorkloadDeepPathLookup,
			Path:       lookupPath,
			PathDepth:  depth,
			PathSample: i + 1,
		})
	}
	return ops, nil
}

// NewMatrixSystem materializes dataset for one read_matrix system and CAS
// latency point. Only CAS Get has latency; setup writes remain zero-latency.
func NewMatrixSystem(ctx context.Context, system SystemName, dataset *MatrixDataset, casLatencyMS int) (MatrixSystem, error) {
	if casLatencyMS < 0 {
		return nil, fmt.Errorf("cas latency must be non-negative")
	}
	switch system {
	case SystemMALTFlat:
		return newMatrixMALTSystem(ctx, dataset, casLatencyMS)
	case SystemMerkleDAG, SystemHAMT:
		return newMatrixBaselineSystem(ctx, system, dataset, casLatencyMS)
	case SystemFlatHAMT:
		return newMatrixFlatHAMTSystem(ctx, dataset, casLatencyMS)
	default:
		return nil, fmt.Errorf("unknown system %q", system)
	}
}

type matrixMALTSystem struct {
	inner        *LocalMALTSystem
	casLatencyMS int
}

func newMatrixMALTSystem(ctx context.Context, dataset *MatrixDataset, casLatencyMS int) (*matrixMALTSystem, error) {
	if dataset == nil {
		return nil, fmt.Errorf("dataset is nil")
	}
	store := newMatrixCAS(casLatencyMS)
	inner, err := NewLocalMALTSystemWithFiles(ctx, store, dataset.Files)
	if err != nil {
		return nil, err
	}
	return &matrixMALTSystem{inner: inner, casLatencyMS: casLatencyMS}, nil
}

func (s *matrixMALTSystem) Name() SystemName { return SystemMALTFlat }

func (s *matrixMALTSystem) Close() error { return nil }

func (s *matrixMALTSystem) Measure(ctx context.Context, iteration int, dataset *MatrixDataset, op MatrixOperation) (*Result, error) {
	if s == nil || s.inner == nil {
		return nil, fmt.Errorf("malt matrix system is nil")
	}
	if op.Kind != OperationResolvePath {
		return nil, fmt.Errorf("unsupported operation kind %q", op.Kind)
	}
	result, err := s.inner.MeasureResolveWithTargetFetch(ctx, iteration, dataset.Name, op.Path)
	if err != nil {
		return nil, err
	}
	attachDatasetMetadata(result, dataset, op, s.casLatencyMS)
	return result, nil
}

type matrixBaselineSystem struct {
	inner        *BaselineSystem
	casLatencyMS int
}

func newMatrixBaselineSystem(ctx context.Context, system SystemName, dataset *MatrixDataset, casLatencyMS int) (*matrixBaselineSystem, error) {
	if dataset == nil {
		return nil, fmt.Errorf("dataset is nil")
	}
	dirLayout, err := baselineDirLayout(system)
	if err != nil {
		return nil, err
	}
	store := newMatrixCAS(casLatencyMS)
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
	return &matrixBaselineSystem{
		inner: &BaselineSystem{
			system: system,
			store:  store,
			dag:    dag,
			root:   imported.Root,
		},
		casLatencyMS: casLatencyMS,
	}, nil
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
		kind:     op.Kind,
		workload: op.Workload,
		path:     op.Path,
	})
	if err != nil {
		return nil, err
	}
	attachDatasetMetadata(result, dataset, op, s.casLatencyMS)
	return result, nil
}

func newMatrixCAS(casLatencyMS int) *casmock.CAS {
	return casmock.NewCAS(
		casmock.WithGetLatency(time.Duration(casLatencyMS)*time.Millisecond),
		casmock.WithJitter(0),
	)
}

func attachDatasetMetadata(result *Result, dataset *MatrixDataset, op MatrixOperation, casLatencyMS int) {
	if result == nil || dataset == nil {
		return
	}
	result.DatasetName = dataset.Name
	result.FileCount = dataset.FileCount
	result.DirectoryCount = dataset.DirectoryCount
	result.PathCount = dataset.PathCount
	result.PathDepth = op.PathDepth
	result.PathSample = op.PathSample
	result.LogicalPayloadBytes = dataset.LogicalPayloadBytes
	result.SmallFileBytes = dataset.SmallFileBytes
	result.LargeFileBytes = dataset.LargeFileBytes
	result.CASLatencyMS = casLatencyMS
}

var _ MatrixSystem = (*matrixMALTSystem)(nil)
var _ MatrixSystem = (*matrixBaselineSystem)(nil)

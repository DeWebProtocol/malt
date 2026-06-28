package readbench

import (
	"context"
	"fmt"
	"time"

	"github.com/dewebprotocol/malt/cmd/internal/merkledagimport"
	"github.com/dewebprotocol/malt/runtime/metrics"
	casmock "github.com/dewebprotocol/malt/storage/cas/mock"
	merkledag "github.com/ipfs/boxo/ipld/merkledag"
	"github.com/ipfs/boxo/ipld/unixfs/hamt"
	unixfsio "github.com/ipfs/boxo/ipld/unixfs/io"
	cid "github.com/ipfs/go-cid"
	ipld "github.com/ipfs/go-ipld-format"
	mh "github.com/multiformats/go-multihash"
)

// matrixFlatHAMTSystem stores complete logical paths as keys in one IPFS HAMT.
// It is a flat map baseline, not a UnixFS directory traversal.
type matrixFlatHAMTSystem struct {
	store        *casmock.CAS
	dag          ipld.DAGService
	root         cid.Cid
	casLatencyMS int
}

func newMatrixFlatHAMTSystem(ctx context.Context, dataset *MatrixDataset, casLatencyMS int) (*matrixFlatHAMTSystem, error) {
	if dataset == nil {
		return nil, fmt.Errorf("dataset is nil")
	}
	store := newMatrixCAS(casLatencyMS)
	dag := merkledagimport.NewDAGService(store)
	shard, err := hamt.NewShard(dag, unixfsio.DefaultShardWidth)
	if err != nil {
		return nil, fmt.Errorf("create flat HAMT shard: %w", err)
	}
	shard.SetCidBuilder(cid.Prefix{Version: 1, Codec: cid.DagProtobuf, MhType: mh.SHA2_256, MhLength: -1})
	for _, file := range dataset.Files {
		if file.Path == "" {
			return nil, fmt.Errorf("empty file path in flat HAMT dataset")
		}
		node := merkledag.NewRawNode(file.Data)
		if err := shard.Set(ctx, file.Path, node); err != nil {
			return nil, fmt.Errorf("set flat HAMT key %q: %w", file.Path, err)
		}
	}
	rootNode, err := shard.Node()
	if err != nil {
		return nil, fmt.Errorf("serialize flat HAMT root: %w", err)
	}
	return &matrixFlatHAMTSystem{
		store:        store,
		dag:          dag,
		root:         rootNode.Cid(),
		casLatencyMS: casLatencyMS,
	}, nil
}

func (s *matrixFlatHAMTSystem) Name() SystemName { return SystemFlatHAMT }

func (s *matrixFlatHAMTSystem) Close() error { return nil }

func (s *matrixFlatHAMTSystem) Measure(ctx context.Context, iteration int, dataset *MatrixDataset, op MatrixOperation) (*Result, error) {
	if s == nil || s.store == nil || s.dag == nil {
		return nil, fmt.Errorf("flat HAMT matrix system is nil")
	}
	if op.Kind != OperationResolvePath {
		return nil, fmt.Errorf("unsupported operation kind %q", op.Kind)
	}
	s.store.ResetStats()

	start := time.Now()
	rootNode, err := s.dag.Get(ctx, s.root)
	if err != nil {
		return nil, fmt.Errorf("get flat HAMT root %s: %w", s.root, err)
	}
	shard, err := hamt.NewHamtFromDag(s.dag, rootNode)
	if err != nil {
		return nil, fmt.Errorf("load flat HAMT root %s: %w", s.root, err)
	}
	link, err := shard.Find(ctx, op.Path)
	if err != nil {
		return nil, fmt.Errorf("flat HAMT lookup %q: %w", op.Path, err)
	}
	if link == nil {
		return nil, fmt.Errorf("flat HAMT lookup %q returned nil link", op.Path)
	}
	targetNode, err := s.dag.Get(ctx, link.Cid)
	if err != nil {
		return nil, fmt.Errorf("fetch flat HAMT target %s: %w", link.Cid, err)
	}
	contentBytes := len(targetNode.RawData())
	elapsed := positiveElapsedNS(start, time.Now())
	casStats := s.store.SnapshotStats()

	result := &Result{
		System:            SystemFlatHAMT,
		OperationKind:     op.Kind,
		Workload:          op.Workload,
		Iteration:         iteration,
		FixtureName:       dataset.Name,
		Path:              op.Path,
		ElapsedNS:         elapsed,
		ContentBytes:      &contentBytes,
		EvidenceItemCount: int(casStats.GetCount),
		Target:            link.Cid.String(),
		CAS:               casStats,
		ArcTable:          metrics.ArcTableStats{},
		Proof:             metrics.ProofStats{},
	}
	attachDatasetMetadata(result, dataset, op, s.casLatencyMS)
	return result, nil
}

var _ MatrixSystem = (*matrixFlatHAMTSystem)(nil)

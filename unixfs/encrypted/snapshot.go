package encrypted

import (
	"context"
	"fmt"
	"sync"

	"github.com/dewebprotocol/malt-client/unixfs"
	"github.com/dewebprotocol/malt-core/auth/arcset"
	materialmemory "github.com/dewebprotocol/malt-core/auth/arcset/materializer/memory"
	"github.com/dewebprotocol/malt-core/auth/commitment"
	"github.com/dewebprotocol/malt-core/auth/commitment/ipa"
	"github.com/dewebprotocol/malt-core/auth/commitment/kzg"
	runtimegraph "github.com/dewebprotocol/malt-core/graph/runtime"
	"github.com/dewebprotocol/malt-core/mutation"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

// SnapshotBlockStore is the owner-local CAS used while one encrypted snapshot
// is prepared. Nothing in this capability is a remote publication authority.
type SnapshotBlockStore interface {
	BlockWriter
	unixfs.BlockGetter
}

// SnapshotOptions separates owner-local computation from the untrusted remote
// publication capabilities. Publish replays only locally computed objects and
// rejects every substituted remote CID/root.
type SnapshotOptions struct {
	Backend            maltcid.BackendKind
	LocalBlocks        SnapshotBlockStore
	RemoteGraph        GraphWriter
	RemoteBlocks       BlockWriter
	PlaintextChunkSize int
}

// Snapshot owns one locally encrypted, locally root-computed publication
// transaction. PrepareBinding and BuildDataset perform no remote I/O.
type Snapshot struct {
	builder      *builder
	graph        *recordingGraph
	blocks       *recordingBlocks
	remoteGraph  GraphWriter
	remoteBlocks BlockWriter
	mu           sync.Mutex
	sealed       bool
	published    bool
}

func NewSnapshot(options SnapshotOptions) (*Snapshot, error) {
	if options.LocalBlocks == nil || options.RemoteGraph == nil || options.RemoteBlocks == nil {
		return nil, fmt.Errorf("encrypted UnixFS snapshot local and remote capabilities are required")
	}
	graph, err := newRecordingGraph(options.Backend)
	if err != nil {
		return nil, err
	}
	blocks := &recordingBlocks{local: options.LocalBlocks, seen: make(map[string]struct{})}
	builder, err := newBuilder(builderOptions{Graph: graph, Blocks: blocks, PlaintextChunkSize: options.PlaintextChunkSize})
	if err != nil {
		return nil, err
	}
	return &Snapshot{
		builder: builder, graph: graph, blocks: blocks,
		remoteGraph: options.RemoteGraph, remoteBlocks: options.RemoteBlocks,
	}, nil
}

func (s *Snapshot) PrepareBinding(ctx context.Context, request BindingSource) (PreparedBinding, error) {
	if s == nil || s.builder == nil {
		return PreparedBinding{}, fmt.Errorf("encrypted UnixFS snapshot is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed {
		return PreparedBinding{}, fmt.Errorf("encrypted UnixFS snapshot is sealed for publication")
	}
	return s.builder.PrepareBinding(ctx, request)
}

func (s *Snapshot) BuildDataset(ctx context.Context, request DatasetBuildRequest) (DatasetBuildResult, error) {
	if s == nil || s.builder == nil {
		return DatasetBuildResult{}, fmt.Errorf("encrypted UnixFS snapshot is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sealed {
		return DatasetBuildResult{}, fmt.Errorf("encrypted UnixFS snapshot is sealed for publication")
	}
	return s.builder.BuildDataset(ctx, request)
}

// Publish uploads the exact local ciphertext blocks, then replays graph
// objects child-before-parent. A Gateway result is accepted only when it is
// byte-for-byte/root-for-root identical to local computation.
func (s *Snapshot) Publish(ctx context.Context) error {
	if s == nil || s.builder == nil || s.graph == nil || s.blocks == nil {
		return fmt.Errorf("encrypted UnixFS snapshot is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.published {
		return nil
	}
	// The first publication attempt freezes the local transaction. A failed
	// attempt may replay these exact objects, but callers cannot append new
	// blocks or roots that would be omitted by a later retry.
	s.sealed = true
	for _, key := range s.blocks.keys {
		body, err := s.blocks.local.Get(ctx, key)
		if err != nil {
			return fmt.Errorf("read local encrypted UnixFS snapshot block %s: %w", key, err)
		}
		remoteKey, err := s.remoteBlocks.Put(ctx, body)
		if err != nil {
			return fmt.Errorf("publish encrypted UnixFS snapshot block %s: %w", key, err)
		}
		if !remoteKey.Equals(key) {
			return fmt.Errorf("remote CAS substituted encrypted UnixFS block CID %s with %s", key, remoteKey)
		}
	}
	for index, operation := range s.graph.operations {
		var (
			got cid.Cid
			err error
		)
		switch operation.kind {
		case graphOperationMap:
			got, err = s.remoteGraph.CreateStagedRoot(ctx, operation.bindings)
		case graphOperationList:
			got, err = s.remoteGraph.ApplyFixedListPayloadMutation(ctx, operation.mutation)
		default:
			err = fmt.Errorf("unknown encrypted UnixFS graph operation")
		}
		if err != nil {
			return fmt.Errorf("publish encrypted UnixFS graph operation %d: %w", index, err)
		}
		if !got.Equals(operation.expected) {
			return fmt.Errorf("remote graph substituted encrypted UnixFS root %s with %s", operation.expected, got)
		}
	}
	s.published = true
	return nil
}

type recordingBlocks struct {
	local SnapshotBlockStore
	keys  []cid.Cid
	seen  map[string]struct{}
}

func (s *recordingBlocks) Put(ctx context.Context, body []byte) (cid.Cid, error) {
	key, err := s.local.Put(ctx, body)
	if err != nil {
		return cid.Undef, err
	}
	if _, ok := s.seen[key.KeyString()]; !ok {
		s.seen[key.KeyString()] = struct{}{}
		s.keys = append(s.keys, key)
	}
	return key, nil
}

func (s *recordingBlocks) PutWithCodec(ctx context.Context, body []byte, codec uint64) (cid.Cid, error) {
	key, err := s.local.PutWithCodec(ctx, body, codec)
	if err != nil {
		return cid.Undef, err
	}
	if _, ok := s.seen[key.KeyString()]; !ok {
		s.seen[key.KeyString()] = struct{}{}
		s.keys = append(s.keys, key)
	}
	return key, nil
}

type graphOperationKind uint8

const (
	graphOperationMap graphOperationKind = iota + 1
	graphOperationList
)

type graphOperation struct {
	kind     graphOperationKind
	bindings map[string]string
	mutation mutation.SemanticMutation
	expected cid.Cid
}

type recordingGraph struct {
	graph      *runtimegraph.RuntimeGraph
	scope      string
	operations []graphOperation
}

func newRecordingGraph(backend maltcid.BackendKind) (*recordingGraph, error) {
	var (
		scheme commitment.IndexCommitment
		err    error
	)
	switch backend {
	case maltcid.BackendKindKZG:
		scheme, err = kzg.NewScheme()
	case maltcid.BackendKindIPA:
		scheme, err = ipa.NewCommitterScheme(ipa.ProfileCompact)
	default:
		return nil, fmt.Errorf("encrypted UnixFS snapshot requires a supported MALT backend, got %q", backend)
	}
	if err != nil {
		return nil, fmt.Errorf("initialize encrypted UnixFS %s root computation: %w", backend, err)
	}
	const scope = "encrypted-unixfs-snapshot"
	graph, err := runtimegraph.NewGraph(
		scope, materialmemory.New(true),
		runtimegraph.WithCommitmentBackend(backend, scheme),
		runtimegraph.WithDefaultCommitmentBackend(backend),
		runtimegraph.WithNamespace(scope),
	)
	if err != nil {
		return nil, err
	}
	return &recordingGraph{graph: graph, scope: scope}, nil
}

func (g *recordingGraph) CreateStagedRoot(ctx context.Context, bindings map[string]string) (cid.Cid, error) {
	values := make(map[string]cid.Cid, len(bindings))
	copyBindings := make(map[string]string, len(bindings))
	for coordinate, raw := range bindings {
		target, err := cid.Parse(raw)
		if err != nil {
			return cid.Undef, err
		}
		values[coordinate] = target
		copyBindings[coordinate] = target.String()
	}
	set, err := arcset.NewArcSet(values)
	if err != nil {
		return cid.Undef, err
	}
	root, err := g.graph.StructureCreator().CreateStructure(ctx, g.scope, set)
	if err != nil {
		return cid.Undef, err
	}
	g.operations = append(g.operations, graphOperation{kind: graphOperationMap, bindings: copyBindings, expected: root})
	return root, nil
}

func (g *recordingGraph) CreateFixedListBaseRoot(ctx context.Context) (cid.Cid, error) {
	return g.CreateStagedRoot(ctx, map[string]string{"@payload": "bafkqaaa"})
}

func (g *recordingGraph) ApplyFixedListPayloadMutation(ctx context.Context, value mutation.SemanticMutation) (cid.Cid, error) {
	receipt, err := g.graph.Writer().Apply(ctx, g.scope, value)
	if err != nil {
		return cid.Undef, err
	}
	if !receipt.NewRoot.Defined() {
		return cid.Undef, fmt.Errorf("local encrypted UnixFS List root is undefined")
	}
	g.operations = append(g.operations, graphOperation{kind: graphOperationList, mutation: value, expected: receipt.NewRoot})
	return receipt.NewRoot, nil
}

var _ GraphWriter = (*recordingGraph)(nil)

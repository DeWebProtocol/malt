package encrypted

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/dewebprotocol/malt-client/unixfs"
	"github.com/dewebprotocol/malt-core/auth/commitment"
	"github.com/dewebprotocol/malt-core/auth/commitment/ipa"
	"github.com/dewebprotocol/malt-core/auth/commitment/kzg"
	"github.com/dewebprotocol/malt-core/auth/coordinate"
	"github.com/dewebprotocol/malt-core/derivation"
	"github.com/dewebprotocol/malt-core/engine"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
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
	RemoteGraph        CandidatePublisher
	RemoteBlocks       BlockWriter
	PlaintextChunkSize int
}

// Snapshot owns one locally encrypted, locally root-computed publication
// transaction. PrepareBinding and BuildDataset perform no remote I/O.
type Snapshot struct {
	builder      *builder
	graph        *recordingGraph
	blocks       *recordingBlocks
	remoteGraph  CandidatePublisher
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
	for index, candidate := range s.graph.operations {
		expected := cid.MustParse(candidate.Root)
		got, err := s.remoteGraph.MaterializeAuthentication(ctx, candidate)
		if err != nil {
			return fmt.Errorf("publish encrypted UnixFS candidate %d: %w", index, err)
		}
		if !got.Equals(expected) {
			return fmt.Errorf("remote graph substituted encrypted UnixFS root %s with %s", expected, got)
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

type recordingGraph struct {
	engine     *engine.Engine
	profile    maltcid.ProfileID
	operations []protocol.AuthenticationCandidate
	seen       map[string]bool
}

func newRecordingGraph(backend maltcid.BackendKind) (*recordingGraph, error) {
	var scheme interface {
		commitment.Backend
		ProfileID() maltcid.ProfileID
	}
	var profile maltcid.ProfileID
	var err error
	switch backend {
	case maltcid.BackendKindKZG:
		scheme, err = kzg.NewScheme()
		profile = maltcid.KZG4096
	case maltcid.BackendKindIPA:
		scheme, err = ipa.NewCommitterScheme(ipa.ProfileCompact)
		profile = maltcid.IPA256
	default:
		return nil, fmt.Errorf("encrypted UnixFS snapshot requires a supported MALT backend, got %q", backend)
	}
	if err != nil {
		return nil, err
	}
	profiles := engine.NewRegistry()
	if err := profiles.Register(scheme); err != nil {
		return nil, err
	}
	return &recordingGraph{engine: engine.New(profiles), profile: profile, seen: make(map[string]bool)}, nil
}

var _ GraphWriter = (*recordingGraph)(nil)

// CandidatePublisher stores an exact locally computed authentication candidate.
// Publication is independent of locally accepted roots.
type CandidatePublisher interface {
	MaterializeAuthentication(context.Context, protocol.AuthenticationCandidate) (cid.Cid, error)
}

func (g *recordingGraph) CreateStagedRoot(ctx context.Context, bindings map[string]string) (cid.Cid, error) {
	state := engine.State{Descriptor: maltcid.RootDescriptor{Layout: maltcid.Prefix, DerivationProfile: uint8(derivation.SHA256), Profile: g.profile}}
	names := make([]string, 0, len(bindings))
	for name := range bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		target, err := cid.Decode(bindings[name])
		if err != nil {
			return cid.Undef, err
		}
		selector := []byte(name)
		if name == "@payload" {
			selector = []byte("@payload")
		}
		state.Entries = append(state.Entries, engine.Entry{Label: selector, Target: target})
	}
	return g.record(ctx, state)
}
func (g *recordingGraph) CreateMeasuredPayload(ctx context.Context, chunks []cid.Cid, total, chunk uint64) (cid.Cid, error) {
	state := engine.State{Descriptor: maltcid.RootDescriptor{DerivationProfile: uint8(derivation.Direct), Layout: maltcid.Positional, Profile: g.profile}, ChunkSize: chunk, TotalSize: total}
	for i, target := range chunks {
		state.Entries = append(state.Entries, engine.Entry{Label: coordinate.EncodeIndex(uint64(i)), Target: target})
	}
	return g.record(ctx, state)
}
func (g *recordingGraph) record(ctx context.Context, state engine.State) (cid.Cid, error) {
	candidate, err := authentication.Prepare(ctx, g.engine, state)
	if err != nil {
		return cid.Undef, err
	}
	root := cid.MustParse(candidate.Root)
	if !g.seen[root.KeyString()] {
		g.seen[root.KeyString()] = true
		g.operations = append(g.operations, candidate)
	}
	return root, nil
}

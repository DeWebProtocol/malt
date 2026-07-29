package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dewebprotocol/malt/auth/arcset"
	materializermemory "github.com/dewebprotocol/malt/auth/arcset/materializer/memory"
	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/auth/semantic/list"
	"github.com/dewebprotocol/malt/auth/semantic/mapping"
	runtimegraph "github.com/dewebprotocol/malt/graph/runtime"
	"github.com/dewebprotocol/malt/mutation"
	"github.com/dewebprotocol/malt/protocol"
	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

const (
	commitmentObjectsProfile = "malt.commitment-objects/v1"
	commitmentDeltaProfile   = "malt.commitment-delta/v1"
	commitmentRetainProfile  = "malt.commitment-retain/v1"
	commitmentResultProfile  = "malt.commitment-result/v1"
	maxCommitmentSessions    = 64
	maxCommitmentRetainRoots = 1 << 20
)

var commitmentObjectIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

type commitmentComputer struct {
	mu      sync.Mutex
	backend maltcid.BackendKind
	scheme  commitment.IndexCommitment
	engines map[string]*commitmentEngine
}

type commitmentEngine struct {
	store *materializermemory.Store
	graph *runtimegraph.RuntimeGraph
}

type commitmentObjects struct {
	Profile   string             `json:"profile"`
	SessionID string             `json:"session_id"`
	Objects   []commitmentObject `json:"objects"`
}

type commitmentObject struct {
	ObjectID string                    `json:"object_id"`
	Root     string                    `json:"root"`
	Kind     arcset.Kind               `json:"kind"`
	Entries  []protocol.ArcEntry       `json:"entries"`
	Commit   protocol.CommitDescriptor `json:"commit"`
}

type commitmentDelta struct {
	Profile   string                    `json:"profile"`
	SessionID string                    `json:"session_id"`
	BaseRoot  string                    `json:"base_root"`
	ObjectID  string                    `json:"object_id"`
	OldRoot   protocol.OptionalCID      `json:"old_root"`
	Kind      arcset.Kind               `json:"kind"`
	Changes   []commitmentChange        `json:"changes"`
	Commit    protocol.CommitDescriptor `json:"commit"`
}

type commitmentChange struct {
	Coordinate protocol.Coordinate     `json:"coordinate"`
	Before     protocol.OptionalTarget `json:"before"`
	After      protocol.OptionalTarget `json:"after"`
}

type commitmentLoadResult struct {
	Profile         string `json:"profile"`
	Backend         string `json:"backend"`
	SessionID       string `json:"session_id"`
	VerifiedObjects int    `json:"verified_objects"`
}

type commitmentApplyResult struct {
	Profile      string `json:"profile"`
	Backend      string `json:"backend"`
	SessionID    string `json:"session_id"`
	Root         string `json:"root"`
	CommitmentNS uint64 `json:"commitment_ns"`
}

type commitmentRetain struct {
	Profile   string                 `json:"profile"`
	SessionID string                 `json:"session_id"`
	Objects   []commitmentRetainRoot `json:"objects"`
}

type commitmentRetainRoot struct {
	ObjectID string `json:"object_id"`
	Root     string `json:"root"`
}

type commitmentRetainResult struct {
	Profile       string `json:"profile"`
	Backend       string `json:"backend"`
	SessionID     string `json:"session_id"`
	RetainedRoots int    `json:"retained_roots"`
	RemovedRoots  int    `json:"removed_roots"`
}

type decodedCommitmentObject struct {
	objectID string
	root     cid.Cid
	kind     arcset.Kind
	entries  *arcset.CanonicalArcSet
	commit   mutation.CommitDescriptor
}

func newCommitmentComputer(backend maltcid.BackendKind, scheme commitment.IndexCommitment) (*commitmentComputer, error) {
	if backend != maltcid.BackendKindKZG && backend != maltcid.BackendKindIPA {
		return nil, fmt.Errorf("unsupported commitment backend %q", backend)
	}
	if scheme == nil {
		return nil, fmt.Errorf("%s commitment scheme is nil", backend)
	}
	return &commitmentComputer{
		backend: backend,
		scheme:  scheme,
		engines: make(map[string]*commitmentEngine),
	}, nil
}

func (c *commitmentComputer) newEngine() (*commitmentEngine, error) {
	store := materializermemory.New(true)
	graph, err := runtimegraph.NewGraph(
		"browser-commitment-"+string(c.backend),
		store,
		runtimegraph.WithCommitmentBackend(c.backend, c.scheme),
		runtimegraph.WithDefaultCommitmentBackend(c.backend),
	)
	if err != nil {
		return nil, fmt.Errorf("create %s commitment engine: %w", c.backend, err)
	}
	return &commitmentEngine{store: store, graph: graph}, nil
}

func (c *commitmentComputer) loadObjects(ctx context.Context, raw []byte) ([]byte, error) {
	if c == nil || c.scheme == nil {
		return nil, fmt.Errorf("commitment backend is not initialized")
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	var request commitmentObjects
	if err := decodeCommitmentJSON(raw, &request); err != nil {
		return nil, fmt.Errorf("decode commitment objects: %w", err)
	}
	if request.Profile != commitmentObjectsProfile || request.Objects == nil || len(request.Objects) == 0 ||
		len(request.Objects) > protocol.MaxClientRootObjects {
		return nil, fmt.Errorf("commitment object set is invalid")
	}
	if !commitmentObjectIDPattern.MatchString(request.SessionID) {
		return nil, fmt.Errorf("commitment session id %q is invalid", request.SessionID)
	}
	if _, exists := c.engines[request.SessionID]; !exists && len(c.engines) >= maxCommitmentSessions {
		return nil, fmt.Errorf("commitment backend already retains %d sessions", maxCommitmentSessions)
	}
	engine, err := c.newEngine()
	if err != nil {
		return nil, err
	}
	seenIDs := make(map[string]struct{}, len(request.Objects))
	seenRoots := make(map[string]struct{}, len(request.Objects))
	for index, wireObject := range request.Objects {
		object, err := c.decodeObject(wireObject)
		if err != nil {
			return nil, fmt.Errorf("commitment object %d: %w", index, err)
		}
		if _, exists := seenIDs[object.objectID]; exists {
			return nil, fmt.Errorf("duplicate commitment object id %q", object.objectID)
		}
		if _, exists := seenRoots[object.root.KeyString()]; exists {
			return nil, fmt.Errorf("duplicate commitment object root %s", object.root)
		}
		seenIDs[object.objectID] = struct{}{}
		seenRoots[object.root.KeyString()] = struct{}{}
		recomputed, err := commitCompleteObject(ctx, engine.graph, object)
		if err != nil {
			return nil, fmt.Errorf("verify commitment object %q: %w", object.objectID, err)
		}
		if !recomputed.Equals(object.root) {
			return nil, fmt.Errorf(
				"verify commitment object %q: recomputed root %s does not match declared root %s",
				object.objectID, recomputed, object.root,
			)
		}
		logical, err := logicalArcSet(object)
		if err != nil {
			return nil, fmt.Errorf("seed commitment object %q: %w", object.objectID, err)
		}
		if err := engine.store.Update(ctx, commitmentScope(object.objectID), recomputed, cid.Undef, logical); err != nil {
			return nil, fmt.Errorf("seed commitment object %q: %w", object.objectID, err)
		}
	}
	c.engines[request.SessionID] = engine
	return json.Marshal(commitmentLoadResult{
		Profile: commitmentResultProfile, Backend: string(c.backend),
		SessionID:       request.SessionID,
		VerifiedObjects: len(request.Objects),
	})
}

func (c *commitmentComputer) applyDelta(ctx context.Context, raw []byte) ([]byte, error) {
	if c == nil || c.scheme == nil {
		return nil, fmt.Errorf("commitment backend is not initialized")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var request commitmentDelta
	if err := decodeCommitmentJSON(raw, &request); err != nil {
		return nil, fmt.Errorf("decode commitment delta: %w", err)
	}
	if !commitmentObjectIDPattern.MatchString(request.SessionID) {
		return nil, fmt.Errorf("commitment session id %q is invalid", request.SessionID)
	}
	engine := c.engines[request.SessionID]
	if engine == nil {
		return nil, fmt.Errorf("commitment session %q has no loaded objects", request.SessionID)
	}
	baseRoot, objectRoot, delta, commit, err := c.decodeDelta(request)
	if err != nil {
		return nil, err
	}
	started := time.Now()
	receipt, err := engine.graph.Writer().Apply(ctx, commitmentScope(request.ObjectID), mutation.SemanticMutation{
		BaseRoot: baseRoot,
		Deltas: []mutation.ArcSetDelta{{
			Object: objectRoot, Kind: request.Kind, Changes: delta, Commit: commit,
		}},
	})
	elapsed := time.Since(started)
	if err != nil {
		return nil, fmt.Errorf("apply commitment delta for %q: %w", request.ObjectID, err)
	}
	if maltcid.BackendKindOf(receipt.NewRoot) != c.backend {
		return nil, fmt.Errorf("commitment delta returned backend %q, want %q", maltcid.BackendKindOf(receipt.NewRoot), c.backend)
	}
	return json.Marshal(commitmentApplyResult{
		Profile: commitmentResultProfile, Backend: string(c.backend),
		SessionID: request.SessionID, Root: receipt.NewRoot.String(), CommitmentNS: durationNS(elapsed),
	})
}

func (c *commitmentComputer) retainRoots(raw []byte) ([]byte, error) {
	if c == nil || c.scheme == nil {
		return nil, fmt.Errorf("commitment backend is not initialized")
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	var request commitmentRetain
	if err := decodeCommitmentJSON(raw, &request); err != nil {
		return nil, fmt.Errorf("decode commitment roots: %w", err)
	}
	if request.Profile != commitmentRetainProfile ||
		!commitmentObjectIDPattern.MatchString(request.SessionID) ||
		request.Objects == nil || len(request.Objects) == 0 ||
		len(request.Objects) > maxCommitmentRetainRoots {
		return nil, fmt.Errorf("commitment retained-root set is invalid")
	}
	engine := c.engines[request.SessionID]
	if engine == nil {
		return nil, fmt.Errorf("commitment session %q has no loaded objects", request.SessionID)
	}
	retain := make(map[string][]cid.Cid)
	seen := make(map[string]struct{}, len(request.Objects))
	for index, object := range request.Objects {
		if !commitmentObjectIDPattern.MatchString(object.ObjectID) {
			return nil, fmt.Errorf("commitment retained root %d has invalid object id %q", index, object.ObjectID)
		}
		root, err := parseCanonicalCID(object.Root, "retained root")
		if err != nil {
			return nil, fmt.Errorf("commitment retained root %d: %w", index, err)
		}
		if maltcid.BackendKindOf(root) != c.backend {
			return nil, fmt.Errorf("commitment retained root %d has backend %q, want %q", index, maltcid.BackendKindOf(root), c.backend)
		}
		key := object.ObjectID + "\x00" + root.KeyString()
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		scope := commitmentScope(object.ObjectID)
		retain[scope] = append(retain[scope], root)
	}
	removed := engine.store.RetainRoots(retain)
	return json.Marshal(commitmentRetainResult{
		Profile: commitmentResultProfile, Backend: string(c.backend),
		SessionID: request.SessionID, RetainedRoots: engine.store.RootCount(), RemovedRoots: removed,
	})
}

func (c *commitmentComputer) dropSession(sessionID string) error {
	if c == nil || c.scheme == nil {
		return fmt.Errorf("commitment backend is not initialized")
	}
	if !commitmentObjectIDPattern.MatchString(sessionID) {
		return fmt.Errorf("commitment session id %q is invalid", sessionID)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.engines, sessionID)
	return nil
}

func (c *commitmentComputer) decodeObject(wireObject commitmentObject) (decodedCommitmentObject, error) {
	if !commitmentObjectIDPattern.MatchString(wireObject.ObjectID) {
		return decodedCommitmentObject{}, fmt.Errorf("invalid object id %q", wireObject.ObjectID)
	}
	root, err := parseCanonicalCID(wireObject.Root, "root")
	if err != nil {
		return decodedCommitmentObject{}, err
	}
	if maltcid.BackendKindOf(root) != c.backend || !rootMatchesKind(root, wireObject.Kind) {
		return decodedCommitmentObject{}, fmt.Errorf("root kind/backend does not match compiled %s %s backend", wireObject.Kind, c.backend)
	}
	if wireObject.Entries == nil || len(wireObject.Entries) > protocol.MaxClientRootEntries {
		return decodedCommitmentObject{}, fmt.Errorf("entries are missing or excessive")
	}
	entries := make([]arcset.ArcEntry, len(wireObject.Entries))
	seen := make(map[string]struct{}, len(entries))
	for index, wireEntry := range wireObject.Entries {
		coordinate, err := decodeCoordinate(wireEntry.Coordinate, wireObject.Kind)
		if err != nil {
			return decodedCommitmentObject{}, fmt.Errorf("entry %d coordinate: %w", index, err)
		}
		key := string(coordinate.Bytes())
		if _, exists := seen[key]; exists {
			return decodedCommitmentObject{}, fmt.Errorf("duplicate coordinate %q", wireEntry.Coordinate.MapPath)
		}
		seen[key] = struct{}{}
		target, err := decodeTarget(wireEntry.Target.Kind, wireEntry.Target.CID)
		if err != nil {
			return decodedCommitmentObject{}, fmt.Errorf("entry %d target: %w", index, err)
		}
		entries[index] = arcset.ArcEntry{Coordinate: coordinate, Target: target}
	}
	canonical, err := arcset.NewCanonicalArcSet(wireObject.Kind, entries)
	if err != nil {
		return decodedCommitmentObject{}, err
	}
	commit, err := decodeCommit(wireObject.Commit, wireObject.Kind, uint64(canonical.Len()))
	if err != nil {
		return decodedCommitmentObject{}, err
	}
	return decodedCommitmentObject{
		objectID: wireObject.ObjectID, root: root, kind: wireObject.Kind,
		entries: canonical, commit: commit,
	}, nil
}

func (c *commitmentComputer) decodeDelta(request commitmentDelta) (cid.Cid, cid.Cid, *arcset.CanonicalArcDelta, mutation.CommitDescriptor, error) {
	if request.Profile != commitmentDeltaProfile || !commitmentObjectIDPattern.MatchString(request.ObjectID) ||
		request.Changes == nil || len(request.Changes) == 0 || len(request.Changes) > protocol.MaxClientRootChanges {
		return cid.Undef, cid.Undef, nil, mutation.CommitDescriptor{}, fmt.Errorf("commitment delta is invalid")
	}
	baseRoot, err := parseCanonicalCID(request.BaseRoot, "base_root")
	if err != nil {
		return cid.Undef, cid.Undef, nil, mutation.CommitDescriptor{}, err
	}
	objectRoot, err := decodeOptionalCID(request.OldRoot)
	if err != nil {
		return cid.Undef, cid.Undef, nil, mutation.CommitDescriptor{}, err
	}
	if objectRoot.Defined() && (maltcid.BackendKindOf(objectRoot) != c.backend || !rootMatchesKind(objectRoot, request.Kind)) {
		return cid.Undef, cid.Undef, nil, mutation.CommitDescriptor{}, fmt.Errorf("old root kind/backend does not match compiled %s %s backend", request.Kind, c.backend)
	}
	changes := make([]arcset.ArcChange, len(request.Changes))
	for index, wireChange := range request.Changes {
		coordinate, err := decodeCoordinate(wireChange.Coordinate, request.Kind)
		if err != nil {
			return cid.Undef, cid.Undef, nil, mutation.CommitDescriptor{}, fmt.Errorf("change %d coordinate: %w", index, err)
		}
		before, err := decodeOptionalTarget(wireChange.Before)
		if err != nil {
			return cid.Undef, cid.Undef, nil, mutation.CommitDescriptor{}, fmt.Errorf("change %d before: %w", index, err)
		}
		after, err := decodeOptionalTarget(wireChange.After)
		if err != nil {
			return cid.Undef, cid.Undef, nil, mutation.CommitDescriptor{}, fmt.Errorf("change %d after: %w", index, err)
		}
		changes[index] = arcset.ArcChange{Coordinate: coordinate, Before: before, After: after}
	}
	delta, err := arcset.NewCanonicalArcDelta(request.Kind, changes)
	if err != nil {
		return cid.Undef, cid.Undef, nil, mutation.CommitDescriptor{}, err
	}
	commit, err := decodeCommit(request.Commit, request.Kind, 0)
	if err != nil {
		return cid.Undef, cid.Undef, nil, mutation.CommitDescriptor{}, err
	}
	return baseRoot, objectRoot, delta, commit, nil
}

func commitCompleteObject(ctx context.Context, graph *runtimegraph.RuntimeGraph, object decodedCommitmentObject) (cid.Cid, error) {
	switch object.kind {
	case arcset.KindMap:
		entries := make(map[arcset.Path]cid.Cid, object.entries.Len())
		for _, entry := range object.entries.Entries() {
			entries[arcset.CanonicalizePath(entry.Coordinate.String())] = entry.Target.CID()
		}
		return graph.Semantic().Commit(ctx, commitmentScope(object.objectID), mapping.NewViewFromPaths(entries))
	case arcset.KindList:
		values := make([]cid.Cid, object.entries.Len())
		for index, entry := range object.entries.Entries() {
			raw := entry.Coordinate.Bytes()
			if len(raw) != 8 || wireUint64(raw) != uint64(index) {
				return cid.Undef, fmt.Errorf("list vector is sparse or out of order at %q", entry.Coordinate.String())
			}
			values[index] = entry.Target.CID()
		}
		if object.commit.FixedList == nil {
			return graph.ListSemantic().Commit(ctx, commitmentScope(object.objectID), list.NewViewFromSlice(values))
		}
		fixed, ok := graph.ListSemantic().(list.FixedWidthCommitter)
		if !ok {
			return cid.Undef, fmt.Errorf("list backend does not support fixed-width commits")
		}
		return fixed.CommitFixed(
			ctx, commitmentScope(object.objectID), values,
			object.commit.FixedList.ChunkSize, object.commit.FixedList.TotalSize,
		)
	default:
		return cid.Undef, fmt.Errorf("unsupported object kind %q", object.kind)
	}
}

func logicalArcSet(object decodedCommitmentObject) (arcset.ArcSet, error) {
	values := make(map[arcset.Path]cid.Cid, object.entries.Len())
	for _, entry := range object.entries.Entries() {
		values[arcset.CanonicalizePath(entry.Coordinate.String())] = entry.Target.CID()
	}
	return arcset.NewArcSetFromPaths(values)
}

func decodeCoordinate(value protocol.Coordinate, containingKind arcset.Kind) (arcset.CanonicalCoordinate, error) {
	if value.Kind != containingKind {
		return arcset.CanonicalCoordinate{}, fmt.Errorf("coordinate kind %q does not match containing kind %q", value.Kind, containingKind)
	}
	switch containingKind {
	case arcset.KindMap:
		if value.MapPath == "" || value.ListIndex != 0 {
			return arcset.CanonicalCoordinate{}, fmt.Errorf("map coordinate must carry only map_path")
		}
		coordinate, err := arcset.NewMapCoordinate(value.MapPath)
		if err != nil || coordinate.String() != value.MapPath {
			return arcset.CanonicalCoordinate{}, fmt.Errorf("map coordinate is not canonical")
		}
		return coordinate, nil
	case arcset.KindList:
		if value.MapPath != "" {
			return arcset.CanonicalCoordinate{}, fmt.Errorf("list coordinate must carry only list_index")
		}
		return arcset.NewListCoordinateUint64(value.ListIndex), nil
	default:
		return arcset.CanonicalCoordinate{}, fmt.Errorf("unsupported coordinate kind %q", containingKind)
	}
}

func decodeTarget(kind arcset.TargetKind, rawCID string) (arcset.TargetRef, error) {
	if kind != arcset.TargetKindUnknown && kind != arcset.TargetKindCAS &&
		kind != arcset.TargetKindMap && kind != arcset.TargetKindList {
		return arcset.TargetRef{}, fmt.Errorf("unsupported target kind %q", kind)
	}
	targetCID, err := parseCanonicalCID(rawCID, "target CID")
	if err != nil {
		return arcset.TargetRef{}, err
	}
	semantic := maltcid.SemanticKindOf(targetCID)
	switch kind {
	case arcset.TargetKindMap:
		if semantic != maltcid.SemanticKindMap {
			return arcset.TargetRef{}, fmt.Errorf("map target CID is not a MALT map root")
		}
	case arcset.TargetKindList:
		if semantic != maltcid.SemanticKindList {
			return arcset.TargetRef{}, fmt.Errorf("list target CID is not a MALT list root")
		}
	case arcset.TargetKindUnknown, arcset.TargetKindCAS:
		if semantic != maltcid.SemanticKindUnknown {
			return arcset.TargetRef{}, fmt.Errorf("%s target CID relabels a MALT semantic root", kind)
		}
	}
	return arcset.NewTargetRef(kind, targetCID), nil
}

func decodeOptionalTarget(value protocol.OptionalTarget) (*arcset.TargetRef, error) {
	switch value.State {
	case protocol.PresenceAbsent:
		if value.Kind != "" || value.CID != "" {
			return nil, fmt.Errorf("absent target has companion fields")
		}
		return nil, nil
	case protocol.PresencePresent:
		target, err := decodeTarget(value.Kind, value.CID)
		if err != nil {
			return nil, err
		}
		return &target, nil
	default:
		return nil, fmt.Errorf("unsupported target presence %q", value.State)
	}
}

func decodeOptionalCID(value protocol.OptionalCID) (cid.Cid, error) {
	switch value.State {
	case protocol.PresenceAbsent:
		if value.CID != "" {
			return cid.Undef, fmt.Errorf("absent CID has a companion value")
		}
		return cid.Undef, nil
	case protocol.PresencePresent:
		return parseCanonicalCID(value.CID, "old_root")
	default:
		return cid.Undef, fmt.Errorf("unsupported CID presence %q", value.State)
	}
}

func decodeCommit(value protocol.CommitDescriptor, kind arcset.Kind, entryCount uint64) (mutation.CommitDescriptor, error) {
	switch value.Mode {
	case protocol.CommitModeDefault:
		if value.TotalSize != 0 || value.ChunkSize != 0 {
			return mutation.CommitDescriptor{}, fmt.Errorf("default commit has fixed-list fields")
		}
		return mutation.CommitDescriptor{}, nil
	case protocol.CommitModeFixedList:
		if kind != arcset.KindList || value.ChunkSize == 0 {
			return mutation.CommitDescriptor{}, fmt.Errorf("invalid fixed-list commit")
		}
		if entryCount > 0 {
			want := value.TotalSize / value.ChunkSize
			if value.TotalSize%value.ChunkSize != 0 {
				want++
			}
			if want != entryCount {
				return mutation.CommitDescriptor{}, fmt.Errorf("fixed-list descriptor implies %d entries, got %d", want, entryCount)
			}
		}
		return mutation.CommitDescriptor{FixedList: &mutation.FixedListCommit{
			TotalSize: value.TotalSize, ChunkSize: value.ChunkSize,
		}}, nil
	default:
		return mutation.CommitDescriptor{}, fmt.Errorf("unsupported commit mode %q", value.Mode)
	}
}

func parseCanonicalCID(raw, field string) (cid.Cid, error) {
	value, err := cid.Parse(raw)
	if err != nil || !value.Defined() || value.String() != raw {
		return cid.Undef, fmt.Errorf("%s is not a canonical CID", field)
	}
	return value, nil
}

func rootMatchesKind(root cid.Cid, kind arcset.Kind) bool {
	switch maltcid.SemanticKindOf(root) {
	case maltcid.SemanticKindMap:
		return kind == arcset.KindMap
	case maltcid.SemanticKindList:
		return kind == arcset.KindList
	default:
		return false
	}
}

func decodeCommitmentJSON(data []byte, target any) error {
	if len(data) == 0 || len(data) > protocol.MaxClientRootJSONBytes {
		return fmt.Errorf("commitment JSON size is outside 1..%d", protocol.MaxClientRootJSONBytes)
	}
	if !utf8.Valid(data) {
		return fmt.Errorf("commitment JSON is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON value")
		}
		return fmt.Errorf("unexpected trailing JSON: %w", err)
	}
	return nil
}

func commitmentScope(objectID string) string {
	return "client-root/v1/" + objectID
}

func wireUint64(raw []byte) uint64 {
	return uint64(raw[0])<<56 | uint64(raw[1])<<48 | uint64(raw[2])<<40 | uint64(raw[3])<<32 |
		uint64(raw[4])<<24 | uint64(raw[5])<<16 | uint64(raw[6])<<8 | uint64(raw[7])
}

func durationNS(value time.Duration) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}

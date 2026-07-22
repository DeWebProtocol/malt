// Package writer implements the application-neutral client-root computation
// path. It verifies a bounded complete update view, normalizes an output-free
// dependency graph, computes every changed semantic object bottom-up, and
// returns an exact client-root bundle. Persistence, publication, and trust
// promotion remain outside this package.
package writer

import (
	"context"
	"encoding/binary"
	"fmt"
	"slices"

	"github.com/dewebprotocol/malt/auth/arcset"
	materializer "github.com/dewebprotocol/malt/auth/arcset/materializer"
	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/auth/semantic/list"
	"github.com/dewebprotocol/malt/auth/semantic/mapping"
	runtimegraph "github.com/dewebprotocol/malt/graph/runtime"
	"github.com/dewebprotocol/malt/mutation"
	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

// Runtime owns backend implementations and caller-supplied in-memory or
// persistent-free materialization used for local computation. The materializer
// is not an ArcTable and no result is durable until a service accepts it.
type Runtime struct {
	store  materializer.MutableStore
	graphs map[maltcid.BackendKind]*runtimegraph.RuntimeGraph
}

// VerifiedUpdateView is a normalized view whose complete logical vectors have
// independently recomputed every declared old root in this runtime.
type VerifiedUpdateView struct {
	View mutation.UpdateView
}

// ComputeResult carries both the exact submission bundle and the complete next
// view retained by a long-lived client session.
type ComputeResult struct {
	Bundle   mutation.ClientRootBundle
	NextView mutation.UpdateView
}

// NewRuntime creates a client writer over the narrow materializer capability.
// Callers normally provide the branching in-memory reference materializer;
// SDK code does not choose persistence policy.
func NewRuntime(store materializer.MutableStore, schemes map[maltcid.BackendKind]commitment.IndexCommitment) (*Runtime, error) {
	if store == nil {
		return nil, fmt.Errorf("client writer materializer is nil")
	}
	if len(schemes) == 0 {
		return nil, fmt.Errorf("client writer has no commitment backends")
	}
	runtime := &Runtime{store: store, graphs: make(map[maltcid.BackendKind]*runtimegraph.RuntimeGraph, len(schemes))}
	for _, backend := range []maltcid.BackendKind{maltcid.BackendKindKZG, maltcid.BackendKindIPA} {
		scheme, ok := schemes[backend]
		if !ok {
			continue
		}
		if scheme == nil {
			return nil, fmt.Errorf("client writer %s backend is nil", backend)
		}
		graph, err := runtimegraph.NewGraph(
			"client-root-"+string(backend),
			store,
			runtimegraph.WithCommitmentBackend(backend, scheme),
			runtimegraph.WithDefaultCommitmentBackend(backend),
		)
		if err != nil {
			return nil, fmt.Errorf("create client writer %s backend: %w", backend, err)
		}
		runtime.graphs[backend] = graph
	}
	if len(runtime.graphs) != len(schemes) {
		return nil, fmt.Errorf("client writer schemes contain an unsupported backend")
	}
	return runtime, nil
}

// VerifyUpdateView recomputes every complete logical vector and seeds the
// local semantic materialization required for subsequent incremental updates.
func (r *Runtime) VerifyUpdateView(ctx context.Context, view mutation.UpdateView) (VerifiedUpdateView, error) {
	if r == nil {
		return VerifiedUpdateView{}, fmt.Errorf("client writer runtime is nil")
	}
	canonical, err := mutation.NormalizeUpdateView(view)
	if err != nil {
		return VerifiedUpdateView{}, err
	}
	for _, object := range canonical.Objects {
		if err := ctx.Err(); err != nil {
			return VerifiedUpdateView{}, err
		}
		backend := maltcid.BackendKindOf(object.Root)
		graph, ok := r.graphs[backend]
		if !ok {
			return VerifiedUpdateView{}, fmt.Errorf("update object %q requires unavailable backend %q", object.ObjectID, backend)
		}
		recomputed, err := commitCompleteObject(ctx, graph, objectScope(object.ObjectID), object)
		if err != nil {
			return VerifiedUpdateView{}, fmt.Errorf("verify update object %q: %w", object.ObjectID, err)
		}
		if !recomputed.Equals(object.Root) {
			return VerifiedUpdateView{}, fmt.Errorf("verify update object %q: recomputed root %s does not match declared root %s", object.ObjectID, recomputed, object.Root)
		}
		logical, err := completeObjectArcSet(object)
		if err != nil {
			return VerifiedUpdateView{}, fmt.Errorf("verify update object %q: %w", object.ObjectID, err)
		}
		if err := r.store.Update(ctx, objectScope(object.ObjectID), recomputed, cid.Undef, logical); err != nil {
			return VerifiedUpdateView{}, fmt.Errorf("seed update object %q: %w", object.ObjectID, err)
		}
	}
	return VerifiedUpdateView{View: canonical}, nil
}

// ComputeBundle applies a normalized intent bottom-up and returns the exact
// candidate plus every intermediate output. Literal CAS post-images are
// collected into PayloadCIDs for service-side existence checks.
func (r *Runtime) ComputeBundle(ctx context.Context, operationID string, verified VerifiedUpdateView, intent mutation.SemanticIntent) (ComputeResult, error) {
	if r == nil {
		return ComputeResult{}, fmt.Errorf("client writer runtime is nil")
	}
	view, err := mutation.NormalizeUpdateView(verified.View)
	if err != nil {
		return ComputeResult{}, err
	}
	normalized, err := mutation.NormalizeSemanticIntent(view, intent)
	if err != nil {
		return ComputeResult{}, err
	}
	viewDigest, err := view.Digest()
	if err != nil {
		return ComputeResult{}, err
	}
	intentDigest, err := normalized.Digest()
	if err != nil {
		return ComputeResult{}, err
	}

	objects := make(map[string]mutation.UpdateObject, len(view.Objects)+len(normalized.Transitions))
	for _, object := range view.Objects {
		objects[object.ObjectID] = object
	}
	outputRoots := make(map[string]cid.Cid, len(normalized.Transitions))
	outputs := make([]mutation.TransitionOutput, 0, len(normalized.Transitions))
	payloadSet := make(map[string]cid.Cid)
	for _, transition := range normalized.Transitions {
		if err := ctx.Err(); err != nil {
			return ComputeResult{}, err
		}
		graph, ok := r.graphs[transition.Backend]
		if !ok {
			return ComputeResult{}, fmt.Errorf("transition %q requires unavailable backend %q", transition.ID, transition.Backend)
		}
		changes, err := resolveChanges(transition, outputRoots, payloadSet)
		if err != nil {
			return ComputeResult{}, err
		}
		delta, err := arcset.NewCanonicalArcDelta(transition.Kind, changes)
		if err != nil {
			return ComputeResult{}, fmt.Errorf("transition %q delta: %w", transition.ID, err)
		}
		receipt, err := graph.Writer().Apply(ctx, objectScope(transition.ObjectID), mutation.SemanticMutation{
			BaseRoot: view.BaseRoot,
			Deltas: []mutation.ArcSetDelta{{
				Object:  transition.OldRoot,
				Kind:    transition.Kind,
				Changes: delta,
				Commit:  transition.Commit,
			}},
		})
		if err != nil {
			return ComputeResult{}, fmt.Errorf("compute transition %q: %w", transition.ID, err)
		}
		if maltcid.BackendKindOf(receipt.NewRoot) != transition.Backend {
			return ComputeResult{}, fmt.Errorf("compute transition %q returned backend %q, want %q", transition.ID, maltcid.BackendKindOf(receipt.NewRoot), transition.Backend)
		}
		outputRoots[transition.ID] = receipt.NewRoot
		outputs = append(outputs, mutation.TransitionOutput{TransitionID: transition.ID, Root: receipt.NewRoot})
		postEntries, err := applyCompleteVector(objects[transition.ObjectID].Entries, transition.Kind, changes)
		if err != nil {
			return ComputeResult{}, fmt.Errorf("retain transition %q: %w", transition.ID, err)
		}
		objects[transition.ObjectID] = mutation.UpdateObject{
			ObjectID: transition.ObjectID,
			Root:     receipt.NewRoot,
			Kind:     transition.Kind,
			Entries:  postEntries,
			Commit:   transition.Commit,
		}
	}
	candidate := outputRoots[normalized.TopOutputID]
	payloads := make([]cid.Cid, 0, len(payloadSet))
	for _, payload := range payloadSet {
		payloads = append(payloads, payload)
	}
	slices.SortFunc(payloads, func(a, b cid.Cid) int { return stringCompareBytes(a.Bytes(), b.Bytes()) })
	bundle, err := mutation.NewClientRootBundle(mutation.ClientRootBundle{
		Profile:      mutation.ClientRootBundleProfile,
		OperationID:  operationID,
		View:         view,
		Intent:       normalized,
		Outputs:      outputs,
		Candidate:    candidate,
		PayloadCIDs:  payloads,
		ViewDigest:   viewDigest,
		IntentDigest: intentDigest,
	})
	if err != nil {
		return ComputeResult{}, err
	}
	next, err := nextReachableView(view, candidate, objects)
	if err != nil {
		return ComputeResult{}, err
	}
	return ComputeResult{Bundle: bundle, NextView: next}, nil
}

func commitCompleteObject(ctx context.Context, graph *runtimegraph.RuntimeGraph, scope string, object mutation.UpdateObject) (cid.Cid, error) {
	switch object.Kind {
	case arcset.KindMap:
		entries := make(map[arcset.Path]cid.Cid, object.Entries.Len())
		for _, entry := range object.Entries.Entries() {
			entries[arcset.CanonicalizePath(entry.Coordinate.String())] = entry.Target.CID()
		}
		return graph.Semantic().Commit(ctx, scope, mapping.NewViewFromPaths(entries))
	case arcset.KindList:
		values, err := listValues(object.Entries)
		if err != nil {
			return cid.Undef, err
		}
		if object.Commit.FixedList == nil {
			return graph.ListSemantic().Commit(ctx, scope, list.NewViewFromSlice(values))
		}
		fixed, ok := graph.ListSemantic().(list.FixedWidthCommitter)
		if !ok {
			return cid.Undef, fmt.Errorf("list backend does not support fixed-width commits")
		}
		return fixed.CommitFixed(ctx, scope, values, object.Commit.FixedList.ChunkSize, object.Commit.FixedList.TotalSize)
	default:
		return cid.Undef, fmt.Errorf("unsupported object kind %q", object.Kind)
	}
}

func completeObjectArcSet(object mutation.UpdateObject) (arcset.ArcSet, error) {
	values := make(map[arcset.Path]cid.Cid, object.Entries.Len())
	for _, entry := range object.Entries.Entries() {
		var path arcset.Path
		switch object.Kind {
		case arcset.KindMap:
			path = arcset.CanonicalizePath(entry.Coordinate.String())
		case arcset.KindList:
			path = arcset.CanonicalizePath(entry.Coordinate.String())
		default:
			return nil, fmt.Errorf("unsupported object kind %q", object.Kind)
		}
		values[path] = entry.Target.CID()
	}
	return arcset.NewArcSetFromPaths(values)
}

func resolveChanges(transition mutation.IntentTransition, outputs map[string]cid.Cid, payloads map[string]cid.Cid) ([]arcset.ArcChange, error) {
	changes := make([]arcset.ArcChange, len(transition.Changes))
	for index, change := range transition.Changes {
		resolved := arcset.ArcChange{Coordinate: change.Coordinate, Before: change.Before, After: change.After}
		if change.OutputID != "" {
			root, ok := outputs[change.OutputID]
			if !ok {
				return nil, fmt.Errorf("transition %q consumes unavailable output %q", transition.ID, change.OutputID)
			}
			var target arcset.TargetRef
			switch change.OutputKind {
			case arcset.TargetKindMap:
				target = arcset.NewMapTarget(root)
			case arcset.TargetKindList:
				target = arcset.NewListTarget(root)
			default:
				return nil, fmt.Errorf("transition %q has invalid output kind %q", transition.ID, change.OutputKind)
			}
			resolved.After = &target
		} else if change.After != nil && !isSemanticTarget(*change.After) {
			payloads[change.After.CID().KeyString()] = change.After.CID()
		}
		changes[index] = resolved
	}
	return changes, nil
}

func applyCompleteVector(current *arcset.CanonicalArcSet, kind arcset.Kind, changes []arcset.ArcChange) (*arcset.CanonicalArcSet, error) {
	entries := make(map[string]arcset.ArcEntry)
	if current != nil {
		for _, entry := range current.Entries() {
			entries[string(entry.Coordinate.Bytes())] = entry
		}
	}
	for _, change := range changes {
		key := string(change.Coordinate.Bytes())
		if change.After == nil {
			delete(entries, key)
			continue
		}
		entries[key] = arcset.ArcEntry{Coordinate: change.Coordinate, Target: *change.After}
	}
	values := make([]arcset.ArcEntry, 0, len(entries))
	for _, entry := range entries {
		values = append(values, entry)
	}
	return arcset.NewCanonicalArcSet(kind, values)
}

func nextReachableView(previous mutation.UpdateView, candidate cid.Cid, objects map[string]mutation.UpdateObject) (mutation.UpdateView, error) {
	byRoot := make(map[string]mutation.UpdateObject, len(objects))
	for _, object := range objects {
		byRoot[object.Root.KeyString()] = object
	}
	rootObject, ok := byRoot[candidate.KeyString()]
	if !ok {
		return mutation.UpdateView{}, fmt.Errorf("candidate root has no retained complete vector")
	}
	reachable := make(map[string]mutation.UpdateObject)
	var visit func(mutation.UpdateObject) error
	visit = func(object mutation.UpdateObject) error {
		if _, seen := reachable[object.ObjectID]; seen {
			return nil
		}
		reachable[object.ObjectID] = object
		for _, entry := range object.Entries.Entries() {
			if !isSemanticTarget(entry.Target) {
				continue
			}
			child, exists := byRoot[entry.Target.CID().KeyString()]
			if !exists {
				return fmt.Errorf("retained object %q references missing child %s", object.ObjectID, entry.Target.CID())
			}
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(rootObject); err != nil {
		return mutation.UpdateView{}, err
	}
	next := mutation.UpdateView{
		Profile: previous.Profile, StateProfile: previous.StateProfile,
		BaseRoot: candidate, Bounds: previous.Bounds,
		Objects: make([]mutation.UpdateObject, 0, len(reachable)),
	}
	for _, object := range reachable {
		next.Objects = append(next.Objects, object)
	}
	return mutation.NormalizeUpdateView(next)
}

func listValues(entries *arcset.CanonicalArcSet) ([]cid.Cid, error) {
	values := make([]cid.Cid, entries.Len())
	for index, entry := range entries.Entries() {
		raw := entry.Coordinate.Bytes()
		if len(raw) != 8 || binary.BigEndian.Uint64(raw) != uint64(index) {
			return nil, fmt.Errorf("list vector is sparse or out of order at %q", entry.Coordinate.String())
		}
		values[index] = entry.Target.CID()
	}
	return values, nil
}

func isSemanticTarget(target arcset.TargetRef) bool {
	if target.Kind() == arcset.TargetKindMap || target.Kind() == arcset.TargetKindList {
		return true
	}
	return maltcid.SemanticKindOf(target.CID()) != maltcid.SemanticKindUnknown
}

func objectScope(objectID string) string {
	return "client-root/v1/" + objectID
}

func stringCompareBytes(a, b []byte) int {
	for index := 0; index < len(a) && index < len(b); index++ {
		if a[index] < b[index] {
			return -1
		}
		if a[index] > b[index] {
			return 1
		}
	}
	switch {
	case len(a) < len(b):
		return -1
	case len(a) > len(b):
		return 1
	default:
		return 0
	}
}

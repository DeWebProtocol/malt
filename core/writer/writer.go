// Package writer implements the write-side API for MALT.
// It provides the unified arc update procedure (UpdateArc) described in Sec 4.5,
// coordinating map semantics and ArcTable (index) updates.
package writer

import (
	"context"
	"errors"
	"fmt"

	"github.com/dewebprotocol/malt/core/arctable"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

var (
	// ErrArcNotFound is returned when the target arc does not exist for a replace/delete operation.
	ErrArcNotFound = errors.New("arc not found")

	// ErrInvalidRoot is returned when the structure root is undefined.
	ErrInvalidRoot = errors.New("invalid structure root")

	// ErrEmptyPath is returned when the path is empty.
	ErrEmptyPath = arcset.ErrEmptyPath

	// ErrMissingPayloadBinding is returned when a MALT-native object is missing
	// its mandatory @payload binding.
	ErrMissingPayloadBinding = errors.New("mandatory @payload binding is missing")

	// ErrDeletingPayloadBinding is returned when an update attempts to remove the
	// mandatory @payload binding from a MALT-native object.
	ErrDeletingPayloadBinding = errors.New("mandatory @payload binding cannot be deleted")
)

var mandatoryPayloadPath = arcset.CanonicalizePath("@payload")

// ArcOp describes the type of arc operation performed.
type ArcOp uint8

const (
	// ArcInsert creates a new arc (⊥ → c).
	ArcInsert ArcOp = iota
	// ArcReplace updates an existing arc (c → c').
	ArcReplace
	// ArcDelete removes an arc (c → ⊥).
	ArcDelete
)

func (op ArcOp) String() string {
	switch op {
	case ArcInsert:
		return "insert"
	case ArcReplace:
		return "replace"
	case ArcDelete:
		return "delete"
	default:
		return "unknown"
	}
}

// UpdateResult records the outcome of a single arc update.
type UpdateResult struct {
	// OldRoot is the structure root before the update.
	OldRoot cid.Cid

	// NewRoot is the structure root after the update.
	NewRoot cid.Cid

	// Path is the arc path that was updated.
	Path arcset.Path

	// OldTarget is the previous target CID (cid.Undef if insert).
	OldTarget cid.Cid

	// NewTarget is the new target CID (cid.Undef if delete).
	NewTarget cid.Cid

	// Op is the operation type.
	Op ArcOp
}

// BatchUpdateResult records the outcome of a batch arc update.
type BatchUpdateResult struct {
	// OldRoot is the structure root before the update.
	OldRoot cid.Cid

	// NewRoot is the structure root after the update.
	NewRoot cid.Cid

	// PerArc contains the result for each updated arc.
	PerArc map[arcset.Path]UpdateResult
}

// Writer implements the write-side API for MALT.
// It coordinates keyed-map semantics and ArcTable (index) updates to execute
// the unified arc update procedure from Sec 4.5.
//
// Symmetric to Resolver on the read side:
//   - Resolver: (root, path) -> (target, transcript) via ArcTable lookup + semantic prove
//   - Writer:   (root, path, newTarget) -> newRoot via semantic update + ArcTable apply
type Writer struct {
	semantic mapping.Semantics
	arctable arctable.ArcTable
}

// NewWriter creates a new Writer.
//
// Parameters:
//   - semantic: keyed-map semantic (required)
//   - arctable: Explicit Arc Table (required)
func NewWriter(semantic mapping.Semantics, arctable arctable.ArcTable) *Writer {
	return &Writer{
		semantic: semantic,
		arctable: arctable,
	}
}

func canonicalizeUpdateMap(updates map[string]cid.Cid) (map[arcset.Path]cid.Cid, error) {
	snapshot, err := arcset.NewArcSet(updates)
	if err != nil {
		return nil, err
	}
	return arcset.ToPathMap(snapshot)
}

func canonicalizeSnapshot(arcs arcset.ArcSet) (arcset.ArcSet, map[arcset.Path]cid.Cid, error) {
	if arcs == nil {
		return nil, nil, fmt.Errorf("arc set is nil")
	}

	arcsMap := make(map[arcset.Path]cid.Cid, arcs.Len())
	iter := arcs.Iterate()
	for {
		path, target, ok := iter.Next()
		if !ok {
			break
		}
		if path.IsEmpty() {
			return nil, nil, ErrEmptyPath
		}
		if !target.Defined() {
			continue
		}
		if existing, ok := arcsMap[path]; ok && !existing.Equals(target) {
			return nil, nil, fmt.Errorf("duplicate canonical path %q in arc set", path.String())
		}
		arcsMap[path] = target
	}
	if iter.Err() != nil {
		return nil, nil, fmt.Errorf("arc iteration error: %w", iter.Err())
	}

	normalized, err := arcset.NewArcSetFromPaths(arcsMap)
	if err != nil {
		return nil, nil, err
	}
	return normalized, arcsMap, nil
}

func diffArcMaps(oldArcs, newArcs map[arcset.Path]cid.Cid) (arcset.ArcSet, error) {
	diff := make(map[arcset.Path]cid.Cid)

	for path, newTarget := range newArcs {
		oldTarget, ok := oldArcs[path]
		if !ok || !oldTarget.Equals(newTarget) {
			diff[path] = newTarget
		}
	}

	for path := range oldArcs {
		if _, ok := newArcs[path]; !ok {
			diff[path] = cid.Undef
		}
	}

	return arcset.NewArcSetFromPaths(diff)
}

func filterLogicalArcSet(arcs arcset.ArcSet) arcset.ArcSet {
	if arcs == nil {
		return nil
	}

	out := make(map[arcset.Path]cid.Cid)
	iter := arcs.Iterate()
	for {
		path, target, ok := iter.Next()
		if !ok {
			break
		}
		if path.HasPrefix(arcset.CanonicalizePath("runtime")) {
			continue
		}
		out[path] = target
	}
	filtered, err := arcset.NewArcSetFromPaths(out)
	if err != nil {
		return arcset.NewSet()
	}
	return filtered
}

func hasDefinedPayloadBinding(arcs arcset.ArcSet) bool {
	if arcs == nil {
		return false
	}

	iter := arcs.Iterate()
	for {
		path, target, ok := iter.Next()
		if !ok {
			return false
		}
		if path == mandatoryPayloadPath && target.Defined() {
			return true
		}
	}
}

// UpdateArc executes the unified arc update procedure.
//
// Given a structure root, path, and new target CID, this method:
//  1. Looks up the current binding: c = ArcTable.Get(root, path)
//  2. Updates the commitment: newRoot = semantic.Update(root, path, c, newTarget)
//  3. Applies the update to ArcTable using the full new arc set for newRoot
//
// The operation covers three semantic cases:
//   - Insert (⊥ → c)
//   - Replace (c -> c')
//   - Delete (c → ⊥)
func (w *Writer) UpdateArc(ctx context.Context, namespace string, root cid.Cid, path string, newTarget cid.Cid) (*UpdateResult, error) {
	if !root.Defined() {
		return nil, ErrInvalidRoot
	}
	canonicalPath := arcset.CanonicalizePath(path)
	if canonicalPath.IsEmpty() {
		return nil, ErrEmptyPath
	}
	if canonicalPath == mandatoryPayloadPath && !newTarget.Defined() {
		return nil, ErrDeletingPayloadBinding
	}

	// Step 1: Look up current binding
	oldTarget, err := w.arctable.Get(ctx, namespace, root, canonicalPath)
	if err != nil && !arctable.IsNotFound(err) {
		return nil, fmt.Errorf("ArcTable.Get failed: %w", err)
	}
	// arctable.IsNotFound means oldTarget == cid.Undef, which is valid for insert
	snapshot, err := w.arctable.Snapshot(ctx, namespace, root)
	if err != nil {
		return nil, fmt.Errorf("ArcTable.Snapshot failed: %w", err)
	}
	if !hasDefinedPayloadBinding(snapshot) && !(canonicalPath == mandatoryPayloadPath && newTarget.Defined()) {
		return nil, ErrMissingPayloadBinding
	}

	// Determine operation type
	var op ArcOp
	isInsert := !oldTarget.Defined() && newTarget.Defined()
	isReplace := oldTarget.Defined() && newTarget.Defined()
	isDelete := oldTarget.Defined() && !newTarget.Defined()

	if isInsert {
		op = ArcInsert
	} else if isReplace {
		op = ArcReplace
	} else if isDelete {
		op = ArcDelete
	} else {
		// Both undefined: no-op
		return &UpdateResult{
			OldRoot:   root,
			NewRoot:   root,
			Path:      canonicalPath,
			OldTarget: oldTarget,
			NewTarget: newTarget,
			Op:        ArcInsert,
		}, nil
	}

	var newRoot cid.Cid
	newRoot, err = w.semantic.Update(ctx, namespace, root, canonicalPath, oldTarget, newTarget)
	if err != nil {
		return nil, fmt.Errorf("semantic.Update failed for arc %s: %w", op, err)
	}

	oldArcs, err := arcset.ToPathMap(snapshot)
	if err != nil {
		return nil, err
	}
	updatedArcs := make(map[arcset.Path]cid.Cid, len(oldArcs)+1)
	for path, target := range oldArcs {
		updatedArcs[path] = target
	}
	if newTarget.Defined() {
		updatedArcs[canonicalPath] = newTarget
	} else {
		delete(updatedArcs, canonicalPath)
	}
	delta, err := diffArcMaps(oldArcs, updatedArcs)
	if err != nil {
		return nil, err
	}

	// Step 3: Apply update to ArcTable as an old->new delta. This keeps versioned ArcTable
	// compact while still emitting tombstones for deletions.
	if err := w.arctable.Update(ctx, namespace, newRoot, root, delta); err != nil {
		return nil, fmt.Errorf("ArcTable.Update failed: %w", err)
	}

	return &UpdateResult{
		OldRoot:   root,
		NewRoot:   newRoot,
		Path:      canonicalPath,
		OldTarget: oldTarget,
		NewTarget: newTarget,
		Op:        op,
	}, nil
}

// BatchUpdateArcs updates multiple arcs atomically.
//
// Given a structure root and a map of path → newTarget, this method:
//  1. Looks up all current bindings
//  2. Applies semantic.Update sequentially over the current keyed view
//  3. Applies all updates to ArcTable
func (w *Writer) BatchUpdateArcs(ctx context.Context, namespace string, root cid.Cid, updates map[string]cid.Cid) (*BatchUpdateResult, error) {
	if !root.Defined() {
		return nil, ErrInvalidRoot
	}
	if len(updates) == 0 {
		return nil, fmt.Errorf("updates must not be empty")
	}
	normalizedUpdates, err := canonicalizeUpdateMap(updates)
	if err != nil {
		return nil, err
	}
	if payloadTarget, ok := normalizedUpdates[mandatoryPayloadPath]; ok && !payloadTarget.Defined() {
		return nil, ErrDeletingPayloadBinding
	}

	// Step 1: Get current arc set snapshot
	snapshot, err := w.arctable.Snapshot(ctx, namespace, root)
	if err != nil {
		return nil, fmt.Errorf("ArcTable.Snapshot failed: %w", err)
	}
	if !hasDefinedPayloadBinding(snapshot) {
		payloadTarget, ok := normalizedUpdates[mandatoryPayloadPath]
		if !ok || !payloadTarget.Defined() {
			return nil, ErrMissingPayloadBinding
		}
	}

	// Step 2: Look up all current bindings and classify operations
	perArc := make(map[arcset.Path]UpdateResult, len(normalizedUpdates))

	for path, newTarget := range normalizedUpdates {
		oldTarget, err := w.arctable.Get(ctx, namespace, root, path)
		if err != nil && !arctable.IsNotFound(err) {
			return nil, fmt.Errorf("ArcTable.Get failed for %s: %w", path.String(), err)
		}

		isInsert := !oldTarget.Defined() && newTarget.Defined()
		isDelete := oldTarget.Defined() && !newTarget.Defined()

		var op ArcOp
		if isInsert {
			op = ArcInsert
		} else if oldTarget.Defined() && newTarget.Defined() {
			op = ArcReplace
		} else if isDelete {
			op = ArcDelete
		}

		perArc[path] = UpdateResult{
			OldRoot:   root,
			OldTarget: oldTarget,
			NewTarget: newTarget,
			Op:        op,
			Path:      path,
		}
	}

	// Step 3: Update commitment
	currentRoot := root
	for path, result := range perArc {
		currentRoot, err = w.semantic.Update(ctx, namespace, currentRoot, path, result.OldTarget, result.NewTarget)
		if err != nil {
			return nil, fmt.Errorf("semantic.Update failed for %s: %w", path.String(), err)
		}
	}
	newRoot := currentRoot

	oldArcs, err := arcset.ToPathMap(snapshot)
	if err != nil {
		return nil, err
	}
	updatedArcs := make(map[arcset.Path]cid.Cid, len(oldArcs)+len(normalizedUpdates))
	for path, target := range oldArcs {
		updatedArcs[path] = target
	}
	for path, newTarget := range normalizedUpdates {
		if newTarget.Defined() {
			updatedArcs[path] = newTarget
		} else {
			delete(updatedArcs, path)
		}
	}

	delta, err := diffArcMaps(oldArcs, updatedArcs)
	if err != nil {
		return nil, err
	}

	// Step 4: Apply the update delta to ArcTable.
	if err := w.arctable.Update(ctx, namespace, newRoot, root, delta); err != nil {
		return nil, fmt.Errorf("ArcTable.Update failed: %w", err)
	}

	// Update NewRoot for all per-arc results
	for path := range perArc {
		r := perArc[path]
		r.NewRoot = newRoot
		perArc[path] = r
	}

	return &BatchUpdateResult{
		OldRoot: root,
		NewRoot: newRoot,
		PerArc:  perArc,
	}, nil
}

// CreateStructure creates a new structure from an arc set.
//
// This is the initial commitment operation:
//  1. Commits the arc set via the semantic layer
//  2. Stores arcs in ArcTable (first version, no parent)
func (w *Writer) CreateStructure(ctx context.Context, namespace string, arcs arcset.ArcSet) (cid.Cid, error) {
	if arcs == nil {
		return cid.Undef, fmt.Errorf("arc set is nil")
	}
	normalizedSnapshot, _, err := canonicalizeSnapshot(arcs)
	if err != nil {
		return cid.Undef, err
	}
	if !hasDefinedPayloadBinding(normalizedSnapshot) {
		return cid.Undef, ErrMissingPayloadBinding
	}

	// Step 1: Commit arc set via semantic layer
	view, err := mapping.NewViewFromArcSet(normalizedSnapshot)
	if err != nil {
		return cid.Undef, err
	}
	root, err := w.semantic.Commit(ctx, namespace, view)
	if err != nil {
		return cid.Undef, fmt.Errorf("semantic.Commit failed: %w", err)
	}

	// Step 2: Store arcs in ArcTable (first version)
	if err := w.arctable.Update(ctx, namespace, root, cid.Undef, normalizedSnapshot); err != nil {
		return cid.Undef, fmt.Errorf("ArcTable.Update failed: %w", err)
	}

	return root, nil
}

// GetArc retrieves the current target CID for an arc path.
//
// This is a read-through operation that delegates to ArcTable.
// Returns ErrArcNotFound if the path does not exist.
func (w *Writer) GetArc(ctx context.Context, namespace string, root cid.Cid, path string) (cid.Cid, error) {
	if !root.Defined() {
		return cid.Undef, ErrInvalidRoot
	}
	canonicalPath := arcset.CanonicalizePath(path)
	if canonicalPath.IsEmpty() {
		return cid.Undef, ErrEmptyPath
	}

	target, err := w.arctable.Get(ctx, namespace, root, canonicalPath)
	if err != nil {
		if arctable.IsNotFound(err) {
			return cid.Undef, fmt.Errorf("%s: %w", canonicalPath.String(), ErrArcNotFound)
		}
		return cid.Undef, fmt.Errorf("ArcTable.Get failed: %w", err)
	}

	return target, nil
}

// GetSnapshot retrieves the current arc set snapshot for a structure root.
func (w *Writer) GetSnapshot(ctx context.Context, namespace string, root cid.Cid) (arcset.ArcSet, error) {
	if !root.Defined() {
		return nil, ErrInvalidRoot
	}

	snapshot, err := w.arctable.Snapshot(ctx, namespace, root)
	if err != nil {
		return nil, fmt.Errorf("ArcTable.Snapshot failed: %w", err)
	}

	return filterLogicalArcSet(snapshot), nil
}

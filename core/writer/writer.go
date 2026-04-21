// Package writer implements the write-side API for MALT.
// It provides the unified arc update procedure (UpdateArc) described in Sec 4.5,
// coordinating map semantics, EAT (index), and lineage recording.
package writer

import (
	"context"
	"errors"
	"fmt"

	"github.com/dewebprotocol/malt/core/eat"
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
	ErrEmptyPath = errors.New("path must not be empty")
)

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

// LineageRecorder is an optional interface for recording structure root lineage.
// Implementations track newRoot → oldRoot relationships for versioned resolution.
type LineageRecorder interface {
	// Record records a lineage relationship: newRoot was derived from oldRoot.
	Record(ctx context.Context, bucketId string, newRoot, oldRoot cid.Cid) error
}

// Writer implements the write-side API for MALT.
// It coordinates keyed-map semantics, EAT (index), and optional lineage recording
// to execute the unified arc update procedure from Sec 4.5.
//
// Symmetric to Resolver on the read side:
//   - Resolver: (root, path) -> (target, transcript) via EAT lookup + semantic prove
//   - Writer:   (root, path, newTarget) -> newRoot via semantic update + EAT apply + lineage
type Writer struct {
	semantic mapping.Semantic
	eat      eat.EAT
	rec      LineageRecorder
}

// NewWriter creates a new Writer.
//
// Parameters:
//   - semantic: keyed-map semantic (required)
//   - eat: Explicit Arc Table (required)
//   - rec: optional LineageRecorder for versioned resolution (nil to disable)
func NewWriter(semantic mapping.Semantic, eat eat.EAT, rec LineageRecorder) *Writer {
	return &Writer{
		semantic: semantic,
		eat:      eat,
		rec:      rec,
	}
}

func canonicalizeUpdateMap(updates map[string]cid.Cid) (map[arcset.Path]cid.Cid, error) {
	out := make(map[arcset.Path]cid.Cid, len(updates))
	for path, target := range updates {
		canonical := arcset.CanonicalizePath(path)
		if canonical.IsEmpty() {
			return nil, ErrEmptyPath
		}
		if existing, ok := out[canonical]; ok && !existing.Equals(target) {
			return nil, fmt.Errorf("duplicate canonical path %q in updates", canonical.String())
		}
		out[canonical] = target
	}
	return out, nil
}

func canonicalizeSnapshot(arcs arcset.ArcSet) (arcset.ArcSet, map[arcset.Path]cid.Cid, error) {
	if arcs == nil {
		return nil, nil, fmt.Errorf("arc set is nil")
	}

	arcsMap := make(map[arcset.Path]cid.Cid, arcs.Len())
	stringMap := make(map[string]cid.Cid, arcs.Len())
	iter := arcs.Iterate()
	for {
		path, target, ok := iter.Next()
		if !ok {
			break
		}
		canonical := path
		if canonical.IsEmpty() {
			return nil, nil, ErrEmptyPath
		}
		if !target.Defined() {
			continue
		}
		if existing, ok := arcsMap[canonical]; ok && !existing.Equals(target) {
			return nil, nil, fmt.Errorf("duplicate canonical path %q in arc set", canonical.String())
		}
		arcsMap[canonical] = target
		stringMap[canonical.String()] = target
	}
	if iter.Err() != nil {
		return nil, nil, fmt.Errorf("arc iteration error: %w", iter.Err())
	}

	return arcset.NewSetFrom(stringMap), arcsMap, nil
}

func diffArcMaps(oldArcs, newArcs map[string]cid.Cid) map[string]cid.Cid {
	diff := make(map[string]cid.Cid)

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

	return diff
}

func stringifyArcSet(arcs arcset.ArcSet) map[string]cid.Cid {
	out := make(map[string]cid.Cid, arcs.Len())
	iter := arcs.Iterate()
	for {
		path, target, ok := iter.Next()
		if !ok {
			break
		}
		out[path.String()] = target
	}
	return out
}

func filterLogicalArcSet(arcs arcset.ArcSet) arcset.ArcSet {
	if arcs == nil {
		return nil
	}

	out := make(map[string]cid.Cid)
	iter := arcs.Iterate()
	for {
		path, target, ok := iter.Next()
		if !ok {
			break
		}
		if path.HasPrefix(arcset.CanonicalizePath("runtime")) {
			continue
		}
		out[path.String()] = target
	}
	return arcset.NewSetFrom(out)
}

// UpdateArc executes the unified arc update procedure.
//
// Given a structure root, path, and new target CID, this method:
//  1. Looks up the current binding: c = EAT.Get(root, path)
//  2. Updates the commitment: newRoot = semantic.Update(root, path, c, newTarget)
//  3. Applies the update to EAT using the full new arc set for newRoot
//  4. Records lineage: RecordLineage(newRoot, root)
//
// The operation covers three semantic cases:
//   - Insert (⊥ → c)
//   - Replace (c -> c')
//   - Delete (c → ⊥)
func (w *Writer) UpdateArc(ctx context.Context, bucketId string, root cid.Cid, path string, newTarget cid.Cid) (*UpdateResult, error) {
	if !root.Defined() {
		return nil, ErrInvalidRoot
	}
	canonicalPath := arcset.CanonicalizePath(path)
	if canonicalPath.IsEmpty() {
		return nil, ErrEmptyPath
	}

	// Step 1: Look up current binding
	oldTarget, err := w.eat.Get(ctx, bucketId, root, canonicalPath.String())
	if err != nil && !eat.IsNotFound(err) {
		return nil, fmt.Errorf("EAT.Get failed: %w", err)
	}
	// eat.IsNotFound means oldTarget == cid.Undef, which is valid for insert
	snapshot, err := w.eat.Snapshot(ctx, bucketId, root)
	if err != nil {
		return nil, fmt.Errorf("EAT.Snapshot failed: %w", err)
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
	newRoot, err = w.semantic.Update(ctx, bucketId, root, canonicalPath, oldTarget, newTarget)
	if err != nil {
		return nil, fmt.Errorf("semantic.Update failed for arc %s: %w", op, err)
	}

	oldArcs := stringifyArcSet(snapshot)
	updatedArcs := stringifyArcSet(snapshot)
	if newTarget.Defined() {
		updatedArcs[canonicalPath.String()] = newTarget
	} else {
		delete(updatedArcs, canonicalPath.String())
	}
	delta := diffArcMaps(oldArcs, updatedArcs)

	// Step 3: Apply update to EAT as an old->new delta. This keeps versioned EAT
	// compact while still emitting tombstones for deletions.
	if err := w.eat.Update(ctx, bucketId, newRoot, root, delta); err != nil {
		return nil, fmt.Errorf("EAT.Update failed: %w", err)
	}

	// Step 4: Record lineage
	if w.rec != nil {
		if err := w.rec.Record(ctx, bucketId, newRoot, root); err != nil {
			return nil, fmt.Errorf("LineageRecorder.Record failed: %w", err)
		}
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
//  3. Applies all updates to EAT
//  4. Records lineage: RecordLineage(newRoot, root)
func (w *Writer) BatchUpdateArcs(ctx context.Context, bucketId string, root cid.Cid, updates map[string]cid.Cid) (*BatchUpdateResult, error) {
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

	// Step 1: Get current arc set snapshot
	snapshot, err := w.eat.Snapshot(ctx, bucketId, root)
	if err != nil {
		return nil, fmt.Errorf("EAT.Snapshot failed: %w", err)
	}

	// Step 2: Look up all current bindings and classify operations
	perArc := make(map[arcset.Path]UpdateResult, len(normalizedUpdates))

	for path, newTarget := range normalizedUpdates {
		oldTarget, err := w.eat.Get(ctx, bucketId, root, path.String())
		if err != nil && !eat.IsNotFound(err) {
			return nil, fmt.Errorf("EAT.Get failed for %s: %w", path.String(), err)
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
	currentMap := stringifyArcSet(snapshot)
	for path, result := range perArc {
		currentRoot, err = w.semantic.Update(ctx, bucketId, currentRoot, path, result.OldTarget, result.NewTarget)
		if err != nil {
			return nil, fmt.Errorf("semantic.Update failed for %s: %w", path.String(), err)
		}
		if result.NewTarget.Defined() {
			currentMap[path.String()] = result.NewTarget
		} else {
			delete(currentMap, path.String())
		}
	}
	newRoot := currentRoot

	oldArcs := stringifyArcSet(snapshot)
	updatedArcs := stringifyArcSet(snapshot)
	for path, newTarget := range normalizedUpdates {
		if newTarget.Defined() {
			updatedArcs[path.String()] = newTarget
		} else {
			delete(updatedArcs, path.String())
		}
	}

	delta := diffArcMaps(oldArcs, updatedArcs)

	// Step 4: Apply the update delta to EAT.
	if err := w.eat.Update(ctx, bucketId, newRoot, root, delta); err != nil {
		return nil, fmt.Errorf("EAT.Update failed: %w", err)
	}

	// Step 5: Record lineage
	if w.rec != nil {
		if err := w.rec.Record(ctx, bucketId, newRoot, root); err != nil {
			return nil, fmt.Errorf("LineageRecorder.Record failed: %w", err)
		}
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
//  2. Stores arcs in EAT (first version, no parent)
//  3. Records lineage with cid.Undef as parent
func (w *Writer) CreateStructure(ctx context.Context, bucketId string, arcs arcset.ArcSet) (cid.Cid, error) {
	if arcs == nil {
		return cid.Undef, fmt.Errorf("arc set is nil")
	}
	normalizedSnapshot, _, err := canonicalizeSnapshot(arcs)
	if err != nil {
		return cid.Undef, err
	}

	// Step 1: Commit arc set via semantic layer
	root, err := w.semantic.Commit(ctx, bucketId, mapping.NewViewFrom(stringifyArcSet(normalizedSnapshot)))
	if err != nil {
		return cid.Undef, fmt.Errorf("semantic.Commit failed: %w", err)
	}

	// Step 2: Store arcs in EAT (first version)
	if err := w.eat.Update(ctx, bucketId, root, cid.Undef, stringifyArcSet(normalizedSnapshot)); err != nil {
		return cid.Undef, fmt.Errorf("EAT.Update failed: %w", err)
	}

	// Step 3: Record lineage (root → cid.Undef means initial creation)
	if w.rec != nil {
		if err := w.rec.Record(ctx, bucketId, root, cid.Undef); err != nil {
			return cid.Undef, fmt.Errorf("LineageRecorder.Record failed: %w", err)
		}
	}

	return root, nil
}

// GetArc retrieves the current target CID for an arc path.
//
// This is a read-through operation that delegates to EAT.
// Returns ErrArcNotFound if the path does not exist.
func (w *Writer) GetArc(ctx context.Context, bucketId string, root cid.Cid, path string) (cid.Cid, error) {
	if !root.Defined() {
		return cid.Undef, ErrInvalidRoot
	}
	canonicalPath := arcset.CanonicalizePath(path)
	if canonicalPath.IsEmpty() {
		return cid.Undef, ErrEmptyPath
	}

	target, err := w.eat.Get(ctx, bucketId, root, canonicalPath.String())
	if err != nil {
		if eat.IsNotFound(err) {
			return cid.Undef, fmt.Errorf("%s: %w", canonicalPath.String(), ErrArcNotFound)
		}
		return cid.Undef, fmt.Errorf("EAT.Get failed: %w", err)
	}

	return target, nil
}

// GetSnapshot retrieves the current arc set snapshot for a structure root.
func (w *Writer) GetSnapshot(ctx context.Context, bucketId string, root cid.Cid) (arcset.ArcSet, error) {
	if !root.Defined() {
		return nil, ErrInvalidRoot
	}

	snapshot, err := w.eat.Snapshot(ctx, bucketId, root)
	if err != nil {
		return nil, fmt.Errorf("EAT.Snapshot failed: %w", err)
	}

	return filterLogicalArcSet(snapshot), nil
}

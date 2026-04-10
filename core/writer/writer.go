// Package writer implements the write-side API for MALT.
// It provides the unified arc update procedure (UpdateArc) described in Sec 4.5,
// coordinating SCE (commitment), EAT (index), and lineage recording.
package writer

import (
	"context"
	"errors"
	"fmt"

	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/sce"
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
	Path string

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
	PerArc map[string]UpdateResult
}

// LineageRecorder is an optional interface for recording structure root lineage.
// Implementations track newRoot → oldRoot relationships for versioned resolution.
type LineageRecorder interface {
	// Record records a lineage relationship: newRoot was derived from oldRoot.
	Record(ctx context.Context, bucketId string, newRoot, oldRoot cid.Cid) error
}

// Writer implements the write-side API for MALT.
// It coordinates SCE (commitment), EAT (index), and optional lineage recording
// to execute the unified arc update procedure from Sec 4.5.
//
// Symmetric to Resolver on the read side:
//   - Resolver: (root, path) → (target, transcript) via EAT lookup + SCE prove
//   - Writer:   (root, path, newTarget) → newRoot via SCE update + EAT apply + lineage
type Writer struct {
	sce    *sce.Engine
	eat    eat.EAT
	rec    LineageRecorder
}

// NewWriter creates a new Writer.
//
// Parameters:
//   - sce: Structure Commitment Engine (required)
//   - eat: Explicit Arc Table (required)
//   - rec: optional LineageRecorder for versioned resolution (nil to disable)
func NewWriter(sce *sce.Engine, eat eat.EAT, rec LineageRecorder) *Writer {
	return &Writer{
		sce: sce,
		eat: eat,
		rec: rec,
	}
}

// UpdateArc executes the unified arc update procedure.
//
// Given a structure root, path, and new target CID, this method:
//  1. Looks up the current binding: c = EAT.Get(root, path)
//  2. Updates the commitment: newRoot = SCE.Update(root, path, c, newTarget)
//     or for inserts: newRoot = SCE.Commit(expanded arc set)
//  3. Applies the update to EAT: EAT.Update(newRoot, oldRoot, {path: newTarget})
//  4. Records lineage: RecordLineage(newRoot, root)
//
// The operation covers three semantic cases:
//   - Insert (⊥ → c): path has no current binding; recommit with expanded arc set
//   - Replace (c → c'): path has an existing binding; use SCE.Update
//   - Delete (c → ⊥): newTarget is cid.Undef; recommit with reduced arc set
func (w *Writer) UpdateArc(ctx context.Context, bucketId string, root cid.Cid, path string, newTarget cid.Cid) (*UpdateResult, error) {
	if !root.Defined() {
		return nil, ErrInvalidRoot
	}
	if path == "" {
		return nil, ErrEmptyPath
	}

	// Step 1: Look up current binding
	oldTarget, err := w.eat.Get(ctx, bucketId, root, path)
	if err != nil && !eat.IsNotFound(err) {
		return nil, fmt.Errorf("EAT.Get failed: %w", err)
	}
	// eat.IsNotFound means oldTarget == cid.Undef, which is valid for insert

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
			Path:      path,
			OldTarget: oldTarget,
			NewTarget: newTarget,
			Op:        ArcInsert,
		}, nil
	}

	var newRoot cid.Cid

	if isInsert || isDelete {
		// Insert or delete: requires recommit with modified arc set
		// because SCE.Update only supports replacing existing paths.
		snapshot, err := w.eat.Snapshot(ctx, bucketId, root)
		if err != nil {
			return nil, fmt.Errorf("EAT.Snapshot failed: %w", err)
		}

		// Build modified arc set
		arcsMap := make(map[string]cid.Cid)
		iter := snapshot.Iterate()
		for {
			p, t, ok := iter.Next()
			if !ok {
				break
			}
			arcsMap[p] = t
		}
		if iter.Err() != nil {
			return nil, fmt.Errorf("arc iteration error: %w", iter.Err())
		}

		// Apply the change
		if isInsert {
			arcsMap[path] = newTarget
		} else {
			// isDelete
			delete(arcsMap, path)
		}

		newRoot, err = w.sce.Commit(arcset.NewMapFrom(arcsMap))
		if err != nil {
			return nil, fmt.Errorf("SCE.Commit failed for arc %s: %w", op, err)
		}
	} else {
		// Replace: use SCE.Update for efficient in-place modification
		snapshot, err := w.eat.Snapshot(ctx, bucketId, root)
		if err != nil {
			return nil, fmt.Errorf("EAT.Snapshot failed: %w", err)
		}

		newRoot, err = w.sce.Update(root, snapshot, path, oldTarget, newTarget)
		if err != nil {
			return nil, fmt.Errorf("SCE.Update failed: %w", err)
		}
	}

	// Step 3: Apply update to EAT
	arcsMap := map[string]cid.Cid{path: newTarget}
	if err := w.eat.Update(ctx, bucketId, newRoot, root, arcsMap); err != nil {
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
		Path:      path,
		OldTarget: oldTarget,
		NewTarget: newTarget,
		Op:        op,
	}, nil
}

// BatchUpdateArcs updates multiple arcs atomically.
//
// Given a structure root and a map of path → newTarget, this method:
//  1. Looks up all current bindings
//  2. If all operations are replacements: newRoot = SCE.BatchUpdate(root, updates)
//  3. If any insert or delete is present: recommit with modified arc set
//  4. Applies all updates to EAT
//  5. Records lineage: RecordLineage(newRoot, root)
func (w *Writer) BatchUpdateArcs(ctx context.Context, bucketId string, root cid.Cid, updates map[string]cid.Cid) (*BatchUpdateResult, error) {
	if !root.Defined() {
		return nil, ErrInvalidRoot
	}
	if len(updates) == 0 {
		return nil, fmt.Errorf("updates must not be empty")
	}

	// Step 1: Get current arc set snapshot
	snapshot, err := w.eat.Snapshot(ctx, bucketId, root)
	if err != nil {
		return nil, fmt.Errorf("EAT.Snapshot failed: %w", err)
	}

	// Step 2: Look up all current bindings and classify operations
	perArc := make(map[string]UpdateResult, len(updates))
	sceUpdates := make(map[string]struct {
		Old cid.Cid
		New cid.Cid
	}, len(updates))
	needsRecommit := false

	for path, newTarget := range updates {
		oldTarget, err := w.eat.Get(ctx, bucketId, root, path)
		if err != nil && !eat.IsNotFound(err) {
			return nil, fmt.Errorf("EAT.Get failed for %s: %w", path, err)
		}

		isInsert := !oldTarget.Defined() && newTarget.Defined()
		isDelete := oldTarget.Defined() && !newTarget.Defined()

		if isInsert || isDelete {
			needsRecommit = true
		}

		var op ArcOp
		if isInsert {
			op = ArcInsert
		} else if oldTarget.Defined() && newTarget.Defined() {
			op = ArcReplace
		} else if isDelete {
			op = ArcDelete
		}

		sceUpdates[path] = struct {
			Old cid.Cid
			New cid.Cid
		}{Old: oldTarget, New: newTarget}

		perArc[path] = UpdateResult{
			OldRoot:   root,
			OldTarget: oldTarget,
			NewTarget: newTarget,
			Op:        op,
			Path:      path,
		}
	}

	// Step 3: Update commitment
	var newRoot cid.Cid

	if needsRecommit {
		// Some operations are inserts or deletes; recommit with modified arc set
		arcsMap := make(map[string]cid.Cid)
		iter := snapshot.Iterate()
		for {
			p, t, ok := iter.Next()
			if !ok {
				break
			}
			arcsMap[p] = t
		}
		if iter.Err() != nil {
			return nil, fmt.Errorf("arc iteration error: %w", iter.Err())
		}

		for path, newTarget := range updates {
			if newTarget.Defined() {
				arcsMap[path] = newTarget
			} else {
				delete(arcsMap, path)
			}
		}

		newRoot, err = w.sce.Commit(arcset.NewMapFrom(arcsMap))
		if err != nil {
			return nil, fmt.Errorf("SCE.Commit failed for batch: %w", err)
		}
	} else {
		// All replacements; use SCE.BatchUpdate
		// Convert sceUpdates format
		batchSCEUpdates := make(map[string]struct {
			Old cid.Cid
			New cid.Cid
		}, len(updates))
		for path, u := range sceUpdates {
			batchSCEUpdates[path] = u
		}

		newRoot, err = w.sce.BatchUpdate(root, snapshot, batchSCEUpdates)
		if err != nil {
			return nil, fmt.Errorf("SCE.BatchUpdate failed: %w", err)
		}
	}

	// Step 4: Apply all updates to EAT
	if err := w.eat.Update(ctx, bucketId, newRoot, root, updates); err != nil {
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
//  1. Commits the arc set via SCE
//  2. Stores arcs in EAT (first version, no parent)
//  3. Records lineage with cid.Undef as parent
func (w *Writer) CreateStructure(ctx context.Context, bucketId string, arcs arcset.Snapshot) (cid.Cid, error) {
	if arcs == nil {
		return cid.Undef, fmt.Errorf("arc set is nil")
	}

	// Step 1: Commit arc set via SCE
	root, err := w.sce.Commit(arcs)
	if err != nil {
		return cid.Undef, fmt.Errorf("SCE.Commit failed: %w", err)
	}

	// Step 2: Store arcs in EAT (first version)
	arcsMap := make(map[string]cid.Cid)
	iter := arcs.Iterate()
	for {
		path, target, ok := iter.Next()
		if !ok {
			break
		}
		arcsMap[path] = target
	}
	if iter.Err() != nil {
		return cid.Undef, fmt.Errorf("arc iteration error: %w", iter.Err())
	}

	if err := w.eat.Update(ctx, bucketId, root, cid.Undef, arcsMap); err != nil {
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
	if path == "" {
		return cid.Undef, ErrEmptyPath
	}

	target, err := w.eat.Get(ctx, bucketId, root, path)
	if err != nil {
		if eat.IsNotFound(err) {
			return cid.Undef, fmt.Errorf("%s: %w", path, ErrArcNotFound)
		}
		return cid.Undef, fmt.Errorf("EAT.Get failed: %w", err)
	}

	return target, nil
}

// GetSnapshot retrieves the current arc set snapshot for a structure root.
func (w *Writer) GetSnapshot(ctx context.Context, bucketId string, root cid.Cid) (arcset.Snapshot, error) {
	if !root.Defined() {
		return nil, ErrInvalidRoot
	}

	snapshot, err := w.eat.Snapshot(ctx, bucketId, root)
	if err != nil {
		return nil, fmt.Errorf("EAT.Snapshot failed: %w", err)
	}

	return snapshot, nil
}

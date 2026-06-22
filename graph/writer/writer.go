// Package writer implements the write-side API for MALT.
// It provides the unified arc update procedure (UpdateArc) described in Sec 4.5,
// coordinating map semantics and ArcTable (index) updates.
package writer

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/auth/semantic/list"
	"github.com/dewebprotocol/malt/auth/semantic/mapping"
	"github.com/dewebprotocol/malt/runtime/arctable"
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

	// ErrStaleRoot is returned when a legacy root-consuming write attempts to
	// update a base root that this writer has already advanced in the same
	// namespace.
	ErrStaleRoot = errors.New("stale root")

	// ErrIndexWriteFailed is returned when the semantic layer produced a new
	// root but the ArcTable index write failed. The semantic commitment is
	// content-addressed and cannot be rolled back, so the returned newRoot is a
	// cryptographically valid root whose ArcTable entries may be missing.
	//
	// To recover, use errors.As to recover IndexWriteFailedError and call
	// Writer.RetryIndexWrite. The error carries the exact old->new ArcTable
	// transition because retrying the original writer operation is not
	// guaranteed to work after a non-atomic backend has partially applied the
	// failed index update (for example, after invalidating the old root mapping).
	ErrIndexWriteFailed = errors.New("arctable index write failed after semantic commit")
)

// IndexWriteFailedError carries the newRoot produced by the semantic layer when
// the ArcTable index write failed. errors.Is(err, ErrIndexWriteFailed) and
// errors.Is(err, <underlying cause>) both succeed.
//
// The fields are exposed for diagnostics, correlation, and index repair.
// IndexBase and IndexDelta capture the failed publication so callers can retry
// it with Writer.RetryIndexWrite. For non-branching ArcTable backends, retry
// first verifies that the namespace has not advanced past this failed
// transition; stale retries fail closed instead of overwriting later writes.
type IndexWriteFailedError struct {
	// NewRoot is the content-addressed root the semantic layer committed. It is
	// valid in the semantic sense, but its ArcTable index entries may not have
	// been persisted.
	NewRoot cid.Cid
	// Namespace is the namespace of the failed index write, for retry context.
	Namespace string
	// OldRoot is the base root the write transitioned from (cid.Undef for
	// CreateStructure).
	OldRoot cid.Cid
	// IndexBase is the namespace arc snapshot immediately before the failed
	// ArcTable publication. It is used to reject stale retries on overwrite-like
	// backends whose physical arc keys are namespace-scoped rather than
	// root-scoped.
	IndexBase arcset.ArcSet
	// IndexDelta is the exact ArcTable update payload for OldRoot -> NewRoot.
	// It may be a full snapshot for create-style writes.
	IndexDelta arcset.ArcSet
	// Cause is the error returned by ArcTable.Update.
	Cause error
}

func (e *IndexWriteFailedError) Error() string {
	return fmt.Errorf("%w (newRoot=%s): %v", ErrIndexWriteFailed, e.NewRoot, e.Cause).Error()
}

func (e *IndexWriteFailedError) Unwrap() error {
	return errors.Join(ErrIndexWriteFailed, e.Cause)
}

// RetryIndexWrite retries the failed ArcTable publication captured by this
// error. Prefer Writer.RetryIndexWrite when a writer is available: the writer
// method reuses the same freshness guard as normal updates and serializes retry
// against concurrent root-consuming writes.
func (e *IndexWriteFailedError) RetryIndexWrite(ctx context.Context, table arctable.ArcTable) error {
	return e.retryIndexWrite(ctx, table)
}

func (e *IndexWriteFailedError) retryIndexWrite(ctx context.Context, table arctable.ArcTable) error {
	if e == nil {
		return fmt.Errorf("index write failure is nil")
	}
	if table == nil {
		return fmt.Errorf("arctable is nil")
	}
	if e.IndexDelta == nil {
		return fmt.Errorf("%w: missing index delta", ErrIndexWriteFailed)
	}
	if !supportsConcurrentBranches(table) {
		if e.IndexBase == nil {
			return fmt.Errorf("%w: missing index retry base", ErrIndexWriteFailed)
		}
		current, err := table.Snapshot(ctx, e.Namespace, cid.Undef)
		if err != nil {
			return fmt.Errorf("ArcTable.Snapshot failed during index retry: %w", err)
		}
		expectedAfter, err := applyArcSetDelta(e.IndexBase, e.IndexDelta)
		if err != nil {
			return err
		}
		matchesBase, err := arcSetsEqual(current, e.IndexBase)
		if err != nil {
			return err
		}
		matchesAfter, err := arcSetsEqual(current, expectedAfter)
		if err != nil {
			return err
		}
		if !matchesBase && !matchesAfter {
			return fmt.Errorf("%w: stale index retry for namespace %q oldRoot=%s newRoot=%s", ErrStaleRoot, e.Namespace, e.OldRoot, e.NewRoot)
		}
	}
	if err := table.Update(ctx, e.Namespace, e.NewRoot, e.OldRoot, e.IndexDelta); err != nil {
		return &IndexWriteFailedError{
			NewRoot:    e.NewRoot,
			Namespace:  e.Namespace,
			OldRoot:    e.OldRoot,
			IndexBase:  e.IndexBase,
			IndexDelta: e.IndexDelta,
			Cause:      err,
		}
	}
	return nil
}

func indexRetryBase(ctx context.Context, table arctable.ArcTable, namespace string) (arcset.ArcSet, error) {
	if table == nil || supportsConcurrentBranches(table) {
		return nil, nil
	}
	// Overwrite-like backends need a namespace snapshot so retry can reject
	// stale replays after a non-atomic index write failure.
	return table.Snapshot(ctx, namespace, cid.Undef)
}

func applyArcSetDelta(base, delta arcset.ArcSet) (arcset.ArcSet, error) {
	baseMap, err := arcset.ToPathMap(base)
	if err != nil {
		return nil, err
	}
	deltaMap, err := arcset.ToPathMap(delta)
	if err != nil {
		return nil, err
	}
	for path, target := range deltaMap {
		if target.Defined() {
			baseMap[path] = target
		} else {
			delete(baseMap, path)
		}
	}
	return arcset.NewArcSetFromPaths(baseMap)
}

func arcSetsEqual(a, b arcset.ArcSet) (bool, error) {
	aMap, err := arcset.ToPathMap(a)
	if err != nil {
		return false, err
	}
	bMap, err := arcset.ToPathMap(b)
	if err != nil {
		return false, err
	}
	if len(aMap) != len(bMap) {
		return false, nil
	}
	for path, aTarget := range aMap {
		bTarget, ok := bMap[path]
		if !ok || !aTarget.Equals(bTarget) {
			return false, nil
		}
	}
	return true, nil
}

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
// The legacy UpdateArc/BatchUpdateArcs paths only enforce single-consumer root
// freshness for non-branching ArcTable backends. MVCC-style backends such as
// versioned ArcTable remain branchable and may accept multiple children from
// the same parent root.
//
// Symmetric to Resolver on the read side:
//   - Resolver: (root, path) -> (target, transcript) via ArcTable lookup + semantic prove
//   - Writer:   (root, path, newTarget) -> newRoot via semantic update + ArcTable apply
type Writer struct {
	semantic     mapping.Semantics
	listSemantic list.Semantics
	arctable     arctable.ArcTable
	freshness    *rootFreshnessGuard
}

// NewWriter creates a new Writer.
//
// Parameters:
//   - semantic: keyed-map semantic (required)
//   - arctable: Explicit Arc Table (required)
func NewWriter(semantic mapping.Semantics, arctable arctable.ArcTable, lists ...list.Semantics) *Writer {
	var listSemantic list.Semantics
	if len(lists) > 0 {
		listSemantic = lists[0]
	}
	return &Writer{
		semantic:     semantic,
		listSemantic: listSemantic,
		arctable:     arctable,
		freshness:    rootFreshnessGuardFor(arctable),
	}
}

// RetryIndexWrite retries a failed writer-level ArcTable publication.
//
// The retry is serialized through the same freshness guard as UpdateArc and
// BatchUpdateArcs for non-branching ArcTable backends. This closes the window
// where a captured failed retry could race with a later root-consuming write for
// the same (namespace, oldRoot) and overwrite namespace-scoped arc entries.
func (w *Writer) RetryIndexWrite(ctx context.Context, err *IndexWriteFailedError) error {
	if w == nil {
		return fmt.Errorf("writer is nil")
	}
	if err == nil {
		return fmt.Errorf("index write failure is nil")
	}
	if w.freshness != nil && err.OldRoot.Defined() {
		unlock, beginErr := w.freshness.beginUpdate(err.Namespace, err.OldRoot)
		if beginErr != nil {
			return beginErr
		}
		defer unlock()
	}
	if retryErr := err.retryIndexWrite(ctx, w.arctable); retryErr != nil {
		return retryErr
	}
	if w.freshness != nil {
		if err.OldRoot.Defined() {
			w.freshness.markAdvancedLocked(err.Namespace, err.OldRoot, err.NewRoot)
		} else {
			w.freshness.markCurrent(err.Namespace, err.NewRoot)
		}
	}
	return nil
}

var sharedFreshnessGuards sync.Map

type rootFreshnessGuard struct {
	mu       sync.Mutex
	consumed map[string]cid.Cid
}

func newRootFreshnessGuard() *rootFreshnessGuard {
	return &rootFreshnessGuard{
		consumed: make(map[string]cid.Cid),
	}
}

func sharedRootFreshnessGuard(table arctable.ArcTable) *rootFreshnessGuard {
	key, ok := arctableFreshnessIdentity(table)
	if !ok {
		return newRootFreshnessGuard()
	}
	guard, _ := sharedFreshnessGuards.LoadOrStore(key, newRootFreshnessGuard())
	return guard.(*rootFreshnessGuard)
}

func arctableFreshnessIdentity(table arctable.ArcTable) (any, bool) {
	if table == nil {
		return nil, false
	}
	if !reflect.TypeOf(table).Comparable() {
		return nil, false
	}
	return table, true
}

func rootFreshnessGuardFor(table arctable.ArcTable) *rootFreshnessGuard {
	if supportsConcurrentBranches(table) {
		return nil
	}
	return sharedRootFreshnessGuard(table)
}

func supportsConcurrentBranches(table arctable.ArcTable) bool {
	if table == nil {
		return false
	}
	branching, ok := table.(arctable.BranchingArcTable)
	return ok && branching.SupportsConcurrentBranches()
}

func freshnessKey(namespace string, root cid.Cid) string {
	return namespace + "\x00" + root.String()
}

func (g *rootFreshnessGuard) beginUpdate(namespace string, root cid.Cid) (func(), error) {
	key := freshnessKey(namespace, root)
	g.mu.Lock()
	advancedTo, ok := g.consumed[key]
	if !ok {
		return g.mu.Unlock, nil
	}
	g.mu.Unlock()
	return nil, fmt.Errorf("%w: root %s in namespace %q already advanced to %s", ErrStaleRoot, root, namespace, advancedTo)
}

func (g *rootFreshnessGuard) markAdvanced(namespace string, oldRoot, newRoot cid.Cid) {
	if !oldRoot.Defined() || !newRoot.Defined() || oldRoot.Equals(newRoot) {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.markAdvancedLocked(namespace, oldRoot, newRoot)
}

func (g *rootFreshnessGuard) markAdvancedLocked(namespace string, oldRoot, newRoot cid.Cid) {
	if !oldRoot.Defined() || !newRoot.Defined() || oldRoot.Equals(newRoot) {
		return
	}
	oldKey := freshnessKey(namespace, oldRoot)
	newKey := freshnessKey(namespace, newRoot)
	g.consumed[oldKey] = newRoot
	delete(g.consumed, newKey)
}

func (g *rootFreshnessGuard) markCurrent(namespace string, root cid.Cid) {
	if !root.Defined() {
		return
	}
	g.mu.Lock()
	delete(g.consumed, freshnessKey(namespace, root))
	g.mu.Unlock()
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

func filterLogicalArcSet(arcs arcset.ArcSet) (arcset.ArcSet, error) {
	if arcs == nil {
		return nil, nil
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
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("arc iteration error: %w", err)
	}
	filtered, err := arcset.NewArcSetFromPaths(out)
	if err != nil {
		return nil, err
	}
	return filtered, nil
}

func hasDefinedPayloadBinding(arcs arcset.ArcSet) (bool, error) {
	if arcs == nil {
		return false, nil
	}

	iter := arcs.Iterate()
	found := false
	for {
		path, target, ok := iter.Next()
		if !ok {
			if err := iter.Err(); err != nil {
				return false, fmt.Errorf("arc iteration error: %w", err)
			}
			return found, nil
		}
		if path == mandatoryPayloadPath && target.Defined() {
			found = true
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
// UpdateArc is a legacy root-consuming API. Within one process, writers that
// share the same ArcTable instance advance (namespace, root) exactly once for
// successful non-no-op calls; later attempts to update the same base root return
// ErrStaleRoot instead of silently forking or overwriting ArcTable state.
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

	if w.freshness != nil {
		unlock, err := w.freshness.beginUpdate(namespace, root)
		if err != nil {
			return nil, err
		}
		defer unlock()
	}

	// Step 1: Snapshot the current arc set for delta computation and payload
	// validation, then read the target path through ArcTable.Get for operation
	// classification. Get must stay in the classification path because some
	// backends may tolerate corrupt entries while materializing broad snapshots;
	// a targeted read needs to fail closed instead of turning corruption into a
	// missing-arc no-op.
	snapshot, err := w.arctable.Snapshot(ctx, namespace, root)
	if err != nil {
		return nil, fmt.Errorf("ArcTable.Snapshot failed: %w", err)
	}
	hasPayload, err := hasDefinedPayloadBinding(snapshot)
	if err != nil {
		return nil, err
	}
	if !hasPayload && !(canonicalPath == mandatoryPayloadPath && newTarget.Defined()) {
		return nil, ErrMissingPayloadBinding
	}
	oldTarget, err := w.arctable.Get(ctx, namespace, root, canonicalPath)
	if err != nil {
		if !arctable.IsNotFound(err) {
			return nil, fmt.Errorf("ArcTable.Get failed: %w", err)
		}
		oldTarget = cid.Undef
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
	retryBase, err := indexRetryBase(ctx, w.arctable, namespace)
	if err != nil {
		return nil, &IndexWriteFailedError{
			NewRoot:    newRoot,
			Namespace:  namespace,
			OldRoot:    root,
			IndexDelta: delta,
			Cause:      fmt.Errorf("ArcTable.Snapshot retry base failed: %w", err),
		}
	}

	// Step 3: Apply update to ArcTable as an old->new delta. This keeps versioned ArcTable
	// compact while still emitting tombstones for deletions. If this fails the
	// newRoot is semantically valid but unreadable via the index; surface it so
	// the caller can retry the idempotent ArcTable write.
	if err := w.arctable.Update(ctx, namespace, newRoot, root, delta); err != nil {
		return nil, &IndexWriteFailedError{
			NewRoot:    newRoot,
			Namespace:  namespace,
			OldRoot:    root,
			IndexBase:  retryBase,
			IndexDelta: delta,
			Cause:      fmt.Errorf("ArcTable.Update failed: %w", err),
		}
	}
	if w.freshness != nil {
		w.freshness.markAdvancedLocked(namespace, root, newRoot)
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
//  2. Applies semantic.BatchUpdate atomically over the current keyed view
//  3. Applies all updates to ArcTable
//
// BatchUpdateArcs is a legacy root-consuming API. Within one process, writers
// that share the same ArcTable instance advance (namespace, root) exactly once
// for successful non-no-op calls; later attempts to update the same base root
// return ErrStaleRoot instead of silently forking or overwriting ArcTable state.
//
// If any update in the batch fails, the entire operation is rejected and
// no state is modified.
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

	if w.freshness != nil {
		unlock, err := w.freshness.beginUpdate(namespace, root)
		if err != nil {
			return nil, err
		}
		defer unlock()
	}

	// Step 1: Get current arc set snapshot
	snapshot, err := w.arctable.Snapshot(ctx, namespace, root)
	if err != nil {
		return nil, fmt.Errorf("ArcTable.Snapshot failed: %w", err)
	}
	hasPayload, err := hasDefinedPayloadBinding(snapshot)
	if err != nil {
		return nil, err
	}
	if !hasPayload {
		payloadTarget, ok := normalizedUpdates[mandatoryPayloadPath]
		if !ok || !payloadTarget.Defined() {
			return nil, ErrMissingPayloadBinding
		}
	}

	// Step 2: Look up current bindings and classify operations. The snapshot is
	// still used below to build the ArcTable delta, but classification uses
	// targeted Get calls so corrupt stored entries fail closed instead of being
	// silently omitted from a broad snapshot and treated as absent paths.
	perArc := make(map[arcset.Path]UpdateResult, len(normalizedUpdates))
	batchUpdates := make([]mapping.BatchUpdate, 0, len(normalizedUpdates))

	for path, newTarget := range normalizedUpdates {
		oldTarget, err := w.arctable.Get(ctx, namespace, root, path)
		if err != nil {
			if !arctable.IsNotFound(err) {
				return nil, fmt.Errorf("ArcTable.Get failed for %s: %w", path.String(), err)
			}
			oldTarget = cid.Undef
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

		// Add to batch update list
		batchUpdates = append(batchUpdates, mapping.BatchUpdate{
			Key:      path,
			OldValue: oldTarget,
			NewValue: newTarget,
		})
	}

	// Step 3: Apply batch update atomically
	newRoot, err := w.semantic.BatchUpdate(ctx, namespace, root, batchUpdates)
	if err != nil {
		return nil, fmt.Errorf("semantic.BatchUpdate failed: %w", err)
	}

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
	retryBase, err := indexRetryBase(ctx, w.arctable, namespace)
	if err != nil {
		return nil, &IndexWriteFailedError{
			NewRoot:    newRoot,
			Namespace:  namespace,
			OldRoot:    root,
			IndexDelta: delta,
			Cause:      fmt.Errorf("ArcTable.Snapshot retry base failed: %w", err),
		}
	}

	// Step 4: Apply the update delta to ArcTable. On failure the newRoot is
	// semantically valid but its index entries are missing; surface it so the
	// caller can retry the idempotent write.
	if err := w.arctable.Update(ctx, namespace, newRoot, root, delta); err != nil {
		return nil, &IndexWriteFailedError{
			NewRoot:    newRoot,
			Namespace:  namespace,
			OldRoot:    root,
			IndexBase:  retryBase,
			IndexDelta: delta,
			Cause:      fmt.Errorf("ArcTable.Update failed: %w", err),
		}
	}
	if w.freshness != nil {
		w.freshness.markAdvancedLocked(namespace, root, newRoot)
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
	hasPayload, err := hasDefinedPayloadBinding(normalizedSnapshot)
	if err != nil {
		return cid.Undef, err
	}
	if !hasPayload {
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
	retryBase, err := indexRetryBase(ctx, w.arctable, namespace)
	if err != nil {
		return cid.Undef, &IndexWriteFailedError{
			NewRoot:    root,
			Namespace:  namespace,
			OldRoot:    cid.Undef,
			IndexDelta: normalizedSnapshot,
			Cause:      fmt.Errorf("ArcTable.Snapshot retry base failed: %w", err),
		}
	}

	// Step 2: Store arcs in ArcTable (first version). On failure root is
	// semantically committed but has no index entries; surface it for retry.
	if err := w.arctable.Update(ctx, namespace, root, cid.Undef, normalizedSnapshot); err != nil {
		return cid.Undef, &IndexWriteFailedError{
			NewRoot:    root,
			Namespace:  namespace,
			OldRoot:    cid.Undef,
			IndexBase:  retryBase,
			IndexDelta: normalizedSnapshot,
			Cause:      fmt.Errorf("ArcTable.Update failed: %w", err),
		}
	}
	if w.freshness != nil {
		w.freshness.markCurrent(namespace, root)
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

	return filterLogicalArcSet(snapshot)
}

// Package graph provides the unified Graph interface for MALT.
// Graph combines read (GraphResolver) and write (GraphWriter) capabilities,
// delegating to the resolver and writer packages respectively.
package graph

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/interfaces"
	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/sce"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// Graph implements interfaces.Graph by composing a read-side resolver
// and a write-side writer. It does not directly depend on SCE, EAT,
// ArcStore, or ContentStore — those are owned by the resolver and writer.
type Graph struct {
	resolver interfaces.GraphResolver
	writer   interfaces.GraphWriter
}

// NewGraph creates a Graph from resolver and writer implementations.
func NewGraph(resolver interfaces.GraphResolver, writer interfaces.GraphWriter) *Graph {
	return &Graph{
		resolver: resolver,
		writer:   writer,
	}
}

// Resolve implements GraphResolver.Resolve.
func (g *Graph) Resolve(ctx context.Context, root cid.Cid, path string) (cid.Cid, interfaces.Proof, error) {
	if !root.Defined() {
		return cid.Cid{}, nil, fmt.Errorf("root must be defined")
	}
	return g.resolver.Resolve(ctx, root, path)
}

// BatchResolve implements GraphResolver.BatchResolve.
func (g *Graph) BatchResolve(ctx context.Context, root cid.Cid, paths []string) (map[string]cid.Cid, *interfaces.AggregatedProof, error) {
	if !root.Defined() {
		return nil, nil, fmt.Errorf("root must be defined")
	}
	return g.resolver.BatchResolve(ctx, root, paths)
}

// Verify implements GraphResolver.Verify.
func (g *Graph) Verify(ctx context.Context, root cid.Cid, proof interfaces.Proof, expectedTarget cid.Cid) (bool, error) {
	if !root.Defined() {
		return false, fmt.Errorf("root must be defined")
	}
	return g.resolver.Verify(ctx, root, proof, expectedTarget)
}

// BatchVerify implements GraphResolver.BatchVerify.
func (g *Graph) BatchVerify(ctx context.Context, root cid.Cid, aggProof *interfaces.AggregatedProof) (bool, error) {
	if !root.Defined() {
		return false, fmt.Errorf("root must be defined")
	}
	return g.resolver.BatchVerify(ctx, root, aggProof)
}

// Update implements GraphWriter.Update.
func (g *Graph) Update(ctx context.Context, root cid.Cid, arcs map[string]cid.Cid) (cid.Cid, *interfaces.UpdateDelta, error) {
	if !root.Defined() {
		return cid.Cid{}, nil, fmt.Errorf("root must be defined")
	}
	return g.writer.Update(ctx, root, arcs)
}

// BatchUpdate implements GraphWriter.BatchUpdate.
func (g *Graph) BatchUpdate(ctx context.Context, root cid.Cid, arcs map[string]cid.Cid) (cid.Cid, *interfaces.UpdateDelta, error) {
	if !root.Defined() {
		return cid.Cid{}, nil, fmt.Errorf("root must be defined")
	}
	return g.writer.BatchUpdate(ctx, root, arcs)
}

// Snapshot implements GraphWriter.Snapshot.
func (g *Graph) Snapshot(ctx context.Context, root cid.Cid) (arcset.Snapshot, error) {
	if !root.Defined() {
		return nil, fmt.Errorf("root must be defined")
	}
	return g.writer.Snapshot(ctx, root)
}

// Commit implements GraphWriter.Commit.
func (g *Graph) Commit(ctx context.Context, snapshot arcset.View) (cid.Cid, error) {
	return g.writer.Commit(ctx, snapshot)
}

// Ensure Graph implements interfaces.Graph.
var _ interfaces.Graph = (*Graph)(nil)

// ---- Adapters ----

// NewResolverAdapter creates a GraphResolver adapter from a core resolver.Resolver.
func NewResolverAdapter(res *resolver.Resolver) interfaces.GraphResolver {
	return &ReadAdapter{resolver: res}
}

// ReadAdapter adapts *resolver.Resolver to interfaces.GraphResolver.
type ReadAdapter struct {
	resolver *resolver.Resolver
}

func (a *ReadAdapter) Resolve(ctx context.Context, root cid.Cid, path string) (cid.Cid, interfaces.Proof, error) {
	result, err := a.resolver.Resolve(root, path)
	if err != nil {
		return cid.Cid{}, nil, fmt.Errorf("resolution failed: %w", err)
	}
	return result.Target, NewTranscriptProof(result.Transcript), nil
}

func (a *ReadAdapter) BatchResolve(ctx context.Context, root cid.Cid, paths []string) (map[string]cid.Cid, *interfaces.AggregatedProof, error) {
	results := make(map[string]cid.Cid)
	for _, p := range paths {
		result, err := a.resolver.Resolve(root, p)
		if err != nil {
			continue
		}
		results[p] = result.Target
	}
	return results, nil, nil
}

func (a *ReadAdapter) Verify(ctx context.Context, root cid.Cid, proof interfaces.Proof, expectedTarget cid.Cid) (bool, error) {
	return proof.Verify(root, expectedTarget)
}

func (a *ReadAdapter) BatchVerify(ctx context.Context, root cid.Cid, aggProof *interfaces.AggregatedProof) (bool, error) {
	if aggProof == nil {
		return false, fmt.Errorf("proof must not be nil")
	}
	return aggProof.Verify()
}

// LineageRecorder is an optional interface for recording structure root lineage.
type LineageRecorder interface {
	Record(ctx context.Context, bucketId string, newRoot, oldRoot cid.Cid) error
}

// WriteAdapterOptions configures a WriteAdapter.
type WriteAdapterOptions struct {
	BucketId       string
	LineageRecorder LineageRecorder
}

// NewWriterAdapter creates a GraphWriter adapter from SCE, EAT, and optional lineage recorder.
func NewWriterAdapter(sce *sce.Engine, eat eat.EAT, opts WriteAdapterOptions) interfaces.GraphWriter {
	if opts.BucketId == "" {
		opts.BucketId = "default"
	}
	return &WriteAdapter{
		sce:            sce,
		eat:            eat,
		bucketId:       opts.BucketId,
		lineageRecorder: opts.LineageRecorder,
	}
}

// WriteAdapter adapts *writer.Writer (via its raw deps) to interfaces.GraphWriter.
type WriteAdapter struct {
	sce             *sce.Engine
	eat             eat.EAT
	bucketId        string
	lineageRecorder LineageRecorder
}

func (a *WriteAdapter) Update(ctx context.Context, root cid.Cid, arcs map[string]cid.Cid) (cid.Cid, *interfaces.UpdateDelta, error) {
	w := newWriterInternal(a.sce, a.eat, a.lineageRecorder)

	result, err := w.BatchUpdateArcs(ctx, a.bucketId, root, arcs)
	if err != nil {
		return cid.Cid{}, nil, fmt.Errorf("batch update failed: %w", err)
	}

	delta := &interfaces.UpdateDelta{
		OldRoot:              result.OldRoot,
		NewRoot:              result.NewRoot,
		RewriteAmplification: 1.0,
	}
	for _, r := range result.PerArc {
		switch r.Op {
		case 0: // ArcInsert
			delta.Added = append(delta.Added, r.Path)
		case 1: // ArcReplace
			delta.Updated = append(delta.Updated, r.Path)
		case 2: // ArcDelete
			delta.Deleted = append(delta.Deleted, r.Path)
		}
	}
	return result.NewRoot, delta, nil
}

func (a *WriteAdapter) BatchUpdate(ctx context.Context, root cid.Cid, arcs map[string]cid.Cid) (cid.Cid, *interfaces.UpdateDelta, error) {
	return a.Update(ctx, root, arcs)
}

func (a *WriteAdapter) Snapshot(ctx context.Context, root cid.Cid) (arcset.Snapshot, error) {
	return a.eat.Snapshot(ctx, a.bucketId, root)
}

func (a *WriteAdapter) Commit(ctx context.Context, snapshot arcset.View) (cid.Cid, error) {
	root, err := a.sce.Commit(snapshot)
	if err != nil {
		return cid.Undef, fmt.Errorf("SCE commit failed: %w", err)
	}
	// Store arcs in EAT
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
		return cid.Undef, iter.Err()
	}
	if err := a.eat.Update(ctx, a.bucketId, root, cid.Undef, arcsMap); err != nil {
		return cid.Undef, fmt.Errorf("EAT update failed: %w", err)
	}
	if a.lineageRecorder != nil {
		if err := a.lineageRecorder.Record(ctx, a.bucketId, root, cid.Undef); err != nil {
			return cid.Undef, fmt.Errorf("lineage record failed: %w", err)
		}
	}
	return root, nil
}

// newWriterInternal creates a *writer.Writer from raw deps without importing the writer package.
// This avoids a circular import — the adapter uses writer's logic directly.
func newWriterInternal(sce *sce.Engine, eat eat.EAT, rec LineageRecorder) *internalWriter {
	return &internalWriter{
		sce: sce,
		eat: eat,
		rec: rec,
	}
}

// internalWriter is a minimal copy of the write logic to avoid package import cycles.
// It implements the unified arc update procedure from Sec 4.5.
type internalWriter struct {
	sce *sce.Engine
	eat eat.EAT
	rec LineageRecorder
}

type arcOp uint8

const (
	arcInsert arcOp = iota
	arcReplace
	arcDelete
)

func (w *internalWriter) BatchUpdateArcs(ctx context.Context, bucketId string, root cid.Cid, updates map[string]cid.Cid) (*batchResult, error) {
	if !root.Defined() {
		return nil, fmt.Errorf("invalid structure root")
	}
	if len(updates) == 0 {
		return nil, fmt.Errorf("updates must not be empty")
	}

	snapshot, err := w.eat.Snapshot(ctx, bucketId, root)
	if err != nil {
		return nil, fmt.Errorf("EAT.Snapshot failed: %w", err)
	}

	perArc := make(map[string]arcResult, len(updates))
	sceUpdates := make(map[string]struct {
		Old cid.Cid
		New cid.Cid
	}, len(updates))
	needsRecommit := false

	for path, newTarget := range updates {
		oldTarget, err := w.eat.Get(ctx, bucketId, root, path)
		if err != nil && !eatIsNotFound(err) {
			return nil, fmt.Errorf("EAT.Get failed for %s: %w", path, err)
		}

		isInsert := !oldTarget.Defined() && newTarget.Defined()
		isDelete := oldTarget.Defined() && !newTarget.Defined()

		if isInsert || isDelete {
			needsRecommit = true
		}

		var op arcOp
		if isInsert {
			op = arcInsert
		} else if oldTarget.Defined() && newTarget.Defined() {
			op = arcReplace
		} else if isDelete {
			op = arcDelete
		}

		sceUpdates[path] = struct {
			Old cid.Cid
			New cid.Cid
		}{Old: oldTarget, New: newTarget}

		perArc[path] = arcResult{
			OldRoot:   root,
			OldTarget: oldTarget,
			NewTarget: newTarget,
			Op:        op,
			Path:      path,
		}
	}

	var newRoot cid.Cid

	if needsRecommit {
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
		newRoot, err = w.sce.BatchUpdate(root, snapshot, sceUpdates)
		if err != nil {
			return nil, fmt.Errorf("SCE.BatchUpdate failed: %w", err)
		}
	}

	if err := w.eat.Update(ctx, bucketId, newRoot, root, updates); err != nil {
		return nil, fmt.Errorf("EAT.Update failed: %w", err)
	}

	if w.rec != nil {
		if err := w.rec.Record(ctx, bucketId, newRoot, root); err != nil {
			return nil, fmt.Errorf("LineageRecorder.Record failed: %w", err)
		}
	}

	for path := range perArc {
		r := perArc[path]
		r.NewRoot = newRoot
		perArc[path] = r
	}

	return &batchResult{
		OldRoot: root,
		NewRoot: newRoot,
		PerArc:  perArc,
	}, nil
}

func eatIsNotFound(err error) bool {
	if err == eat.ErrNotFound {
		return true
	}
	if err != nil && err.Error() == "arc not found" {
		return true
	}
	return false
}

type arcResult struct {
	OldRoot   cid.Cid
	NewRoot   cid.Cid
	Path      string
	OldTarget cid.Cid
	NewTarget cid.Cid
	Op        arcOp
}

type batchResult struct {
	OldRoot cid.Cid
	NewRoot cid.Cid
	PerArc  map[string]arcResult
}

package gateway

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/lineage"
	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/types/evidence"
	"github.com/dewebprotocol/malt/core/writer"
	cid "github.com/ipfs/go-cid"
)

// NodeAdapter adapts api.Node to the gateway's needs.
// It provides a thin layer between the HTTP handlers and the MALT core.
type NodeAdapter struct {
	node *api.Node
	gm   *graph.Manager
	wr   *WriteAdapter
}

// WriteAdapter wraps writer.Writer and provides string-based API.
type WriteAdapter struct {
	writer   *writerAdapterInternal
	bucketId string
}

// writerAdapterInternal adapts the writer package to string-based API.
type writerAdapterInternal struct {
	// Uses the Node's SCE, EAT directly
	node *api.Node
}

// NewNodeAdapter creates a new adapter for the given MALT node.
func NewNodeAdapter(node *api.Node) *NodeAdapter {
	return &NodeAdapter{
		node: node,
		gm:   node.GraphManager(),
	}
}

// GraphManager returns the graph manager.
func (na *NodeAdapter) GraphManager() *graph.Manager {
	return na.gm
}

// Writer returns the write adapter.
func (na *NodeAdapter) Writer() *WriteAdapter {
	if na.wr == nil {
		na.wr = &WriteAdapter{
			writer:   &writerAdapterInternal{node: na.node},
			bucketId: "default",
		}
	}
	return na.wr
}

// HybridResolve performs hybrid path resolution.
func (na *NodeAdapter) HybridResolve(rootStr string, path string) (*ResolveResult, error) {
	rootCid, err := decodeCID(rootStr)
	if err != nil {
		return nil, fmt.Errorf("invalid root CID: %w", err)
	}

	result, err := na.node.HybridResolver().Resolve(rootCid, path)
	if err != nil {
		return nil, fmt.Errorf("resolution failed: %w", err)
	}

	rr := &ResolveResult{
		Target:     result.Target.String(),
		Transcript: &Transcript{Steps: make([]StepEvidence, len(result.Transcript.Steps))},
	}

	for i, step := range result.Transcript.Steps {
		kind := "unknown"
		switch step.Evidence.Kind() {
		case evidence.EvidenceKindExplicit:
			kind = "explicit"
		case evidence.EvidenceKindImplicit:
			kind = "implicit"
		case evidence.EvidenceKindHAMT:
			kind = "hamt"
		}
		rr.Transcript.Steps[i] = StepEvidence{
			Path:     step.Path,
			Target:   step.Target.String(),
			Evidence: step.Evidence.Bytes(),
			Kind:     kind,
		}
	}

	return rr, nil
}

// VerifyTranscript verifies a resolution transcript.
func (na *NodeAdapter) VerifyTranscript(rootStr string, transcript *Transcript) (bool, error) {
	rootCid, err := decodeCID(rootStr)
	if err != nil {
		return false, fmt.Errorf("invalid root CID: %w", err)
	}

	// Reconstruct the resolver's Transcript type
	steps := make([]resolver.StepEvidence, len(transcript.Steps))
	for i, step := range transcript.Steps {
		targetCid, err := decodeCID(step.Target)
		if err != nil {
			return false, fmt.Errorf("invalid target CID at step %d: %w", i, err)
		}

		var ev evidence.Evidence
		switch step.Kind {
		case "explicit":
			ev = evidence.NewExplicitEvidence(step.Evidence)
		case "implicit":
			ev = evidence.NewImplicitEvidence(step.Evidence)
		case "hamt":
			ev = evidence.NewHAMTEvidence(step.Evidence)
		default:
			return false, fmt.Errorf("unknown evidence kind: %s", step.Kind)
		}

		steps[i] = resolver.StepEvidence{
			Path:     step.Path,
			Target:   targetCid,
			Evidence: ev,
		}
	}

	resolverTranscript := &resolver.Transcript{Steps: steps}
	return na.node.HybridResolver().VerifyTranscript(rootCid, resolverTranscript)
}

// GetArc retrieves an arc target for a given bucket, root, and path.
func (na *NodeAdapter) GetArc(bucketId, rootStr, path string) (string, error) {
	rootCid, err := decodeCID(rootStr)
	if err != nil {
		return "", fmt.Errorf("invalid root CID: %w", err)
	}

	target, err := na.node.EAT().Get(context.Background(), bucketId, rootCid, path)
	if err != nil {
		return "", fmt.Errorf("EAT.Get failed: %w", err)
	}

	return target.String(), nil
}

// GetArcSetSnapshot returns the arc set for a structure as a map.
func (na *NodeAdapter) GetArcSetSnapshot(bucketId, rootStr string) (map[string]string, error) {
	rootCid, err := decodeCID(rootStr)
	if err != nil {
		return nil, fmt.Errorf("invalid root CID: %w", err)
	}

	snapshot, err := na.node.EAT().Snapshot(context.Background(), bucketId, rootCid)
	if err != nil {
		return nil, fmt.Errorf("EAT.Snapshot failed: %w", err)
	}

	result := make(map[string]string)
	iter := snapshot.Iterate()
	for {
		path, target, ok := iter.Next()
		if !ok {
			break
		}
		result[path] = target.String()
	}
	if iter.Err() != nil {
		return nil, fmt.Errorf("iteration error: %w", iter.Err())
	}

	return result, nil
}

// CASGet fetches content from the underlying CAS.
func (na *NodeAdapter) CASGet(cidStr string) ([]byte, error) {
	c, err := decodeCID(cidStr)
	if err != nil {
		return nil, fmt.Errorf("invalid CID: %w", err)
	}

	content, err := na.node.CAS().Get(context.Background(), c)
	if err != nil {
		return nil, fmt.Errorf("CAS.Get failed: %w", err)
	}

	return content, nil
}

// Config returns the node configuration as a string.
func (na *NodeAdapter) Config() string {
	return na.node.Config().String()
}

// Close releases all resources.
func (na *NodeAdapter) Close() error {
	return na.node.Close()
}

// UpdateArc updates a single arc.
func (wa *WriteAdapter) UpdateArc(ctx context.Context, bucketId string, rootStr string, path string, targetStr string) (*WriteUpdateResult, error) {
	rootCid, err := decodeCID(rootStr)
	if err != nil {
		return nil, fmt.Errorf("invalid root CID: %w", err)
	}

	var newTarget cid.Cid
	if targetStr == "" {
		newTarget = cid.Undef
	} else {
		newTarget, err = decodeCID(targetStr)
		if err != nil {
			return nil, fmt.Errorf("invalid target CID: %w", err)
		}
	}

	// Use the Writer module if available, otherwise fall back to manual update
	w := wa.writer
	return w.UpdateArc(ctx, bucketId, rootCid, path, newTarget)
}

// BatchUpdateArcs updates multiple arcs.
func (wa *WriteAdapter) BatchUpdateArcs(ctx context.Context, bucketId string, rootStr string, updates map[string]string) (*WriteBatchResult, error) {
	rootCid, err := decodeCID(rootStr)
	if err != nil {
		return nil, fmt.Errorf("invalid root CID: %w", err)
	}

	cidUpdates := make(map[string]cid.Cid)
	for path, targetStr := range updates {
		if targetStr == "" {
			cidUpdates[path] = cid.Undef
		} else {
			t, err := decodeCID(targetStr)
			if err != nil {
				return nil, fmt.Errorf("invalid target CID for %s: %w", path, err)
			}
			cidUpdates[path] = t
		}
	}

	w := wa.writer
	return w.BatchUpdateArcs(ctx, bucketId, rootCid, cidUpdates)
}

// CreateStructure creates a new structure from arcs.
func (wa *WriteAdapter) CreateStructure(ctx context.Context, bucketId string, arcs map[string]string) (string, error) {
	arcsMap := make(map[string]cid.Cid)
	for path, targetStr := range arcs {
		t, err := decodeCID(targetStr)
		if err != nil {
			return "", fmt.Errorf("invalid target CID for %s: %w", path, err)
		}
		arcsMap[path] = t
	}

	w := wa.writer
	rootCid, err := w.CreateStructure(ctx, bucketId, arcsMap)
	if err != nil {
		return "", err
	}
	return rootCid.String(), nil
}

// writeAdapterInternal methods.

// UpdateArc performs a single arc update using the Writer module.
func (wi *writerAdapterInternal) UpdateArc(ctx context.Context, bucketId string, rootCid cid.Cid, path string, newTarget cid.Cid) (*WriteUpdateResult, error) {
	// Build the write-side API from Node components
	w := buildWriter(wi.node)

	result, err := w.UpdateArc(ctx, bucketId, rootCid, path, newTarget)
	if err != nil {
		return nil, err
	}

	return &WriteUpdateResult{
		OldRoot:   result.OldRoot.String(),
		NewRoot:   result.NewRoot.String(),
		Path:      result.Path,
		OldTarget: result.OldTarget.String(),
		NewTarget: result.NewTarget.String(),
		Op:        result.Op.String(),
	}, nil
}

// BatchUpdateArcs performs a batch arc update.
func (wi *writerAdapterInternal) BatchUpdateArcs(ctx context.Context, bucketId string, rootCid cid.Cid, updates map[string]cid.Cid) (*WriteBatchResult, error) {
	w := buildWriter(wi.node)

	result, err := w.BatchUpdateArcs(ctx, bucketId, rootCid, updates)
	if err != nil {
		return nil, err
	}

	perArc := make(map[string]*WriteUpdateResult)
	for path, r := range result.PerArc {
		perArc[path] = &WriteUpdateResult{
			OldRoot:   r.OldRoot.String(),
			NewRoot:   r.NewRoot.String(),
			Path:      r.Path,
			OldTarget: r.OldTarget.String(),
			NewTarget: r.NewTarget.String(),
			Op:        r.Op.String(),
		}
	}

	return &WriteBatchResult{
		OldRoot: result.OldRoot.String(),
		NewRoot: result.NewRoot.String(),
		PerArc:  perArc,
	}, nil
}

// CreateStructure creates a new structure.
func (wi *writerAdapterInternal) CreateStructure(ctx context.Context, bucketId string, arcs map[string]cid.Cid) (cid.Cid, error) {
	w := buildWriter(wi.node)

	// Convert map to arcset.Snapshot
	snapshot := buildSnapshot(arcs)
	rootCid, err := w.CreateStructure(ctx, bucketId, snapshot)
	if err != nil {
		return cid.Undef, err
	}

	return rootCid, nil
}

// buildWriter constructs a writer.Writer from a MALT node.
func buildWriter(node *api.Node) *writer.Writer {
	rec := buildLineageRecorder(node)
	return writer.NewWriter(node.SCE(), node.EAT(), rec)
}

// buildLineageRecorder creates a lineage recorder adapter if the node
// has a lineage manager configured.
func buildLineageRecorder(node *api.Node) writer.LineageRecorder {
	lm := node.LineageManager()
	if lm == nil {
		return nil
	}
	return lineage.NewRecorderAdapter(lm, nil)
}

// buildSnapshot converts a map[string]cid.Cid to an arcset.Snapshot.
func buildSnapshot(arcs map[string]cid.Cid) arcset.Snapshot {
	return arcset.NewMapFrom(arcs)
}

// Verify codec detection helper.
func (na *NodeAdapter) CodecName(rootStr string) (string, error) {
	c, err := decodeCID(rootStr)
	if err != nil {
		return "", err
	}
	if codec.IsMaltCid(c) {
		return codec.CodecName(codec.GetMaltCodec(c)), nil
	}
	return "standard", nil
}
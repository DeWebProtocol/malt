package gateway

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/graph"
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
	g    *graph.Graph // default graph for operations
	wr   *WriteAdapter
}

const defaultGatewayGraphID = "default"

// WriteAdapter wraps writer.Writer and provides string-based API.
type WriteAdapter struct {
	g        *graph.Graph
	writer   *writer.Writer
	bucketId string
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

// EnsureGraph returns the default graph, creating it if needed.
func (na *NodeAdapter) EnsureGraph() (*graph.Graph, error) {
	if na.g != nil {
		return na.g, nil
	}
	g, err := na.node.NewGraph(defaultGatewayGraphID)
	if err != nil {
		return nil, err
	}
	na.g = g
	return g, nil
}

// Writer returns the write adapter.
func (na *NodeAdapter) Writer() (*WriteAdapter, error) {
	if na.wr == nil {
		g, err := na.EnsureGraph()
		if err != nil {
			return nil, err
		}
		na.wr = &WriteAdapter{
			g:        g,
			writer:   g.Writer(),
			bucketId: g.BucketId(),
		}
	}
	return na.wr, nil
}

// HybridResolve performs path resolution across MALT-native and interoperable
// IPLD/CAS steps when needed.
func (na *NodeAdapter) HybridResolve(rootStr string, path string) (*ResolveResult, error) {
	g, err := na.EnsureGraph()
	if err != nil {
		return nil, err
	}

	rootCid, err := decodeCID(rootStr)
	if err != nil {
		return nil, fmt.Errorf("invalid root CID: %w", err)
	}

	result, err := g.Resolver().Resolve(rootCid, path)
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
	g, err := na.EnsureGraph()
	if err != nil {
		return false, err
	}

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
	return g.Resolver().VerifyTranscript(rootCid, resolverTranscript)
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

	result, err := wa.writer.UpdateArc(ctx, bucketId, rootCid, path, newTarget)
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

	result, err := wa.writer.BatchUpdateArcs(ctx, bucketId, rootCid, cidUpdates)
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

	snapshot := arcset.NewMapFrom(arcsMap)
	rootCid, err := wa.writer.CreateStructure(ctx, bucketId, snapshot)
	if err != nil {
		return "", err
	}
	return rootCid.String(), nil
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

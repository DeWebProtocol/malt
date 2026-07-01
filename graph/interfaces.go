package graph

import (
	"context"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/graph/resolver"
	"github.com/dewebprotocol/malt/graph/writer"
	cid "github.com/ipfs/go-cid"
)

// Resolver is the graph read/proof port.
type Resolver interface {
	ResolveKey(ctx context.Context, root cid.Cid, path string) (*resolver.ResolveResult, error)
	Resolve(ctx context.Context, root cid.Cid, path string) (*resolver.ResolveResult, error)
	VerifyTranscript(ctx context.Context, root cid.Cid, transcript *resolver.Transcript) (bool, error)
}

// MutationWriter is the stable graph mutation port. Callers supply an explicit
// base root through writer.SemanticMutation and receive a result root in the
// write receipt; this interface does not publish heads or arbitrate freshness.
type MutationWriter interface {
	Apply(ctx context.Context, namespace string, mut writer.SemanticMutation) (writer.WriteReceipt, error)
}

// CompatWriter exposes reference-runtime helper methods used by the local
// server, CLI, and tests. These helpers are not the gateway product API.
type CompatWriter interface {
	CreateStructure(ctx context.Context, namespace string, arcs arcset.ArcSet) (cid.Cid, error)
	UpdateArc(ctx context.Context, namespace string, root cid.Cid, path string, newTarget cid.Cid) (*writer.UpdateResult, error)
	BatchUpdateArcs(ctx context.Context, namespace string, root cid.Cid, updates map[string]cid.Cid) (*writer.BatchUpdateResult, error)
	GetArc(ctx context.Context, namespace string, root cid.Cid, path string) (cid.Cid, error)
	GetSnapshot(ctx context.Context, namespace string, root cid.Cid) (arcset.ArcSet, error)
}

// Writer is the reference-runtime write surface. New core integrations should
// prefer MutationWriter unless they intentionally need compatibility helpers.
type Writer interface {
	MutationWriter
	CompatWriter
}

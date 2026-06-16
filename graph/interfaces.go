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
	ResolveKey(root cid.Cid, path string) (*resolver.ResolveResult, error)
	Resolve(root cid.Cid, path string) (*resolver.ResolveResult, error)
	VerifyTranscript(root cid.Cid, transcript *resolver.Transcript) (bool, error)
}

// Writer is the graph mutation port.
type Writer interface {
	Apply(ctx context.Context, namespace string, mut writer.SemanticMutation) (writer.WriteReceipt, error)
	CreateStructure(ctx context.Context, namespace string, arcs arcset.ArcSet) (cid.Cid, error)
	UpdateArc(ctx context.Context, namespace string, root cid.Cid, path string, newTarget cid.Cid) (*writer.UpdateResult, error)
	BatchUpdateArcs(ctx context.Context, namespace string, root cid.Cid, updates map[string]cid.Cid) (*writer.BatchUpdateResult, error)
	GetArc(ctx context.Context, namespace string, root cid.Cid, path string) (cid.Cid, error)
	GetSnapshot(ctx context.Context, namespace string, root cid.Cid) (arcset.ArcSet, error)
}

package unixfs

import (
	"context"
	"fmt"

	gatewaytransport "github.com/dewebprotocol/malt-client/transport"
	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
	"github.com/dewebprotocol/malt-core/mutation"
	cid "github.com/ipfs/go-cid"
)

// GatewayMutationRemote is the legacy Gateway HTTP-DTO mutation surface.
// New code should implement MutationRemote directly.
type GatewayMutationRemote interface {
	ApplySemanticMutation(context.Context, mutation.SemanticMutation) (*gatewaytransport.SemanticMutationResponse, error)
	CreateRootStructure(context.Context, map[string]string) (*gatewaytransport.CreateStructureResponse, error)
}

type GatewayMutationAdapter = MutationAdapter

// Deprecated: use NewMutationAdapter.
func NewGatewayMutationAdapter(remote GatewayMutationRemote) (*GatewayMutationAdapter, error) {
	if remote == nil {
		return nil, fmt.Errorf("unixfs gateway mutation remote is nil")
	}
	return NewMutationAdapter(legacyGatewayMutationRemote{remote: remote})
}

type legacyGatewayMutationRemote struct {
	remote GatewayMutationRemote
}

func (r legacyGatewayMutationRemote) ApplyMutation(ctx context.Context, mut mutation.SemanticMutation) (transportcap.MutationResult, error) {
	value, err := r.remote.ApplySemanticMutation(ctx, mut)
	if err != nil {
		return transportcap.MutationResult{}, err
	}
	if value == nil {
		return transportcap.MutationResult{}, fmt.Errorf("gateway returned a nil semantic mutation receipt")
	}
	base, err := cid.Parse(value.BaseRoot)
	if err != nil {
		return transportcap.MutationResult{}, fmt.Errorf("decode gateway mutation base root: %w", err)
	}
	candidate, err := cid.Parse(value.NewRoot)
	if err != nil {
		return transportcap.MutationResult{}, fmt.Errorf("decode gateway candidate root: %w", err)
	}
	return transportcap.MutationResult{
		BaseRoot: base, CandidateRoot: candidate,
		DeltaCount: value.DeltaCount, ArcCount: value.ArcCount,
		MALTObjectCount: value.MALTObjectCount, MapCount: value.MapCount, ListCount: value.ListCount,
	}, nil
}

func (r legacyGatewayMutationRemote) CreateStructureCandidate(ctx context.Context, arcs map[string]string) (cid.Cid, error) {
	value, err := r.remote.CreateRootStructure(ctx, arcs)
	if err != nil {
		return cid.Undef, err
	}
	if value == nil {
		return cid.Undef, fmt.Errorf("gateway returned a nil structure receipt")
	}
	root, err := cid.Parse(value.Root)
	if err != nil {
		return cid.Undef, fmt.Errorf("decode gateway structure candidate root: %w", err)
	}
	return root, nil
}

var _ MutationRemote = legacyGatewayMutationRemote{}

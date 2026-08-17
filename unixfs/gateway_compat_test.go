package unixfs_test

import (
	"context"
	"testing"

	gatewaytransport "github.com/dewebprotocol/malt-client/transport"
	"github.com/dewebprotocol/malt-client/unixfs"
	"github.com/dewebprotocol/malt-core/mutation"
)

type legacyMutationRemote struct {
	base      string
	candidate string
}

func (r legacyMutationRemote) ApplySemanticMutation(context.Context, mutation.SemanticMutation) (*gatewaytransport.SemanticMutationResponse, error) {
	return &gatewaytransport.SemanticMutationResponse{BaseRoot: r.base, NewRoot: r.candidate}, nil
}

func (r legacyMutationRemote) CreateRootStructure(context.Context, map[string]string) (*gatewaytransport.CreateStructureResponse, error) {
	return &gatewaytransport.CreateStructureResponse{Root: r.base}, nil
}

func TestDeprecatedGatewayMutationAdapterRemainsSourceCompatible(t *testing.T) {
	base := adapterCID(t, "legacy-base")
	candidate := adapterCID(t, "legacy-candidate")
	adapter, err := unixfs.NewGatewayMutationAdapter(legacyMutationRemote{base: base.String(), candidate: candidate.String()})
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.ApplySemanticMutation(t.Context(), mutation.SemanticMutation{BaseRoot: base})
	if err != nil {
		t.Fatal(err)
	}
	if !result.BaseRoot.Equals(base) || !result.CandidateRoot.Equals(candidate) || result.Accepted {
		t.Fatalf("legacy adapter result = %#v", result)
	}
}

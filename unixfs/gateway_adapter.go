package unixfs

import (
	"context"
	"fmt"

	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
	"github.com/dewebprotocol/malt-core/mutation"
	cid "github.com/ipfs/go-cid"
)

// MutationRemote is the untrusted semantic writer capability consumed by the
// UnixFS application adapter. It contains no HTTP or Gateway response DTOs.
type MutationRemote = transportcap.Mutations

const (
	payloadArc       = "@payload"
	emptyPayloadRoot = "bafkqaaa"
)

// MutationAdapter translates transport-neutral mutation results into
// UnixFS-owned candidate-root values and fixed-list writer operations.
type MutationAdapter struct {
	remote MutationRemote
}

func NewMutationAdapter(remote MutationRemote) (*MutationAdapter, error) {
	if remote == nil {
		return nil, fmt.Errorf("unixfs mutation remote is nil")
	}
	return &MutationAdapter{remote: remote}, nil
}

func (a *MutationAdapter) ApplySemanticMutation(ctx context.Context, mut mutation.SemanticMutation) (CandidateRootReceipt, error) {
	if a == nil || a.remote == nil {
		return CandidateRootReceipt{}, fmt.Errorf("unixfs mutation adapter is nil")
	}
	response, err := a.remote.ApplyMutation(ctx, mut)
	if err != nil {
		return CandidateRootReceipt{}, err
	}
	if !response.BaseRoot.Equals(mut.BaseRoot) {
		return CandidateRootReceipt{}, fmt.Errorf("remote mutation result base root does not match the requested mutation")
	}
	if !response.CandidateRoot.Defined() {
		return CandidateRootReceipt{}, fmt.Errorf("remote mutation candidate root is undefined")
	}
	return CandidateRootReceipt{BaseRoot: response.BaseRoot, CandidateRoot: response.CandidateRoot, Accepted: false}, nil
}

func (a *MutationAdapter) CreateFixedListBaseRoot(ctx context.Context) (cid.Cid, error) {
	if a == nil || a.remote == nil {
		return cid.Undef, fmt.Errorf("unixfs mutation adapter is nil")
	}
	root, err := a.remote.CreateStructureCandidate(ctx, map[string]string{payloadArc: emptyPayloadRoot})
	if err != nil {
		return cid.Undef, err
	}
	if !root.Defined() {
		return cid.Undef, fmt.Errorf("remote fixed-list base-root candidate is undefined")
	}
	return root, nil
}

func (a *MutationAdapter) ApplyFixedListPayloadMutation(ctx context.Context, mut mutation.SemanticMutation) (cid.Cid, error) {
	receipt, err := a.ApplySemanticMutation(ctx, mut)
	if err != nil {
		return cid.Undef, err
	}
	return receipt.CandidateRoot, nil
}

var _ MutationTransport = (*MutationAdapter)(nil)
var _ FixedListPayloadWriter = (*MutationAdapter)(nil)

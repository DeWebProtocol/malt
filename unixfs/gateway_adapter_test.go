package unixfs_test

import (
	"context"
	"testing"

	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
	"github.com/dewebprotocol/malt-client/unixfs"
	unixfsmodel "github.com/dewebprotocol/malt-client/unixfs/model"
	"github.com/dewebprotocol/malt-core/mutation"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

type mutationRemote struct {
	base             cid.Cid
	responseBase     cid.Cid
	candidate        cid.Cid
	createdStructure map[string]string
}

func (r mutationRemote) ApplyMutation(_ context.Context, mut mutation.SemanticMutation) (transportcap.MutationResult, error) {
	if !mut.BaseRoot.Equals(r.base) {
		return transportcap.MutationResult{BaseRoot: r.responseBase, CandidateRoot: r.base}, nil
	}
	responseBase := r.responseBase
	if !responseBase.Defined() {
		responseBase = r.base
	}
	return transportcap.MutationResult{BaseRoot: responseBase, CandidateRoot: r.candidate}, nil
}
func (r mutationRemote) CreateStructureCandidate(_ context.Context, arcs map[string]string) (cid.Cid, error) {
	for key, value := range arcs {
		r.createdStructure[key] = value
	}
	return r.base, nil
}

func TestMutationAdapterReturnsUnacceptedTransportNeutralReceipt(t *testing.T) {
	base := adapterCID(t, "base")
	candidate := adapterCID(t, "candidate")
	created := map[string]string{}
	adapter, err := unixfs.NewMutationAdapter(mutationRemote{base: base, candidate: candidate, createdStructure: created})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := adapter.ApplySemanticMutation(t.Context(), mutation.SemanticMutation{BaseRoot: base})
	if err != nil {
		t.Fatal(err)
	}
	if receipt.Accepted || !receipt.BaseRoot.Equals(base) || !receipt.CandidateRoot.Equals(candidate) {
		t.Fatalf("receipt = %#v", receipt)
	}
	listBase, err := adapter.CreateFixedListBaseRoot(t.Context())
	if err != nil || !listBase.Equals(base) {
		t.Fatalf("fixed-list base = %s err=%v", listBase, err)
	}
	if len(created) != 1 || created["@payload"] != "bafkqaaa" {
		t.Fatalf("fixed-list base bindings = %#v", created)
	}
}

func TestMutationAdapterRejectsMismatchedResponseBaseRoot(t *testing.T) {
	base := adapterCID(t, "base")
	other := adapterCID(t, "other-base")
	candidate := adapterCID(t, "candidate")
	adapter, err := unixfs.NewMutationAdapter(mutationRemote{
		base: base, responseBase: other, candidate: candidate, createdStructure: map[string]string{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.ApplySemanticMutation(t.Context(), mutation.SemanticMutation{BaseRoot: base}); err == nil {
		t.Fatal("adapter accepted a remote result bound to another base root")
	}
}

type acceptedMutationTransport struct{ receipt unixfs.CandidateRootReceipt }

func (t acceptedMutationTransport) ApplySemanticMutation(context.Context, mutation.SemanticMutation) (unixfs.CandidateRootReceipt, error) {
	return t.receipt, nil
}

func TestApplyPlanRejectsTransportClaimingCandidateAccepted(t *testing.T) {
	base := adapterCID(t, "base")
	candidate := adapterCID(t, "candidate")
	remote := acceptedMutationTransport{receipt: unixfs.CandidateRootReceipt{BaseRoot: base, CandidateRoot: candidate, Accepted: true}}
	plan := &unixfsmodel.MutationPlan{BaseRoot: base}
	if _, err := unixfs.ApplyPlan(t.Context(), remote, plan, cid.Undef); err == nil {
		t.Fatal("ApplyPlan accepted a transport receipt marked as accepted")
	}
}

func adapterCID(t *testing.T, body string) cid.Cid {
	t.Helper()
	hash, err := mh.Sum([]byte(body), mh.SHA2_256, -1)
	if err != nil {
		t.Fatal(err)
	}
	return cid.NewCidV1(cid.Raw, hash)
}

package bucketsync

import (
	"context"
	"testing"
	"time"

	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
	cid "github.com/ipfs/go-cid"
)

type capabilityRemote struct {
	binding transportcap.DatasetBinding
	head    transportcap.ObservedHead
	calls   int
	apply   func(transportcap.ApplyRequest) transportcap.ApplyResult
	last    transportcap.ApplyRequest
}

func (r *capabilityRemote) DatasetBinding() transportcap.DatasetBinding {
	return r.binding
}

func (r *capabilityRemote) ObserveHead(context.Context) (*transportcap.ObservedHead, error) {
	r.calls++
	value := r.head
	return &value, nil
}

func (r *capabilityRemote) ApplyCandidate(_ context.Context, request transportcap.ApplyRequest) (*transportcap.ApplyResult, error) {
	r.calls++
	r.last = request
	value := r.apply(request)
	return &value, nil
}

func TestOpenRemoteBranchRejectsMisbindingBeforeIO(t *testing.T) {
	remote := &capabilityRemote{binding: transportcap.DatasetBinding{DatasetID: "another", Branch: "main"}}
	if _, err := OpenRemote(t.TempDir()+"/workspace.json", remote, "dataset-one"); err == nil {
		t.Fatal("OpenRemote accepted a capability bound to another dataset")
	}
	if remote.calls != 0 {
		t.Fatalf("misbound capability performed %d I/O calls", remote.calls)
	}
}

func TestRemoteCapabilityRunsPullAndVerifiedApplyWithoutGatewayDTOs(t *testing.T) {
	base := testCID(t, "capability-base")
	candidate := testCID(t, "capability-candidate")
	now := time.Date(2026, time.August, 17, 1, 2, 3, 0, time.UTC)
	remote := &capabilityRemote{
		binding: transportcap.DatasetBinding{DatasetID: "dataset-one", Branch: "main"},
		head: transportcap.ObservedHead{
			DatasetID: "dataset-one", Name: "main", Kind: "main", State: "open",
			CommitID: "commit-base", Root: base.String(), Revision: 1, CreatedAt: now, UpdatedAt: now,
		},
	}
	remote.apply = func(request transportcap.ApplyRequest) transportcap.ApplyResult {
		commit := transportcap.Commit{
			ID: "commit-candidate", DatasetID: "dataset-one", Root: request.CandidateRoot,
			Parents: []string{request.BaseCommit}, BaseRoot: request.BaseRoot,
			Author: "device-one", Message: request.Message, CreatedAt: now,
		}
		return transportcap.ApplyResult{
			Status: "fast_forward",
			Head: transportcap.ObservedHead{
				DatasetID: "dataset-one", Name: "main", Kind: "main", State: "open",
				CommitID: commit.ID, Root: commit.Root, Revision: 2, CreatedAt: now, UpdatedAt: now,
			},
			Candidate: commit,
			Commit:    commit,
		}
	}
	service, err := OpenRemote(t.TempDir()+"/workspace.json", remote, "dataset-one")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := service.Pull(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	stash, err := service.Stage(candidate, workspace.Base, cid.Undef, "capability apply")
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := service.Push(t.Context(), candidate, cid.Undef, "capability apply")
	if err != nil {
		t.Fatal(err)
	}
	if remote.last.OperationID == "" || remote.last.OperationID != stash.PushID || remote.last.CandidateRoot != candidate.String() {
		t.Fatalf("semantic apply request = %#v stash=%#v", remote.last, stash)
	}
	if outcome.Result.Head.Root != candidate.String() || outcome.Workspace.Base.Root != candidate.String() {
		t.Fatalf("apply outcome = %#v", outcome)
	}
}

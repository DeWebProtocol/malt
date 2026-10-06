package bucketsync

import (
	"context"
	"testing"
	"time"

	"github.com/dewebprotocol/malt-client/nodeapi"
	cid "github.com/ipfs/go-cid"
)

type capabilityRemote struct {
	binding nodeapi.DatasetBinding
	head    nodeapi.ObservedHead
	calls   int
	apply   func(nodeapi.ApplyRequest) nodeapi.ApplyResult
	last    nodeapi.ApplyRequest
}

func (r *capabilityRemote) DatasetBinding() nodeapi.DatasetBinding {
	return r.binding
}

func (r *capabilityRemote) ObserveHead(context.Context) (*nodeapi.ObservedHead, error) {
	r.calls++
	value := r.head
	return &value, nil
}

func (r *capabilityRemote) ApplyCandidate(_ context.Context, request nodeapi.ApplyRequest) (*nodeapi.ApplyResult, error) {
	r.calls++
	r.last = request
	value := r.apply(request)
	return &value, nil
}

func TestOpenRemoteBranchRejectsMisbindingBeforeIO(t *testing.T) {
	remote := &capabilityRemote{binding: nodeapi.DatasetBinding{DatasetID: "another", Branch: "main"}}
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
		binding: nodeapi.DatasetBinding{DatasetID: "dataset-one", Branch: "main"},
		head: nodeapi.ObservedHead{
			DatasetID: "dataset-one", Name: "main", Kind: "main", State: "open",
			CommitID: "commit-base", Root: base.String(), Revision: 1, CreatedAt: now, UpdatedAt: now,
		},
	}
	remote.apply = func(request nodeapi.ApplyRequest) nodeapi.ApplyResult {
		commit := nodeapi.Commit{
			ID: "commit-candidate", DatasetID: "dataset-one", Root: request.CandidateRoot,
			Parents: []string{request.BaseCommit}, BaseRoot: request.BaseRoot,
			Author: "device-one", Message: request.Message, CreatedAt: now,
		}
		return nodeapi.ApplyResult{
			Status: "fast_forward",
			Head: nodeapi.ObservedHead{
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

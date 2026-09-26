package bucketsync

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	transport "github.com/dewebprotocol/malt-client/transport/capability"
	cid "github.com/ipfs/go-cid"
)

type fakeGateway struct {
	head     transport.ObservedHead
	result   transport.ApplyResult
	headErr  error
	pushErr  error
	onHead   func()
	lastPush transport.ApplyRequest
}

func (f *fakeGateway) ObserveHead(context.Context) (*transport.ObservedHead, error) {
	if f.onHead != nil {
		f.onHead()
	}
	if f.headErr != nil {
		return nil, f.headErr
	}
	value := f.head
	return &value, nil
}

func (f *fakeGateway) ApplyCandidate(_ context.Context, request transport.ApplyRequest) (*transport.ApplyResult, error) {
	f.lastPush = request
	if f.pushErr != nil {
		return nil, f.pushErr
	}
	value := f.result
	return &value, nil
}

func TestPushStashesBeforeFetchAndKeepsOriginalBase(t *testing.T) {
	baseRoot := testCID(t, "base")
	remoteRoot := testCID(t, "remote")
	candidateRoot := testCID(t, "candidate")
	mergedRoot := testCID(t, "merged")
	now := time.Now().UTC()
	gateway := &fakeGateway{head: testHead("cmt_base", baseRoot, 1, now)}
	service, err := OpenRemote(filepath.Join(t.TempDir(), "buckets.json"), gateway, "bkt_one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Pull(t.Context()); err != nil {
		t.Fatal(err)
	}
	base, err := service.CurrentBase(baseRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Stage(candidateRoot, base, cid.Undef, "local edit"); err != nil {
		t.Fatal(err)
	}
	gateway.head = testHead("cmt_remote", remoteRoot, 2, now)
	gateway.result = transport.ApplyResult{
		Status:    "merged",
		Head:      testHead("cmt_merge", mergedRoot, 3, now),
		Candidate: testCommit("cmt_candidate", candidateRoot, baseRoot, []string{"cmt_base"}, "local edit", now),
		Commit:    testCommit("cmt_merge", mergedRoot, remoteRoot, []string{"cmt_remote", "cmt_candidate"}, "gateway auto-merge", now),
		MergeBase: baseRoot.String(),
	}
	gateway.onHead = func() {
		workspace, err := service.Status()
		if err != nil {
			t.Error(err)
			return
		}
		if len(workspace.Stashes) != 1 || workspace.Stashes[0].Status != "pending" {
			t.Errorf("workspace was fetched before stash: %#v", workspace)
		}
		if workspace.Stashes[0].Base.CommitID != "cmt_base" || workspace.Stashes[0].Base.Root != baseRoot.String() {
			t.Errorf("stash base = %#v", workspace.Stashes[0].Base)
		}
	}
	outcome, err := service.Push(t.Context(), candidateRoot, cid.Undef, "local edit")
	if err != nil {
		t.Fatal(err)
	}
	if gateway.lastPush.BaseCommit != "cmt_base" || gateway.lastPush.BaseRoot != baseRoot.String() || gateway.lastPush.BaseRevision != 1 {
		t.Fatalf("push used fetched head instead of stashed base: %#v", gateway.lastPush)
	}
	if outcome.Result.Status != "merged" || outcome.Workspace.Base.CommitID != "cmt_merge" || len(outcome.Workspace.Stashes) != 0 {
		t.Fatalf("outcome = %#v", outcome)
	}
}

func TestBranchWorkspacesArePersistedIndependently(t *testing.T) {
	now := time.Now().UTC()
	mainHead := testHead("cmt_main", testCID(t, "main"), 1, now)
	branchHead := testHead("cmt_photos", testCID(t, "photos"), 1, now)
	branchHead.Name = "heads/team/photos"
	branchHead.Kind = "explicit"
	path := filepath.Join(t.TempDir(), "buckets.json")
	mainService, err := OpenRemoteBranch(path, &fakeGateway{head: mainHead}, "bkt_one", "main")
	if err != nil {
		t.Fatal(err)
	}
	branchService, err := OpenRemoteBranch(path, &fakeGateway{head: branchHead}, "bkt_one", "heads/team/photos")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mainService.Pull(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := branchService.Pull(t.Context()); err != nil {
		t.Fatal(err)
	}
	mainWorkspace, err := mainService.Status()
	if err != nil {
		t.Fatal(err)
	}
	branchWorkspace, err := branchService.Status()
	if err != nil {
		t.Fatal(err)
	}
	if mainWorkspace.Branch != "main" || mainWorkspace.Base.CommitID != "cmt_main" {
		t.Fatalf("main workspace = %#v", mainWorkspace)
	}
	if branchWorkspace.Branch != "team/photos" || branchWorkspace.Base.CommitID != "cmt_photos" {
		t.Fatalf("branch workspace = %#v", branchWorkspace)
	}
}

func TestFailedFetchLeavesPendingStash(t *testing.T) {
	baseRoot := testCID(t, "base")
	candidateRoot := testCID(t, "candidate")
	now := time.Now().UTC()
	gateway := &fakeGateway{head: testHead("cmt_base", baseRoot, 1, now)}
	service, err := OpenRemote(filepath.Join(t.TempDir(), "buckets.json"), gateway, "bkt_one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Pull(t.Context()); err != nil {
		t.Fatal(err)
	}
	base, err := service.CurrentBase(baseRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Stage(candidateRoot, base, cid.Undef, "offline edit"); err != nil {
		t.Fatal(err)
	}
	gateway.headErr = errors.New("offline")
	if _, err := service.Push(t.Context(), candidateRoot, cid.Undef, "offline edit"); err == nil {
		t.Fatal("Push succeeded while fetch was offline")
	}
	workspace, err := service.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(workspace.Stashes) != 1 || workspace.Stashes[0].CandidateRoot != candidateRoot.String() || workspace.Stashes[0].Status != "pending" {
		t.Fatalf("pending stash = %#v", workspace.Stashes)
	}
}

func TestRestorePendingReinstatesExactFrozenPushIdentity(t *testing.T) {
	baseRoot := testCID(t, "base")
	candidateRoot := testCID(t, "candidate")
	now := time.Now().UTC()
	gateway := &fakeGateway{head: testHead("cmt_base", baseRoot, 1, now)}
	path := filepath.Join(t.TempDir(), "buckets.json")
	service, err := OpenRemote(path, gateway, "bkt_one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Pull(t.Context()); err != nil {
		t.Fatal(err)
	}
	base, err := service.CurrentBase(baseRoot)
	if err != nil {
		t.Fatal(err)
	}
	stash := Stash{
		ID: "stash-recovered", PushID: "push-recovered",
		CandidateRoot: candidateRoot.String(), Base: base, Message: "snapshot",
		RequestFrozen: true, Status: "pending", CreatedAt: now,
	}
	restored, err := service.RestorePending(stash)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ID != stash.ID || restored.PushID != stash.PushID || !restored.RequestFrozen {
		t.Fatalf("restored stash = %#v", restored)
	}
	reopened, err := OpenRemote(path, gateway, "bkt_one")
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := reopened.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(workspace.Stashes) != 1 || workspace.Stashes[0].ID != stash.ID ||
		workspace.Stashes[0].PushID != stash.PushID {
		t.Fatalf("persisted restored stash = %#v", workspace.Stashes)
	}
	if _, err := reopened.RestorePending(stash); err != nil {
		t.Fatalf("exact restore was not idempotent: %v", err)
	}
	conflict := stash
	conflict.PushID = "different"
	if _, err := reopened.RestorePending(conflict); err == nil {
		t.Fatal("conflicting restored stash identity was accepted")
	}
}

func TestPushRequiresAnObservedBase(t *testing.T) {
	gateway := &fakeGateway{}
	service, err := OpenRemote(filepath.Join(t.TempDir(), "buckets.json"), gateway, "bkt_one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Push(t.Context(), testCID(t, "candidate"), cid.Undef, ""); !errors.Is(err, ErrNotInitialized) {
		t.Fatalf("Push error = %v", err)
	}
}

func TestPushRequiresCandidateStagedAgainstCapturedBase(t *testing.T) {
	baseRoot := testCID(t, "base")
	remoteRoot := testCID(t, "remote")
	candidateRoot := testCID(t, "candidate")
	now := time.Now().UTC()
	gateway := &fakeGateway{head: testHead("cmt_base", baseRoot, 1, now)}
	service, err := OpenRemote(filepath.Join(t.TempDir(), "buckets.json"), gateway, "bkt_one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Pull(t.Context()); err != nil {
		t.Fatal(err)
	}
	captured, err := service.CurrentBase(baseRoot)
	if err != nil {
		t.Fatal(err)
	}
	gateway.head = testHead("cmt_remote", remoteRoot, 2, now)
	if _, err := service.Pull(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Push(t.Context(), candidateRoot, cid.Undef, ""); !errors.Is(err, ErrNotStaged) {
		t.Fatalf("unstaged Push error = %v", err)
	}
	if _, err := service.CurrentBase(baseRoot); err == nil {
		t.Fatal("CurrentBase accepted the stale root after Pull")
	}
	if _, err := service.Stage(candidateRoot, captured, cid.Undef, ""); err != nil {
		t.Fatal(err)
	}
	gateway.result = transport.ApplyResult{
		Status: "branched", Head: gateway.head,
		Candidate: testCommit("cmt_candidate", candidateRoot, capturedCID(t, captured.Root), []string{captured.CommitID}, "", now),
		Commit:    testCommit("cmt_candidate", candidateRoot, capturedCID(t, captured.Root), []string{captured.CommitID}, "", now),
		Branch: func() *transport.ObservedHead {
			value := testHead("cmt_candidate", candidateRoot, 1, now)
			value.Name, value.Kind = "conflicts/alice/one", "conflict"
			return &value
		}(), MergeBase: captured.Root, Conflicts: []transport.Conflict{{Coordinate: "docs/readme"}},
	}
	if _, err := service.Push(t.Context(), candidateRoot, cid.Undef, ""); err != nil {
		t.Fatal(err)
	}
	if gateway.lastPush.BaseCommit != captured.CommitID || gateway.lastPush.BaseRoot != captured.Root || gateway.lastPush.BaseRevision != captured.Revision {
		t.Fatalf("Push base = %#v, want %#v", gateway.lastPush, captured)
	}
}

type delayedHeadGateway struct {
	head    transport.ObservedHead
	started chan<- struct{}
	release <-chan struct{}
}

func (g *delayedHeadGateway) ObserveHead(context.Context) (*transport.ObservedHead, error) {
	g.started <- struct{}{}
	<-g.release
	value := g.head
	return &value, nil
}

func (*delayedHeadGateway) ApplyCandidate(context.Context, transport.ApplyRequest) (*transport.ApplyResult, error) {
	return nil, errors.New("unexpected push")
}

func TestConcurrentPullResponsesDoNotRegressWorkspaceRevision(t *testing.T) {
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "buckets.json")
	initial := &fakeGateway{head: testHead("cmt_one", testCID(t, "one"), 1, now)}
	service, err := OpenRemote(path, initial, "bkt_one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Pull(t.Context()); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	olderGateway := &delayedHeadGateway{
		head: testHead("cmt_two", testCID(t, "two"), 2, now), started: started, release: release,
	}
	older, err := OpenRemote(path, olderGateway, "bkt_one")
	if err != nil {
		t.Fatal(err)
	}
	newer, err := OpenRemote(path, &fakeGateway{head: testHead("cmt_three", testCID(t, "three"), 3, now)}, "bkt_one")
	if err != nil {
		t.Fatal(err)
	}

	var workers sync.WaitGroup
	workers.Add(1)
	var olderErr error
	go func() {
		defer workers.Done()
		_, olderErr = older.Pull(t.Context())
	}()
	<-started
	if _, err := newer.Pull(t.Context()); err != nil {
		t.Fatal(err)
	}
	close(release)
	workers.Wait()
	if olderErr != nil {
		t.Fatal(olderErr)
	}

	workspace, err := service.Status()
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Remote.Revision != 3 || workspace.Remote.CommitID != "cmt_three" || workspace.Base.Revision != 3 {
		t.Fatalf("workspace regressed after delayed response: %#v", workspace)
	}
}

type delayedPushGateway struct {
	head    transport.ObservedHead
	result  transport.ApplyResult
	started chan<- struct{}
	release <-chan struct{}
}

func (g *delayedPushGateway) ObserveHead(context.Context) (*transport.ObservedHead, error) {
	value := g.head
	return &value, nil
}

func (g *delayedPushGateway) ApplyCandidate(_ context.Context, _ transport.ApplyRequest) (*transport.ApplyResult, error) {
	g.started <- struct{}{}
	<-g.release
	value := g.result
	return &value, nil
}

func TestDelayedPushResponseDoesNotRegressNewerObservedHead(t *testing.T) {
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "buckets.json")
	baseRoot := testCID(t, "base")
	candidateRoot := testCID(t, "candidate")
	baseHead := testHead("cmt_one", baseRoot, 1, now)
	initial, err := OpenRemote(path, &fakeGateway{head: baseHead}, "bkt_one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := initial.Pull(t.Context()); err != nil {
		t.Fatal(err)
	}
	base, err := initial.CurrentBase(baseRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := initial.Stage(candidateRoot, base, cid.Undef, "delayed push"); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{}, 1)
	release := make(chan struct{})
	pushHead := testHead("cmt_two", candidateRoot, 2, now)
	candidateCommit := testCommit("cmt_two", candidateRoot, baseRoot, []string{"cmt_one"}, "delayed push", now)
	pusher, err := OpenRemote(path, &delayedPushGateway{
		head: baseHead, result: transport.ApplyResult{Status: "fast_forward", Head: pushHead, Candidate: candidateCommit, Commit: candidateCommit},
		started: started, release: release,
	}, "bkt_one")
	if err != nil {
		t.Fatal(err)
	}

	var workers sync.WaitGroup
	workers.Add(1)
	var pushErr error
	go func() {
		defer workers.Done()
		_, pushErr = pusher.Push(t.Context(), candidateRoot, cid.Undef, "delayed push")
	}()
	<-started
	newerHead := testHead("cmt_three", testCID(t, "newer"), 3, now)
	observer, err := OpenRemote(path, &fakeGateway{head: newerHead}, "bkt_one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := observer.Pull(t.Context()); err != nil {
		t.Fatal(err)
	}
	close(release)
	workers.Wait()
	if pushErr != nil {
		t.Fatal(pushErr)
	}

	workspace, err := initial.Status()
	if err != nil {
		t.Fatal(err)
	}
	if workspace.Remote.Revision != 3 || workspace.Base.Revision != 3 || len(workspace.Stashes) != 0 {
		t.Fatalf("workspace regressed after delayed push response: %#v", workspace)
	}
}

func TestInvalidPushResultLeavesPendingStash(t *testing.T) {
	now := time.Now().UTC()
	baseRoot := testCID(t, "base")
	candidateRoot := testCID(t, "candidate")
	path := filepath.Join(t.TempDir(), "buckets.json")
	baseHead := testHead("opaque-base", baseRoot, 1, now)
	gateway := &fakeGateway{head: baseHead}
	service, err := OpenRemote(path, gateway, "bkt_one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Pull(t.Context()); err != nil {
		t.Fatal(err)
	}
	base, err := service.CurrentBase(baseRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Stage(candidateRoot, base, cid.Undef, "local edit"); err != nil {
		t.Fatal(err)
	}
	candidate := testCommit("opaque-candidate", candidateRoot, baseRoot, []string{"opaque-base"}, "local edit", now)
	gateway.result = transport.ApplyResult{
		Status: "fast_forward", Head: testHead("another-version", candidateRoot, 2, now), Candidate: candidate, Commit: candidate,
	}
	if _, err := service.Push(t.Context(), candidateRoot, cid.Undef, "local edit"); err == nil {
		t.Fatal("Push accepted a result whose main head did not point to the final commit")
	}
	workspace, err := service.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(workspace.Stashes) != 1 || workspace.Stashes[0].Status != "pending" {
		t.Fatalf("invalid response changed pending stash: %#v", workspace.Stashes)
	}
}

func TestPushResultsDoNotOrderAgainstDescriptiveBaseRevision(t *testing.T) {
	baseRoot := testCID(t, "base")
	candidateRoot := testCID(t, "candidate")
	remoteRoot := testCID(t, "remote")
	mergedRoot := testCID(t, "merged")
	now := time.Now().UTC()
	candidate := testCommit("opaque-candidate", candidateRoot, baseRoot, []string{"opaque-base"}, "local edit", now)

	tests := []struct {
		name       string
		result     transport.ApplyResult
		wantStatus string
		wantStash  int
	}{
		{
			name: "fast-forward",
			result: transport.ApplyResult{
				Status: "fast_forward", Head: testHead(candidate.ID, candidateRoot, 2, now), Candidate: candidate, Commit: candidate,
			},
		},
		{
			name: "merged",
			result: transport.ApplyResult{
				Status:    "merged",
				Head:      testHead("opaque-merge", mergedRoot, 2, now),
				Candidate: candidate,
				Commit:    testCommit("opaque-merge", mergedRoot, remoteRoot, []string{"opaque-remote", candidate.ID}, "gateway auto-merge", now),
				MergeBase: baseRoot.String(),
			},
		},
		{
			name: "branched",
			result: transport.ApplyResult{
				Status: "branched", Head: testHead("opaque-base", baseRoot, 2, now), Candidate: candidate, Commit: candidate,
				Branch: func() *transport.ObservedHead {
					value := testHead(candidate.ID, candidateRoot, 1, now)
					value.Name, value.Kind = "conflicts/alice/one", "conflict"
					return &value
				}(),
				MergeBase: baseRoot.String(), Conflicts: []transport.Conflict{{Coordinate: "docs/readme"}},
			},
			wantStatus: "branched",
			wantStash:  1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// The captured revision is deliberately unrelated to the result
			// revision. It describes the observation and is not a CAS token.
			gateway := &fakeGateway{head: testHead("opaque-base", baseRoot, 50, now), result: test.result}
			service, err := OpenRemote(filepath.Join(t.TempDir(), "buckets.json"), gateway, "bkt_one")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Pull(t.Context()); err != nil {
				t.Fatal(err)
			}
			base, err := service.CurrentBase(baseRoot)
			if err != nil {
				t.Fatal(err)
			}
			if base.Revision != 50 {
				t.Fatalf("captured base revision = %d", base.Revision)
			}
			if _, err := service.Stage(candidateRoot, base, cid.Undef, "local edit"); err != nil {
				t.Fatal(err)
			}
			outcome, err := service.Push(t.Context(), candidateRoot, cid.Undef, "local edit")
			if err != nil {
				t.Fatalf("Push rejected structurally valid %s replay: %v", test.name, err)
			}
			if gateway.lastPush.BaseRevision != 50 || outcome.Result.Head.Revision != 2 {
				t.Fatalf("%s did not exercise mismatched descriptive revisions: request=%d result=%d", test.name, gateway.lastPush.BaseRevision, outcome.Result.Head.Revision)
			}
			if len(outcome.Workspace.Stashes) != test.wantStash {
				t.Fatalf("stashes after %s = %#v", test.name, outcome.Workspace.Stashes)
			}
			if test.wantStatus != "" && outcome.Workspace.Stashes[0].Status != test.wantStatus {
				t.Fatalf("stash status after %s = %q", test.name, outcome.Workspace.Stashes[0].Status)
			}
		})
	}
}

func TestPushRetryAfterResponseLossReusesFrozenRequest(t *testing.T) {
	now := time.Now().UTC()
	baseRoot := testCID(t, "base")
	candidateRoot := testCID(t, "candidate")
	path := filepath.Join(t.TempDir(), "buckets.json")
	baseHead := testHead("opaque-base", baseRoot, 1, now)
	firstGateway := &fakeGateway{head: baseHead, pushErr: errors.New("connection reset after request commit")}
	first, err := OpenRemote(path, firstGateway, "bkt_one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Pull(t.Context()); err != nil {
		t.Fatal(err)
	}
	base, err := first.CurrentBase(baseRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Stage(candidateRoot, base, cid.Undef, "staged message"); err != nil {
		t.Fatal(err)
	}
	if _, err := first.Push(t.Context(), candidateRoot, cid.Undef, "first request"); err == nil {
		t.Fatal("Push succeeded despite response loss")
	}
	original := firstGateway.lastPush
	if original.OperationID == "" || original.Message != "first request" {
		t.Fatalf("first push request = %#v", original)
	}

	candidate := testCommit("opaque-candidate", candidateRoot, baseRoot, []string{"opaque-base"}, "first request", now)
	replayGateway := &fakeGateway{
		head: testHead("opaque-candidate", candidateRoot, 2, now),
		result: transport.ApplyResult{
			Status: "fast_forward", Head: testHead("opaque-candidate", candidateRoot, 2, now), Candidate: candidate, Commit: candidate,
		},
	}
	reopened, err := OpenRemote(path, replayGateway, "bkt_one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reopened.Push(t.Context(), candidateRoot, cid.Undef, "different retry"); err == nil || !strings.Contains(err.Error(), "retry message") {
		t.Fatalf("changed retry error = %v", err)
	}
	if replayGateway.lastPush.OperationID != "" {
		t.Fatal("changed retry reached the Gateway")
	}
	if _, err := reopened.Push(t.Context(), candidateRoot, cid.Undef, ""); err != nil {
		t.Fatal(err)
	}
	if replayGateway.lastPush != original {
		t.Fatalf("replayed request = %#v, want %#v", replayGateway.lastPush, original)
	}
	workspace, err := reopened.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(workspace.Stashes) != 0 {
		t.Fatalf("successful replay left stash: %#v", workspace.Stashes)
	}
}

func TestCurrentStashFreezesRequestOnFirstPush(t *testing.T) {
	baseRoot := testCID(t, "base")
	candidateRoot := testCID(t, "candidate")
	now := time.Now().UTC()
	gateway := &fakeGateway{head: testHead("opaque-base", baseRoot, 1, now), pushErr: errors.New("offline")}
	service, err := OpenRemote(filepath.Join(t.TempDir(), "buckets.json"), gateway, "bkt_one")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Pull(t.Context()); err != nil {
		t.Fatal(err)
	}
	base, err := service.CurrentBase(baseRoot)
	if err != nil {
		t.Fatal(err)
	}
	stash, err := service.Stage(candidateRoot, base, cid.Undef, "newly staged")
	if err != nil {
		t.Fatal(err)
	}
	if stash.RequestFrozen {
		t.Fatal("newly staged request was already frozen")
	}
	data, err := os.ReadFile(service.path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"request_frozen": false`) {
		t.Fatalf("current stash did not persist an explicit request_frozen=false: %s", data)
	}
	if _, err := service.Push(t.Context(), candidateRoot, cid.Undef, "first push override"); err == nil {
		t.Fatal("Push succeeded while Gateway was offline")
	}
	workspace, err := service.Status()
	if err != nil {
		t.Fatal(err)
	}
	if len(workspace.Stashes) != 1 || !workspace.Stashes[0].RequestFrozen || workspace.Stashes[0].Message != "first push override" {
		t.Fatalf("first push did not freeze current stash: %#v", workspace.Stashes)
	}
}

func TestCurrentMissingRequestFrozenIsRejected(t *testing.T) {
	baseRoot := testCID(t, "base")
	candidateRoot := testCID(t, "candidate")
	now := time.Now().UTC()
	path := filepath.Join(t.TempDir(), "buckets.json")
	base := Head{CommitID: "opaque-base", Root: baseRoot.String(), Revision: 1}
	writeWorkspaceWithoutRequestFrozen(t, path, bucketWorkspaceVersion, Workspace{
		BucketID: "bkt_one", Initialized: true, Base: base, Remote: base,
		Stashes: []Stash{{
			ID: "incomplete-v2", PushID: "push-incomplete", CandidateRoot: candidateRoot.String(), Base: base,
			Message: "original", Status: "pending", CreatedAt: now, UpdatedAt: now,
		}},
		UpdatedAt: now,
	})

	if _, err := OpenRemote(path, &fakeGateway{}, "bkt_one"); err == nil || !strings.Contains(err.Error(), "lacks explicit request_frozen") {
		t.Fatalf("Open error for incomplete current state = %v", err)
	}
}

func testHead(commit string, root cid.Cid, revision uint64, now time.Time) transport.ObservedHead {
	return transport.ObservedHead{
		DatasetID: "bkt_one", Name: "main", Kind: "main", State: "open", CommitID: commit,
		Root: root.String(), Revision: revision, CreatedAt: now, UpdatedAt: now,
	}
}

func writeWorkspaceWithoutRequestFrozen(t *testing.T, path string, version int, workspace Workspace) {
	t.Helper()
	state := persistedState{Version: version, Workspaces: map[string]Workspace{workspaceKey(workspace.BucketID, "main"): workspace}}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	workspaces, ok := persisted["workspaces"].(map[string]any)
	if !ok {
		t.Fatal("version 1 fixture has no workspaces object")
	}
	for _, rawWorkspace := range workspaces {
		workspaceObject, ok := rawWorkspace.(map[string]any)
		if !ok {
			t.Fatal("version 1 fixture has an invalid workspace")
		}
		rawStashes, _ := workspaceObject["stashes"].([]any)
		for _, rawStash := range rawStashes {
			stashObject, ok := rawStash.(map[string]any)
			if !ok {
				t.Fatal("version 1 fixture has an invalid stash")
			}
			delete(stashObject, "request_frozen")
		}
	}
	data, err = json.MarshalIndent(persisted, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readPersistedState(t *testing.T, path string) persistedState {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	return state
}

func testCID(t *testing.T, value string) cid.Cid {
	t.Helper()
	key, err := clientcas.CIDForBlock(clientcas.Block{Data: []byte(value), Codec: cid.Raw})
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func capturedCID(t *testing.T, value string) cid.Cid {
	t.Helper()
	parsed, err := cid.Parse(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func testCommit(id string, root, baseRoot cid.Cid, parents []string, message string, now time.Time) transport.Commit {
	base := ""
	if baseRoot.Defined() {
		base = baseRoot.String()
	}
	return transport.Commit{
		ID: id, DatasetID: "bkt_one", Root: root.String(), Parents: parents, BaseRoot: base,
		Author: "alice", Message: message, CreatedAt: now,
	}
}

func testDatasetBinding(head transport.ObservedHead) transport.DatasetBinding {
	branch := strings.TrimPrefix(head.Name, "heads/")
	if branch == "" {
		branch = "main"
	}
	return transport.DatasetBinding{DatasetID: "bkt_one", Branch: branch}
}
func (f *fakeGateway) DatasetBinding() transport.DatasetBinding { return testDatasetBinding(f.head) }
func (g *delayedHeadGateway) DatasetBinding() transport.DatasetBinding {
	return testDatasetBinding(g.head)
}
func (g *delayedPushGateway) DatasetBinding() transport.DatasetBinding {
	return testDatasetBinding(g.head)
}

func TestRejectsRetiredWorkspaceVersions(t *testing.T) {
	for _, version := range []int{1, 2} {
		path := filepath.Join(t.TempDir(), "buckets.json")
		before, err := json.Marshal(persistedState{Version: version, Workspaces: map[string]Workspace{}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, before, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenRemote(path, &fakeGateway{}, "bkt_one"); err == nil {
			t.Fatal("accepted retired workspace version")
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Fatal("rejected workspace was rewritten")
		}
	}
}

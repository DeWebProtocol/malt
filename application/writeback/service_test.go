package writeback

import (
	"context"
	"errors"
	"sync"
	"testing"

	filesystemservice "github.com/dewebprotocol/malt-client/filesystem/service"
	"github.com/dewebprotocol/malt-client/filesystem/staging"
	"github.com/dewebprotocol/malt-client/journal"
	"github.com/dewebprotocol/malt-client/writeplan"
	"github.com/dewebprotocol/malt-core/auth/commitment/kzg"
	"github.com/dewebprotocol/malt-core/derivation"
	"github.com/dewebprotocol/malt-core/engine"
	"github.com/dewebprotocol/malt-core/maltcid"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestReplayComputesPersistsAndRecordsUnacceptedCandidate(t *testing.T) {
	fixture := newWritebackFixture(t)
	service := fixture.service(t)
	result, err := service.Replay(t.Context(), fixture.view)
	if err != nil {
		t.Fatal(err)
	}
	if result.Profile != ResultProfile || !result.BaseRoot.Equals(fixture.view.Root) || !result.CandidateRoot.Defined() || result.CandidateRoot.Equals(result.BaseRoot) || !result.RemotePersisted || !result.CandidateStored || result.RootAccepted {
		t.Fatalf("write-back result=%#v", result)
	}
	if fixture.remote.submitted == nil || fixture.remote.submitted.Root != result.CandidateRoot.String() {
		t.Fatalf("submitted bundle=%#v", fixture.remote.submitted)
	}
	if !fixture.roots.accepted.Equals(fixture.view.Root) || !fixture.roots.candidate.Equals(result.CandidateRoot) {
		t.Fatalf("root policy accepted=%s candidate=%s", fixture.roots.accepted, fixture.roots.candidate)
	}
	if fixture.queue.completed != 1 || fixture.queue.conflicted != 0 || !fixture.queue.candidate.Equals(result.CandidateRoot) {
		t.Fatalf("queue state=%#v", fixture.queue)
	}
	if fixture.payloads.puts != 1 || !fixture.payloads.stored.Equals(fixture.payload) {
		t.Fatalf("payload store=%#v", fixture.payloads)
	}
}

func TestReplayCompletesVerifiedNoChangeWithoutCandidateOrMutation(t *testing.T) {
	fixture := newWritebackFixture(t)
	fixture.noChange = true
	result, err := fixture.service(t).Replay(t.Context(), fixture.view)
	if err != nil {
		t.Fatal(err)
	}
	if !result.NoAuthenticatedChange || result.RemotePersisted || result.CandidateRoot.Defined() || result.CandidateStored || result.RootAccepted {
		t.Fatalf("no-change write-back result=%#v", result)
	}
	if fixture.payloads.puts != 0 || fixture.plans != 1 || fixture.remote.submitted != nil || fixture.roots.candidate.Defined() {
		t.Fatalf("no-change write-back performed a mutation: remote=%#v roots=%#v", fixture.remote, fixture.roots)
	}
	if fixture.queue.completed != 1 || !fixture.queue.candidate.Equals(fixture.view.Root) || fixture.queue.conflicted != 0 {
		t.Fatalf("no-change queue state=%#v", fixture.queue)
	}
}

func TestReplayUploadsOnlyFinalIntentPayloads(t *testing.T) {
	fixture := newWritebackFixture(t)
	secretBody := []byte("overwritten secret")
	secret := writebackRawCID(t, secretBody)
	secretWrite := fixture.queue.batch.Operations[0]
	secretWrite.OperationID = "op-secret"
	secretWrite.RetryID = "retry-secret"
	secretWrite.Sequence = 0
	secretWrite.PayloadCID = secret.String()
	fixture.queue.batch.Operations = append([]journal.Operation{secretWrite}, fixture.queue.batch.Operations...)
	fixture.queue.batch.Pending = append([]journal.Operation{secretWrite}, fixture.queue.batch.Pending...)
	fixture.queue.batch.Payloads = append(fixture.queue.batch.Payloads, staging.UploadPayload{CID: secret, Body: secretBody})
	result, err := fixture.service(t).Replay(t.Context(), fixture.view)
	if err != nil {
		t.Fatal(err)
	}
	if !result.RemotePersisted || fixture.payloads.puts != 1 || !fixture.payloads.stored.Equals(fixture.payload) {
		t.Fatalf("selected payload upload result=%#v payloads=%#v", result, fixture.payloads)
	}
}

func TestReplayRejectsPlannerRequiredUnstagedPayload(t *testing.T) {
	fixture := newWritebackFixture(t)
	fixture.queue.batch.Payloads = nil
	if _, err := fixture.service(t).Replay(t.Context(), fixture.view); err == nil {
		t.Fatal("final intent payload missing from staging was accepted")
	}
	if fixture.payloads.puts != 0 || fixture.remote.submitted != nil || fixture.queue.completed != 0 {
		t.Fatalf("unstaged required payload caused side effects: payloads=%#v remote=%#v queue=%#v", fixture.payloads, fixture.remote, fixture.queue)
	}
}

func TestReplayNoChangePreservesConflictWhenAcceptedRootAdvances(t *testing.T) {
	fixture := newWritebackFixture(t)
	fixture.noChange = true
	fixture.roots.advanceOnRecheck = writebackRawCID(t, []byte("advanced during no-change replay"))
	result, err := fixture.service(t).Replay(t.Context(), fixture.view)
	if !errors.Is(err, ErrStaleAcceptedView) {
		t.Fatalf("no-change accepted-root race error=%v", err)
	}
	if result.NoAuthenticatedChange || result.RemotePersisted || fixture.queue.completed != 0 || fixture.queue.conflicted != 1 || fixture.remote.submitted != nil {
		t.Fatalf("no-change accepted-root race changed state: result=%#v queue=%#v", result, fixture.queue)
	}
}

func TestReplayRejectsMaliciousReceiptWithoutCompletingOrRecordingCandidate(t *testing.T) {
	fixture := newWritebackFixture(t)
	fixture.remote.substituteReceipt = true
	result, err := fixture.service(t).Replay(t.Context(), fixture.view)
	if err == nil {
		t.Fatal("substituted materialization receipt was accepted")
	}
	if result.RemotePersisted || result.CandidateStored || result.RootAccepted || fixture.queue.completed != 0 || fixture.roots.candidate.Defined() {
		t.Fatalf("malicious receipt changed local state: result=%#v queue=%#v roots=%#v", result, fixture.queue, fixture.roots)
	}
	if fixture.queue.prepared != 1 || fixture.queue.conflicted != 0 {
		t.Fatalf("ambiguous failed request did not remain pending: %#v", fixture.queue)
	}
}

func TestReplayRejectsPayloadCIDSubstitutionBeforePayloadPublication(t *testing.T) {
	fixture := newWritebackFixture(t)
	fixture.payloads.substitute = writebackRawCID(t, []byte("wrong"))
	if _, err := fixture.service(t).Replay(t.Context(), fixture.view); err == nil {
		t.Fatal("payload CID substitution was accepted")
	}
	if fixture.plans != 1 || fixture.remote.submitted != nil || fixture.queue.completed != 0 || fixture.roots.candidate.Defined() {
		t.Fatalf("write-back continued after payload substitution: remote=%#v queue=%#v", fixture.remote, fixture.queue)
	}
}

func TestReplayRecomputesStagedPayloadCIDBeforeCallingStore(t *testing.T) {
	fixture := newWritebackFixture(t)
	fixture.queue.batch.Payloads[0].Body = []byte("corrupt local staging body")
	if _, err := fixture.service(t).Replay(t.Context(), fixture.view); err == nil {
		t.Fatal("staged payload body with a false CID was accepted")
	}
	if fixture.payloads.puts != 0 || fixture.plans != 0 || fixture.remote.submitted != nil || fixture.queue.completed != 0 || fixture.roots.candidate.Defined() {
		t.Fatalf("write-back continued after local payload corruption: payloads=%#v remote=%#v queue=%#v", fixture.payloads, fixture.remote, fixture.queue)
	}
}

func TestReplayRejectsUndefinedStagedPayloadCIDBeforeCallingStore(t *testing.T) {
	fixture := newWritebackFixture(t)
	fixture.queue.batch.Payloads[0].CID = cid.Undef
	if _, err := fixture.service(t).Replay(t.Context(), fixture.view); err == nil {
		t.Fatal("undefined staged payload CID was accepted")
	}
	if fixture.payloads.puts != 0 || fixture.plans != 0 {
		t.Fatalf("write-back continued after undefined payload CID: payloads=%#v remote=%#v", fixture.payloads, fixture.remote)
	}
}

func TestReplayRejectsStaleAcceptedRootBeforeFreezingQueue(t *testing.T) {
	fixture := newWritebackFixture(t)
	fixture.roots.accepted = writebackRawCID(t, []byte("new accepted"))
	if _, err := fixture.service(t).Replay(t.Context(), fixture.view); !errors.Is(err, ErrStaleAcceptedView) {
		t.Fatalf("stale accepted View error=%v", err)
	}
	if fixture.queue.prepared != 0 || fixture.payloads.puts != 0 || fixture.plans != 0 {
		t.Fatalf("stale View performed I/O: queue=%#v payloads=%#v remote=%#v", fixture.queue, fixture.payloads, fixture.remote)
	}
}

func TestReplayPreservesConflictWhenAcceptedRootAdvancesAfterReceipt(t *testing.T) {
	fixture := newWritebackFixture(t)
	advanced := writebackRawCID(t, []byte("advanced accepted"))
	fixture.roots.advanceOnObserve = advanced
	result, err := fixture.service(t).Replay(t.Context(), fixture.view)
	if !errors.Is(err, ErrStaleAcceptedView) {
		t.Fatalf("accepted-root race error=%v", err)
	}
	if !result.RemotePersisted || !result.CandidateRoot.Defined() || result.CandidateStored || result.RootAccepted {
		t.Fatalf("accepted-root race result=%#v", result)
	}
	if fixture.queue.completed != 0 || fixture.queue.conflicted != 1 || fixture.queue.conflictID == "" {
		t.Fatalf("accepted-root race queue=%#v", fixture.queue)
	}
}

func TestReplayPreservesConflictWhenAcceptedRootAdvancesAfterCandidateObservation(t *testing.T) {
	fixture := newWritebackFixture(t)
	advanced := writebackRawCID(t, []byte("advanced after candidate observation"))
	fixture.roots.completionEntered = make(chan struct{})
	fixture.roots.allowCompletion = make(chan struct{})
	service := fixture.service(t)
	type replayOutcome struct {
		result Result
		err    error
	}
	done := make(chan replayOutcome, 1)
	go func() {
		result, err := service.Replay(t.Context(), fixture.view)
		done <- replayOutcome{result: result, err: err}
	}()
	<-fixture.roots.completionEntered
	fixture.roots.advanceAccepted(advanced)
	close(fixture.roots.allowCompletion)
	outcome := <-done
	result, err := outcome.result, outcome.err
	if !errors.Is(err, ErrStaleAcceptedView) {
		t.Fatalf("post-candidate accepted-root race error=%v", err)
	}
	if !result.RemotePersisted || !result.CandidateRoot.Defined() || !result.CandidateStored || result.RootAccepted {
		t.Fatalf("post-candidate accepted-root race result=%#v", result)
	}
	if !fixture.roots.candidate.Equals(result.CandidateRoot) || !fixture.roots.accepted.Equals(advanced) {
		t.Fatalf("post-candidate root state=%#v", fixture.roots)
	}
	if fixture.queue.completed != 0 || fixture.queue.conflicted != 1 || fixture.queue.conflictID == "" {
		t.Fatalf("post-candidate queue=%#v", fixture.queue)
	}
}

type writebackFixture struct {
	view      filesystemservice.View
	candidate protocol.AuthenticationCandidate
	payload   cid.Cid
	queue     *fakeQueue
	payloads  *fakePayloadStore
	remote    *fakeAuthenticationRemote
	roots     *fakeRootPolicy
	noChange  bool
	plans     int
}

func newWritebackFixture(t *testing.T) *writebackFixture {
	t.Helper()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatal(err)
	}
	profiles := engine.NewRegistry()
	if err := profiles.Register(scheme); err != nil {
		t.Fatal(err)
	}
	e := engine.New(profiles)
	oldPayload := writebackRawCID(t, []byte("old"))
	newBody := []byte("new")
	newPayload := writebackRawCID(t, newBody)
	state := engine.State{Descriptor: maltcid.RootDescriptor{Layout: maltcid.Prefix, DerivationProfile: uint8(derivation.SHA256), Profile: maltcid.KZG4096}, Entries: []engine.Entry{{Label: []byte("payload"), Target: oldPayload}}}
	base, err := authentication.Prepare(t.Context(), e, state)
	if err != nil {
		t.Fatal(err)
	}
	state.Entries = []engine.Entry{{Label: []byte("payload"), Target: newPayload}}
	candidate, err := authentication.PrepareUpdate(t.Context(), e, base, state)
	if err != nil {
		t.Fatal(err)
	}
	oldRoot := cid.MustParse(base.Root)
	operation := journal.Operation{Intent: journal.Intent{OperationID: "op-one", RetryID: "retry-one", DatasetID: "dataset", Branch: "main", BaseRoot: oldRoot.String(), BaseRevision: 7, Kind: journal.KindWrite, Path: "docs/file.txt", PayloadCID: newPayload.String()}, Sequence: 1, Status: journal.StatusPendingUpload}
	view := filesystemservice.View{DatasetID: "dataset", Branch: "main", Root: oldRoot, Revision: 7}
	batch := staging.UploadBatch{View: view, TransactionID: "fs-writeback-one", Operations: []journal.Operation{operation}, Pending: []journal.Operation{operation}, Payloads: []staging.UploadPayload{{CID: newPayload, Body: newBody}}}
	return &writebackFixture{view: view, candidate: candidate, payload: newPayload,
		queue: &fakeQueue{batch: batch}, payloads: &fakePayloadStore{expected: newPayload},
		remote: &fakeAuthenticationRemote{engine: e}, roots: &fakeRootPolicy{accepted: oldRoot}}
}

func (f *writebackFixture) service(t *testing.T) *Service {
	t.Helper()
	service, err := New(Options{Queue: f.queue, Payloads: f.payloads, Remote: f.remote, Planner: f, Roots: f.roots, TrustAlias: "docs", Source: "test write-back"})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type fakeQueue struct {
	batch      staging.UploadBatch
	prepared   int
	completed  int
	conflicted int
	conflictID string
	candidate  cid.Cid
}

func (q *fakeQueue) PrepareUpload(_ context.Context, view filesystemservice.View) (staging.UploadBatch, error) {
	q.prepared++
	if !view.Root.Equals(q.batch.View.Root) {
		return staging.UploadBatch{}, errors.New("wrong queue View")
	}
	return q.batch, nil
}

func (q *fakeQueue) CompleteUpload(_ context.Context, batch staging.UploadBatch, candidate cid.Cid) ([]journal.Operation, error) {
	if batch.TransactionID != q.batch.TransactionID {
		return nil, errors.New("wrong completion batch")
	}
	q.completed++
	q.candidate = candidate
	completed := append([]journal.Operation(nil), batch.Pending...)
	for index := range completed {
		completed[index].Status = journal.StatusCompleted
		completed[index].ResultRoot = candidate.String()
	}
	return completed, nil
}

func (q *fakeQueue) CompleteNoChange(_ context.Context, batch staging.UploadBatch) ([]journal.Operation, error) {
	return q.CompleteUpload(context.Background(), batch, batch.View.Root)
}

func (q *fakeQueue) MarkUploadConflicted(_ context.Context, batch staging.UploadBatch, conflictID string) ([]journal.Operation, error) {
	if batch.TransactionID != q.batch.TransactionID {
		return nil, errors.New("wrong conflict batch")
	}
	q.conflicted++
	q.conflictID = conflictID
	return append([]journal.Operation(nil), batch.Pending...), nil
}

type fakePayloadStore struct {
	expected   cid.Cid
	substitute cid.Cid
	stored     cid.Cid
	puts       int
}

func (s *fakePayloadStore) PutWithCodec(_ context.Context, body []byte, codec uint64) (cid.Cid, error) {
	s.puts++
	computed, err := s.expected.Prefix().Sum(body)
	if err != nil {
		return cid.Undef, err
	}
	if !computed.Equals(s.expected) {
		return cid.Undef, errors.New("unexpected payload body")
	}
	if s.substitute.Defined() {
		return s.substitute, nil
	}
	s.stored = computed
	return computed, nil
}

type fakeAuthenticationRemote struct {
	engine            *engine.Engine
	submitted         *protocol.AuthenticationBatch
	substituteReceipt bool
}

func (r *fakeAuthenticationRemote) MaterializeAuthenticationBatch(ctx context.Context, batch protocol.AuthenticationBatch) (protocol.AuthenticationReceipt, error) {
	if err := authentication.ValidateBatch(ctx, r.engine, batch); err != nil {
		return protocol.AuthenticationReceipt{}, err
	}
	copyBatch := batch
	r.submitted = &copyBatch
	digest, err := batch.Digest()
	if err != nil {
		return protocol.AuthenticationReceipt{}, err
	}
	root := batch.Root
	if r.substituteReceipt {
		root = batch.Base
	}
	return protocol.AuthenticationReceipt{Profile: protocol.AuthenticationReceiptProfile, TransactionID: batch.TransactionID, Base: batch.Base, Root: root, Digest: digest, DurableBoundary: "test-atomic-v1"}, nil
}

type fakeRootPolicy struct {
	mu                sync.Mutex
	accepted          cid.Cid
	candidate         cid.Cid
	advanceOnObserve  cid.Cid
	advanceOnRecheck  cid.Cid
	completionEntered chan struct{}
	allowCompletion   chan struct{}
	completionOnce    sync.Once
}

func (p *fakeRootPolicy) AcceptedRoot(string) (cid.Cid, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.accepted, nil
}

func (p *fakeRootPolicy) CompleteIfAccepted(_ string, expected cid.Cid, operation func() error) (bool, error) {
	if p.completionEntered != nil {
		p.completionOnce.Do(func() { close(p.completionEntered) })
		<-p.allowCompletion
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.advanceOnRecheck.Defined() {
		p.accepted = p.advanceOnRecheck
	}
	if !p.accepted.Equals(expected) {
		return false, nil
	}
	return true, operation()
}

func (p *fakeRootPolicy) ObserveCandidate(_ string, candidate, base cid.Cid, _ string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !base.Equals(p.accepted) {
		return errors.New("candidate base is stale")
	}
	if p.advanceOnObserve.Defined() {
		p.accepted = p.advanceOnObserve
		return errors.New("accepted root advanced")
	}
	p.candidate = candidate
	return nil
}

func (p *fakeRootPolicy) advanceAccepted(root cid.Cid) {
	p.mu.Lock()
	p.accepted = root
	p.mu.Unlock()
}

func writebackRawCID(t *testing.T, body []byte) cid.Cid {
	t.Helper()
	digest, err := mh.Sum(body, mh.SHA2_256, -1)
	if err != nil {
		t.Fatal(err)
	}
	return cid.NewCidV1(cid.Raw, digest)
}

func (f *writebackFixture) Prepare(_ context.Context, base cid.Cid, operations []journal.Operation) (writeplan.Plan, error) {
	f.plans++
	if !base.Equals(f.view.Root) || len(operations) == 0 || operations[len(operations)-1].OperationID != "op-one" {
		return writeplan.Plan{}, errors.New("planner received substituted input")
	}
	if f.noChange {
		return writeplan.Plan{Base: base, Root: base}, nil
	}
	return writeplan.Plan{Base: base, Root: cid.MustParse(f.candidate.Root), Candidates: []protocol.AuthenticationCandidate{f.candidate}, Required: []cid.Cid{f.payload}}, nil
}

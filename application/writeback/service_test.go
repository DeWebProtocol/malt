package writeback

import (
	"context"
	"errors"
	"testing"

	clientrootapp "github.com/dewebprotocol/malt-client/application/clientroot"
	filesystemservice "github.com/dewebprotocol/malt-client/filesystem/service"
	"github.com/dewebprotocol/malt-client/filesystem/staging"
	"github.com/dewebprotocol/malt-client/journal"
	"github.com/dewebprotocol/malt-core/auth/arcset"
	materializermemory "github.com/dewebprotocol/malt-core/auth/arcset/materializer/memory"
	"github.com/dewebprotocol/malt-core/auth/commitment"
	"github.com/dewebprotocol/malt-core/auth/commitment/kzg"
	"github.com/dewebprotocol/malt-core/auth/semantic/mapping"
	mappingradix "github.com/dewebprotocol/malt-core/auth/semantic/mapping/radix"
	"github.com/dewebprotocol/malt-core/mutation"
	"github.com/dewebprotocol/malt-core/protocol"
	clientwriter "github.com/dewebprotocol/malt-core/sdk/writer"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
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
	if fixture.remote.submitted == nil || !fixture.remote.submitted.Candidate.Equals(result.CandidateRoot) {
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

func TestReplayRejectsPayloadCIDSubstitutionBeforeClientRootRequest(t *testing.T) {
	fixture := newWritebackFixture(t)
	fixture.payloads.substitute = writebackRawCID(t, []byte("wrong"))
	if _, err := fixture.service(t).Replay(t.Context(), fixture.view); err == nil {
		t.Fatal("payload CID substitution was accepted")
	}
	if fixture.remote.fetches != 0 || fixture.remote.submitted != nil || fixture.queue.completed != 0 || fixture.roots.candidate.Defined() {
		t.Fatalf("write-back continued after payload substitution: remote=%#v queue=%#v", fixture.remote, fixture.queue)
	}
}

func TestReplayRejectsStaleAcceptedRootBeforeFreezingQueue(t *testing.T) {
	fixture := newWritebackFixture(t)
	fixture.roots.accepted = writebackRawCID(t, []byte("new accepted"))
	if _, err := fixture.service(t).Replay(t.Context(), fixture.view); !errors.Is(err, ErrStaleAcceptedView) {
		t.Fatalf("stale accepted View error=%v", err)
	}
	if fixture.queue.prepared != 0 || fixture.payloads.puts != 0 || fixture.remote.fetches != 0 {
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

type writebackFixture struct {
	view     filesystemservice.View
	update   mutation.UpdateView
	intent   mutation.SemanticIntent
	payload  cid.Cid
	runtime  *clientwriter.Runtime
	queue    *fakeQueue
	payloads *fakePayloadStore
	remote   *fakeClientRootRemote
	roots    *fakeRootPolicy
}

func newWritebackFixture(t *testing.T) *writebackFixture {
	t.Helper()
	ctx := t.Context()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatal(err)
	}
	oldPayload := writebackRawCID(t, []byte("old"))
	newBody := []byte("new")
	newPayload := writebackRawCID(t, newBody)
	oldMap, err := mappingradix.NewMap(scheme, materializermemory.New(true))
	if err != nil {
		t.Fatal(err)
	}
	oldRoot, err := oldMap.Commit(ctx, "writeback-fixture", mapping.NewViewFrom(map[string]cid.Cid{"payload": oldPayload}))
	if err != nil {
		t.Fatal(err)
	}
	coordinate, err := arcset.NewMapCoordinate("payload")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := arcset.NewCanonicalArcSet(arcset.KindMap, []arcset.ArcEntry{{Coordinate: coordinate, Target: arcset.NewCASTarget(oldPayload)}})
	if err != nil {
		t.Fatal(err)
	}
	update := mutation.UpdateView{
		Profile: mutation.UpdateViewProfile, StateProfile: mutation.StatefulCompleteVectorsProfile,
		BaseRoot: oldRoot, Bounds: mutation.UpdateViewBounds{MaxObjects: 8, MaxTotalEntries: 64, MaxDepth: 8},
		Objects: []mutation.UpdateObject{{ObjectID: "root", Root: oldRoot, Kind: arcset.KindMap, Entries: entries}},
	}
	before, after := arcset.NewCASTarget(oldPayload), arcset.NewCASTarget(newPayload)
	intent := mutation.SemanticIntent{
		Profile: mutation.SemanticIntentProfile, BaseRoot: oldRoot, TopOutputID: "root-output",
		Transitions: []mutation.IntentTransition{{
			ID: "root-output", ObjectID: "root", OldRoot: oldRoot, Kind: arcset.KindMap,
			Backend: maltcid.BackendKindKZG,
			Changes: []mutation.IntentChange{{Coordinate: coordinate, Before: &before, After: &after}},
		}},
	}
	runtime, err := clientwriter.NewRuntime(materializermemory.New(true), map[maltcid.BackendKind]commitment.IndexCommitment{maltcid.BackendKindKZG: scheme})
	if err != nil {
		t.Fatal(err)
	}
	operation := journal.Operation{
		Intent: journal.Intent{
			OperationID: "op-one", RetryID: "retry-one", DatasetID: "dataset", Branch: "main",
			BaseRoot: oldRoot.String(), BaseRevision: 7, Kind: journal.KindWrite,
			Path: "docs/file.txt", PayloadCID: newPayload.String(),
		},
		Sequence: 1, Status: journal.StatusPendingUpload,
	}
	view := filesystemservice.View{DatasetID: "dataset", Branch: "main", Root: oldRoot, Revision: 7}
	batch := staging.UploadBatch{
		View: view, OperationID: "fs-writeback-one", Operations: []journal.Operation{operation},
		Pending: []journal.Operation{operation}, Payloads: []staging.UploadPayload{{CID: newPayload, Body: newBody}},
	}
	return &writebackFixture{
		view: view, update: update, intent: intent, payload: newPayload, runtime: runtime,
		queue: &fakeQueue{batch: batch}, payloads: &fakePayloadStore{expected: newPayload},
		remote: &fakeClientRootRemote{view: update}, roots: &fakeRootPolicy{accepted: oldRoot},
	}
}

func (f *writebackFixture) service(t *testing.T) *Service {
	t.Helper()
	service, err := New(Options{
		Queue: f.queue, Payloads: f.payloads, Remote: f.remote, Writer: f.runtime,
		Planner: PlannerFunc(func(_ context.Context, view mutation.UpdateView, operations []journal.Operation) (mutation.SemanticIntent, error) {
			if !view.BaseRoot.Equals(f.update.BaseRoot) || len(operations) != 1 || operations[0].OperationID != "op-one" {
				return mutation.SemanticIntent{}, errors.New("planner received substituted input")
			}
			return f.intent, nil
		}),
		Roots: f.roots, TrustAlias: "docs", Source: "test write-back",
	})
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
	if batch.OperationID != q.batch.OperationID {
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

func (q *fakeQueue) MarkUploadConflicted(_ context.Context, batch staging.UploadBatch, conflictID string) ([]journal.Operation, error) {
	if batch.OperationID != q.batch.OperationID {
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

func (s *fakePayloadStore) Put(_ context.Context, body []byte) (cid.Cid, error) {
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

type fakeClientRootRemote struct {
	view              mutation.UpdateView
	fetches           int
	submitted         *mutation.ClientRootBundle
	substituteReceipt bool
}

func (r *fakeClientRootRemote) FetchUpdateView(_ context.Context, root cid.Cid, _ *protocol.UpdateViewBounds) (clientrootapp.ViewEnvelope, error) {
	r.fetches++
	if !root.Equals(r.view.BaseRoot) {
		return clientrootapp.ViewEnvelope{}, errors.New("unexpected update-view root")
	}
	return clientrootapp.ViewEnvelope{View: r.view, WireBytes: 1}, nil
}

func (r *fakeClientRootRemote) SubmitClientRoot(_ context.Context, bundle mutation.ClientRootBundle) (clientrootapp.ReceiptEnvelope, error) {
	copyBundle := bundle
	r.submitted = &copyBundle
	digest, err := bundle.Digest()
	if err != nil {
		return clientrootapp.ReceiptEnvelope{}, err
	}
	candidate := bundle.Candidate
	if r.substituteReceipt {
		candidate = bundle.View.BaseRoot
	}
	return clientrootapp.ReceiptEnvelope{Receipt: mutation.MaterializationReceipt{
		Profile: mutation.MaterializationReceiptProfile, OperationID: bundle.OperationID,
		BaseRoot: bundle.View.BaseRoot, Candidate: candidate, BundleDigest: digest,
		DurableBoundary: "test-atomic-v1",
	}}, nil
}

type fakeRootPolicy struct {
	accepted         cid.Cid
	candidate        cid.Cid
	advanceOnObserve cid.Cid
}

func (p *fakeRootPolicy) AcceptedRoot(string) (cid.Cid, error) { return p.accepted, nil }

func (p *fakeRootPolicy) ObserveCandidate(_ string, candidate, base cid.Cid, _ string) error {
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

func writebackRawCID(t *testing.T, body []byte) cid.Cid {
	t.Helper()
	digest, err := mh.Sum(body, mh.SHA2_256, -1)
	if err != nil {
		t.Fatal(err)
	}
	return cid.NewCidV1(cid.Raw, digest)
}

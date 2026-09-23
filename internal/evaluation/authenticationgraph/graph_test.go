package authenticationgraph

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	"github.com/dewebprotocol/malt-client/internal/evaluation/gatewaytransport"
	"github.com/dewebprotocol/malt-core/auth/commitment"
	"github.com/dewebprotocol/malt-core/auth/commitment/kzg"
	"github.com/dewebprotocol/malt-core/auth/coordinate"
	"github.com/dewebprotocol/malt-core/derivation"
	"github.com/dewebprotocol/malt-core/engine"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

type countedScheme struct {
	*kzg.Scheme
	verifies int
}

func (s *countedScheme) BatchVerify(root commitment.Value, indices []uint64, values []commitment.Cell, proof []byte) (bool, error) {
	s.verifies++
	return s.Scheme.BatchVerify(root, indices, values, proof)
}
func (s *countedScheme) VerifyIndex(root commitment.Value, index uint64, value commitment.Cell, proof []byte) (bool, error) {
	s.verifies++
	return s.Scheme.VerifyIndex(root, index, value, proof)
}

type fixtureRemote struct {
	candidates       map[string]protocol.AuthenticationCandidate
	fetches, submits int
	wrong            string
	last             protocol.AuthenticationBatch
}

func (r *fixtureRemote) AuthenticationCandidate(_ context.Context, root cid.Cid) (gatewaytransport.CandidateResponse, error) {
	r.fetches++
	c, ok := r.candidates[root.String()]
	if !ok {
		return gatewaytransport.CandidateResponse{}, errors.New("missing candidate")
	}
	data, _ := json.Marshal(c)
	c, _ = protocol.DecodeAuthenticationCandidate(data)
	return gatewaytransport.CandidateResponse{Candidate: c, WireBytes: uint64(len(data))}, nil
}
func (r *fixtureRemote) SubmitAuthenticationBatch(_ context.Context, b protocol.AuthenticationBatch) (gatewaytransport.BatchResponse, error) {
	r.submits++
	r.last = b
	digest, err := b.Digest()
	if err != nil {
		return gatewaytransport.BatchResponse{}, err
	}
	receipt := protocol.AuthenticationReceipt{Profile: protocol.AuthenticationReceiptProfile, TransactionID: b.TransactionID, Base: b.Base, Root: b.Root, Digest: digest, DurableBoundary: gatewaytransport.AuthenticationDurableBoundary}
	switch r.wrong {
	case "root":
		receipt.Root = b.Base
	case "transaction":
		receipt.TransactionID = "other"
	case "digest":
		receipt.Digest = "different"
	case "boundary":
		receipt.DurableBoundary = "different"
	case "profile":
		receipt.Profile = "old"
	}
	if r.wrong == "" {
		for _, c := range b.Candidates {
			r.candidates[c.Root] = c
		}
	}
	return gatewaytransport.BatchResponse{Receipt: receipt}, nil
}
func graphFixture(t *testing.T) (*Session, *fixtureRemote, *countedScheme, cid.Cid, cid.Cid, cid.Cid) {
	t.Helper()
	ctx := context.Background()
	base, err := kzg.NewScheme()
	if err != nil {
		t.Fatal(err)
	}
	counted := &countedScheme{Scheme: base}
	registry := engine.NewRegistry()
	if err := registry.Register(counted); err != nil {
		t.Fatal(err)
	}
	e := engine.New(registry)
	payload := raw(t, "first chunk")
	child, err := authentication.Prepare(ctx, e, engine.State{Descriptor: maltcid.RootDescriptor{DerivationProfile: uint8(derivation.Direct), Layout: maltcid.Positional, Profile: maltcid.KZG4096}, Entries: []engine.Entry{{Label: coordinate.EncodeIndex(0), Target: payload}}, ChunkSize: 11, TotalSize: 11})
	if err != nil {
		t.Fatal(err)
	}
	childRoot, _ := cid.Parse(child.Root)
	parent, err := authentication.Prepare(ctx, e, engine.State{Descriptor: maltcid.RootDescriptor{Layout: maltcid.Prefix, DerivationProfile: uint8(derivation.SHA256), Profile: maltcid.KZG4096}, Entries: []engine.Entry{{Label: []byte("first"), Target: childRoot}, {Label: []byte("alias"), Target: childRoot}}})
	if err != nil {
		t.Fatal(err)
	}
	root, _ := cid.Parse(parent.Root)
	remote := &fixtureRemote{candidates: map[string]protocol.AuthenticationCandidate{child.Root: child, parent.Root: parent}}
	session, err := New(remote, e)
	if err != nil {
		t.Fatal(err)
	}
	return session, remote, counted, root, childRoot, payload
}
func raw(t *testing.T, s string) cid.Cid {
	t.Helper()
	v, err := clientcas.CIDForBlock(clientcas.Block{Codec: cid.Raw, Data: []byte(s)})
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func TestRetainedWritersCopyOnWriteAndExactReceipt(t *testing.T) {
	s, r, scheme, root, child, payload := graphFixture(t)
	ctx := context.Background()
	load, err := s.Load(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if load.Objects != 2 || r.fetches != 2 || scheme.verifies == 0 {
		t.Fatalf("initial import not observed: %+v fetches=%d verifies=%d", load, r.fetches, scheme.verifies)
	}
	scheme.verifies = 0
	stale, _ := s.Begin()
	edit, _ := s.Begin()
	after := raw(t, "second data")
	nextChild, err := edit.Apply(ctx, child, authentication.Delta{Changes: []engine.Change{{Label: coordinate.EncodeIndex(0), Before: payload, After: after}}})
	if err != nil {
		t.Fatal(err)
	}
	next, err := edit.Apply(ctx, root, authentication.Delta{Changes: []engine.Change{{Label: []byte("first"), Before: child, After: nextChild}}})
	if err != nil {
		t.Fatal(err)
	}
	preview, err := edit.Preview(ctx, next)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.States) != 3 {
		t.Fatal("aliased child was not preserved")
	}
	state, _ := preview.State(next)
	for _, entry := range state.Entries {
		if string(entry.Label) == "alias" && !entry.Target.Equals(child) {
			t.Fatal("alias was rebound")
		}
	}
	// A snapshot is caller-owned; changing its input or target must not poison a writer.
	old, _ := s.Snapshot()
	oldState := old.States[root.String()]
	oldState.Entries[0].Label[0] ^= 1
	oldState.Entries[0].Target = after
	for _, bad := range []string{"root", "transaction", "digest", "boundary", "profile"} {
		r.wrong = bad
		if _, err := s.Submit(ctx, "edit-one", edit, next); err == nil {
			t.Fatalf("accepted %s substitution", bad)
		}
		unchanged, _ := s.Snapshot()
		if !unchanged.Root.Equals(root) {
			t.Fatal("failed receipt advanced session")
		}
	}
	r.wrong = ""
	result, err := s.Submit(ctx, "edit-one", edit, next)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Root.Equals(next) || r.last.Candidates[0].Root != nextChild.String() || r.last.Candidates[1].Root != next.String() {
		t.Fatal("dependency order or receipt Root differs")
	}
	if r.fetches != 2 || scheme.verifies != 0 {
		t.Fatalf("incremental writer refetched or reverified state: fetches=%d verifies=%d", r.fetches, scheme.verifies)
	}
	if _, err := s.Submit(ctx, "stale-edit", stale, root); err == nil {
		t.Fatal("stale edit accepted")
	}
	if err := s.Audit(ctx); err != nil {
		t.Fatal(err)
	}
	if r.fetches != 5 || scheme.verifies == 0 {
		t.Fatal("durable audit did not independently import full closure")
	}
}
func TestFailedImportAndMissingClosureLeavePriorSessionUsable(t *testing.T) {
	s, r, _, root, child, _ := graphFixture(t)
	ctx := context.Background()
	if _, err := s.Load(ctx, root); err != nil {
		t.Fatal(err)
	}
	c := r.candidates[child.String()]
	delete(r.candidates, child.String())
	if _, err := s.Load(ctx, root); err == nil {
		t.Fatal("missing child accepted")
	}
	state, err := s.Snapshot()
	if err != nil || !state.Root.Equals(root) {
		t.Fatal("failed load discarded prior state")
	}
	r.candidates[child.String()] = c
	bad := r.candidates[root.String()]
	bad.State.Entries = append([]engine.Entry{}, bad.State.Entries...)
	bad.State.Entries[0].Target = raw(t, "wrong")
	r.candidates[root.String()] = bad
	if _, err := s.Load(ctx, root); err == nil {
		t.Fatal("unbound inputs accepted")
	}
	edit, _ := s.Begin()
	unknown := raw(t, "not a Root")
	if _, err := edit.Apply(ctx, unknown, authentication.Delta{}); err == nil {
		t.Fatal("unknown edit base accepted")
	}
}

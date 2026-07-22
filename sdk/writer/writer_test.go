package writer

import (
	"context"
	"errors"
	"testing"

	"github.com/dewebprotocol/malt/auth/arcset"
	materializermemory "github.com/dewebprotocol/malt/auth/arcset/materializer/memory"
	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/auth/commitment/kzg"
	"github.com/dewebprotocol/malt/auth/semantic/mapping"
	mappingradix "github.com/dewebprotocol/malt/auth/semantic/mapping/radix"
	"github.com/dewebprotocol/malt/mutation"
	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestComputeBundleMatchesIndependentFullRebuildAndRetainsNextView(t *testing.T) {
	ctx := context.Background()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatal(err)
	}
	view, intent, expectedCandidate, newPayload := mapWriterFixture(t, ctx, scheme)
	runtime, err := NewRuntime(materializermemory.New(true), map[maltcid.BackendKind]commitment.IndexCommitment{
		maltcid.BackendKindKZG: scheme,
	})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := runtime.VerifyUpdateView(ctx, view)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.ComputeBundle(ctx, "map-replace-1", verified, intent)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Bundle.Candidate.Equals(expectedCandidate) {
		t.Fatalf("candidate = %s, want independent full rebuild %s", result.Bundle.Candidate, expectedCandidate)
	}
	if len(result.Bundle.Outputs) != 2 || result.Bundle.Outputs[0].TransitionID != "child-output" || result.Bundle.Outputs[1].TransitionID != "top-output" {
		t.Fatalf("outputs = %#v", result.Bundle.Outputs)
	}
	if len(result.Bundle.PayloadCIDs) != 1 || !result.Bundle.PayloadCIDs[0].Equals(newPayload) {
		t.Fatalf("payload CIDs = %#v", result.Bundle.PayloadCIDs)
	}
	if !result.NextView.BaseRoot.Equals(result.Bundle.Candidate) {
		t.Fatalf("next view base = %s, candidate = %s", result.NextView.BaseRoot, result.Bundle.Candidate)
	}

	// A fresh runtime must independently accept the retained complete vectors;
	// this prevents a session-only materialization cache from hiding bad roots.
	fresh, err := NewRuntime(materializermemory.New(true), map[maltcid.BackendKind]commitment.IndexCommitment{
		maltcid.BackendKindKZG: scheme,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.VerifyUpdateView(ctx, result.NextView); err != nil {
		t.Fatalf("verify retained next view: %v", err)
	}
}

func TestVerifyUpdateViewRejectsUntrustedVectorForDeclaredRoot(t *testing.T) {
	ctx := context.Background()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatal(err)
	}
	view, _, _, _ := mapWriterFixture(t, ctx, scheme)
	wrongPayload := arcset.NewCASTarget(writerRawCID(t, "tampered"))
	tamperedEntries, err := arcset.NewCanonicalArcSet(arcset.KindMap, []arcset.ArcEntry{{
		Coordinate: writerMapCoordinate(t, "payload"), Target: wrongPayload,
	}})
	if err != nil {
		t.Fatal(err)
	}
	for index := range view.Objects {
		if view.Objects[index].ObjectID == "child" {
			view.Objects[index].Entries = tamperedEntries
		}
	}
	runtime, err := NewRuntime(materializermemory.New(true), map[maltcid.BackendKind]commitment.IndexCommitment{
		maltcid.BackendKindKZG: scheme,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.VerifyUpdateView(ctx, view); err == nil {
		t.Fatal("tampered complete vector was accepted")
	}
}

func TestComputeBundleRejectsDependencyTamperingBeforeComputation(t *testing.T) {
	ctx := context.Background()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatal(err)
	}
	view, intent, _, _ := mapWriterFixture(t, ctx, scheme)
	for index := range intent.Transitions {
		if intent.Transitions[index].ID == "child-output" {
			intent.Transitions[index].ExpectedUses = 2
		}
	}
	runtime, err := NewRuntime(materializermemory.New(true), map[maltcid.BackendKind]commitment.IndexCommitment{
		maltcid.BackendKindKZG: scheme,
	})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := runtime.VerifyUpdateView(ctx, view)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.ComputeBundle(ctx, "tampered-dependency", verified, intent); !errors.Is(err, mutation.ErrIntentMultiplicity) {
		t.Fatalf("error = %v, want ErrIntentMultiplicity", err)
	}
}

func TestSessionAdvancesOnlyAfterExactDurableReceipt(t *testing.T) {
	ctx := context.Background()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatal(err)
	}
	view, intent, _, _ := mapWriterFixture(t, ctx, scheme)
	runtime, err := NewRuntime(materializermemory.New(true), map[maltcid.BackendKind]commitment.IndexCommitment{
		maltcid.BackendKindKZG: scheme,
	})
	if err != nil {
		t.Fatal(err)
	}
	session, err := NewSession(runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Load(ctx, view); err != nil {
		t.Fatal(err)
	}
	prepared, err := session.Prepare(ctx, "session-map-replace", intent)
	if err != nil {
		t.Fatal(err)
	}
	if !session.BaseRoot().Equals(view.BaseRoot) {
		t.Fatal("preparing a candidate advanced the accepted session base")
	}
	digest, err := prepared.Bundle.Digest()
	if err != nil {
		t.Fatal(err)
	}
	wrong := mutation.MaterializationReceipt{
		Profile: mutation.MaterializationReceiptProfile, OperationID: prepared.Bundle.OperationID,
		BaseRoot: prepared.Bundle.View.BaseRoot, Candidate: view.BaseRoot,
		BundleDigest: digest, DurableBoundary: "embedded-transaction-commit-v1",
	}
	if err := session.AcceptReceipt(wrong, prepared); err == nil {
		t.Fatal("session accepted a substituted receipt candidate")
	}
	if !session.BaseRoot().Equals(view.BaseRoot) {
		t.Fatal("rejected receipt advanced the accepted session base")
	}
	correct := wrong
	correct.Candidate = prepared.Bundle.Candidate
	if err := session.AcceptReceipt(correct, prepared); err != nil {
		t.Fatal(err)
	}
	if !session.BaseRoot().Equals(prepared.Bundle.Candidate) {
		t.Fatalf("session base = %s, want %s", session.BaseRoot(), prepared.Bundle.Candidate)
	}
	if err := session.Audit(ctx); err != nil {
		t.Fatalf("final session audit: %v", err)
	}
}

func mapWriterFixture(t *testing.T, ctx context.Context, scheme *kzg.Scheme) (mutation.UpdateView, mutation.SemanticIntent, cid.Cid, cid.Cid) {
	t.Helper()
	oldPayload := writerRawCID(t, "old-payload")
	newPayload := writerRawCID(t, "new-payload")
	oldStore := materializermemory.New(true)
	oldMap, err := mappingradix.NewMap(scheme, oldStore)
	if err != nil {
		t.Fatal(err)
	}
	childRoot, err := oldMap.Commit(ctx, "child", mapping.NewViewFrom(map[string]cid.Cid{"payload": oldPayload}))
	if err != nil {
		t.Fatal(err)
	}
	parentRoot, err := oldMap.Commit(ctx, "parent", mapping.NewViewFrom(map[string]cid.Cid{"child": childRoot}))
	if err != nil {
		t.Fatal(err)
	}

	newStore := materializermemory.New(true)
	newMap, err := mappingradix.NewMap(scheme, newStore)
	if err != nil {
		t.Fatal(err)
	}
	newChildRoot, err := newMap.Commit(ctx, "child", mapping.NewViewFrom(map[string]cid.Cid{"payload": newPayload}))
	if err != nil {
		t.Fatal(err)
	}
	expectedCandidate, err := newMap.Commit(ctx, "parent", mapping.NewViewFrom(map[string]cid.Cid{"child": newChildRoot}))
	if err != nil {
		t.Fatal(err)
	}

	childEntries, err := arcset.NewCanonicalArcSet(arcset.KindMap, []arcset.ArcEntry{{
		Coordinate: writerMapCoordinate(t, "payload"), Target: arcset.NewCASTarget(oldPayload),
	}})
	if err != nil {
		t.Fatal(err)
	}
	parentEntries, err := arcset.NewCanonicalArcSet(arcset.KindMap, []arcset.ArcEntry{{
		Coordinate: writerMapCoordinate(t, "child"), Target: arcset.NewMapTarget(childRoot),
	}})
	if err != nil {
		t.Fatal(err)
	}
	view := mutation.UpdateView{
		Profile: mutation.UpdateViewProfile, StateProfile: mutation.StatefulCompleteVectorsProfile,
		BaseRoot: parentRoot, Bounds: mutation.UpdateViewBounds{MaxObjects: 8, MaxTotalEntries: 64, MaxDepth: 8},
		Objects: []mutation.UpdateObject{
			{ObjectID: "parent", Root: parentRoot, Kind: arcset.KindMap, Entries: parentEntries},
			{ObjectID: "child", Root: childRoot, Kind: arcset.KindMap, Entries: childEntries},
		},
	}
	oldPayloadTarget := arcset.NewCASTarget(oldPayload)
	newPayloadTarget := arcset.NewCASTarget(newPayload)
	oldChildTarget := arcset.NewMapTarget(childRoot)
	intent := mutation.SemanticIntent{
		Profile: mutation.SemanticIntentProfile, BaseRoot: parentRoot, TopOutputID: "top-output",
		Transitions: []mutation.IntentTransition{
			{
				ID: "top-output", ObjectID: "parent", OldRoot: parentRoot, Kind: arcset.KindMap, Backend: maltcid.BackendKindKZG,
				Changes: []mutation.IntentChange{{
					Coordinate: writerMapCoordinate(t, "child"), Before: &oldChildTarget,
					OutputID: "child-output", OutputKind: arcset.TargetKindMap,
				}},
			},
			{
				ID: "child-output", ObjectID: "child", OldRoot: childRoot, Kind: arcset.KindMap, Backend: maltcid.BackendKindKZG,
				ExpectedUses: 1,
				Changes: []mutation.IntentChange{{
					Coordinate: writerMapCoordinate(t, "payload"), Before: &oldPayloadTarget, After: &newPayloadTarget,
				}},
			},
		},
	}
	return view, intent, expectedCandidate, newPayload
}

func writerMapCoordinate(t *testing.T, value string) arcset.CanonicalCoordinate {
	t.Helper()
	coordinate, err := arcset.NewMapCoordinate(value)
	if err != nil {
		t.Fatal(err)
	}
	return coordinate
}

func writerRawCID(t *testing.T, value string) cid.Cid {
	t.Helper()
	hash, err := mh.Sum([]byte(value), mh.SHA2_256, -1)
	if err != nil {
		t.Fatal(err)
	}
	return cid.NewCidV1(cid.Raw, hash)
}

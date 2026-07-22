package mutation

import (
	"errors"
	"testing"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestClientRootBundleCanonicalizesAndBindsExactTopOutput(t *testing.T) {
	view, intent, childOutput, topOutput, payload := clientRootFixture(t)
	viewDigest, err := view.Digest()
	if err != nil {
		t.Fatal(err)
	}
	normalizedIntent, err := NormalizeSemanticIntent(view, intent)
	if err != nil {
		t.Fatal(err)
	}
	intentDigest, err := normalizedIntent.Digest()
	if err != nil {
		t.Fatal(err)
	}

	bundle, err := NewClientRootBundle(ClientRootBundle{
		Profile:      ClientRootBundleProfile,
		OperationID:  "operation-1",
		View:         view,
		Intent:       intent,
		Outputs:      []TransitionOutput{topOutput, childOutput},
		Candidate:    topOutput.Root,
		PayloadCIDs:  []cid.Cid{payload},
		ViewDigest:   viewDigest,
		IntentDigest: intentDigest,
	})
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Intent.Transitions[0].ID != "child-output" || bundle.Intent.Transitions[1].ID != "top-output" {
		t.Fatalf("transition order = %q, %q", bundle.Intent.Transitions[0].ID, bundle.Intent.Transitions[1].ID)
	}
	if bundle.Outputs[0].TransitionID != "child-output" || bundle.Outputs[1].TransitionID != "top-output" {
		t.Fatalf("output order = %#v", bundle.Outputs)
	}
	digest, err := bundle.Digest()
	if err != nil {
		t.Fatal(err)
	}
	receipt := MaterializationReceipt{
		Profile:         MaterializationReceiptProfile,
		OperationID:     bundle.OperationID,
		BaseRoot:        bundle.View.BaseRoot,
		Candidate:       bundle.Candidate,
		BundleDigest:    digest,
		DurableBoundary: "embedded-transaction-commit-v1",
	}
	if err := receipt.Validate(bundle); err != nil {
		t.Fatalf("receipt validation: %v", err)
	}
}

func TestNormalizeUpdateViewRequiresExactReachabilityClosure(t *testing.T) {
	view, _, _, _, _ := clientRootFixture(t)

	missing := view
	missing.Objects = append([]UpdateObject(nil), view.Objects[:1]...)
	if _, err := NormalizeUpdateView(missing); !errors.Is(err, ErrIncompleteUpdateView) {
		t.Fatalf("missing child error = %v, want ErrIncompleteUpdateView", err)
	}

	extraRoot := typedRoot(t, arcset.KindMap, 90)
	extraEntries, err := arcset.NewCanonicalArcSet(arcset.KindMap, []arcset.ArcEntry{{
		Coordinate: mapCoordinate(t, "payload"),
		Target:     arcset.NewCASTarget(rawCID(t, "extra-payload")),
	}})
	if err != nil {
		t.Fatal(err)
	}
	extra := view
	extra.Objects = append(append([]UpdateObject(nil), view.Objects...), UpdateObject{
		ObjectID: "extra", Root: extraRoot, Kind: arcset.KindMap, Entries: extraEntries,
	})
	if _, err := NormalizeUpdateView(extra); !errors.Is(err, ErrIncompleteUpdateView) {
		t.Fatalf("extra object error = %v, want ErrIncompleteUpdateView", err)
	}
}

func TestNormalizeSemanticIntentRejectsMultiplicityBeforeImageAndLastDeltaPatterns(t *testing.T) {
	view, intent, _, _, _ := clientRootFixture(t)

	wrongMultiplicity := intent
	wrongMultiplicity.Transitions = cloneIntentTransitions(intent.Transitions)
	wrongMultiplicity.Transitions[1].ExpectedUses = 2
	if _, err := NormalizeSemanticIntent(view, wrongMultiplicity); !errors.Is(err, ErrIntentMultiplicity) {
		t.Fatalf("multiplicity error = %v, want ErrIntentMultiplicity", err)
	}

	wrongBefore := intent
	wrongBefore.Transitions = cloneIntentTransitions(intent.Transitions)
	wrong := arcset.NewCASTarget(rawCID(t, "wrong-before"))
	wrongBefore.Transitions[1].Changes[0].Before = &wrong
	if _, err := NormalizeSemanticIntent(view, wrongBefore); !errors.Is(err, ErrInvalidSemanticIntent) {
		t.Fatalf("before-image error = %v, want ErrInvalidSemanticIntent", err)
	}

	duplicateObject := intent
	duplicateObject.Transitions = append(cloneIntentTransitions(intent.Transitions), intent.Transitions[0])
	duplicateObject.Transitions[2].ID = "second-child-delta"
	if _, err := NormalizeSemanticIntent(view, duplicateObject); !errors.Is(err, ErrInvalidSemanticIntent) {
		t.Fatalf("duplicate object error = %v, want ErrInvalidSemanticIntent", err)
	}
}

func TestClientRootBundleRejectsDigestAndCandidateSubstitution(t *testing.T) {
	view, intent, childOutput, topOutput, payload := clientRootFixture(t)
	viewDigest, err := view.Digest()
	if err != nil {
		t.Fatal(err)
	}
	normalizedIntent, err := NormalizeSemanticIntent(view, intent)
	if err != nil {
		t.Fatal(err)
	}
	intentDigest, err := normalizedIntent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	base := ClientRootBundle{
		Profile: ClientRootBundleProfile, OperationID: "operation-2", View: view, Intent: intent,
		Outputs: []TransitionOutput{childOutput, topOutput}, Candidate: topOutput.Root,
		PayloadCIDs: []cid.Cid{payload}, ViewDigest: viewDigest, IntentDigest: intentDigest,
	}

	tamperedDigest := base
	tamperedDigest.IntentDigest[0] ^= 0xff
	if _, err := NewClientRootBundle(tamperedDigest); !errors.Is(err, ErrBundleDigestMismatch) {
		t.Fatalf("digest error = %v, want ErrBundleDigestMismatch", err)
	}

	substituted := base
	substituted.Candidate = typedRoot(t, arcset.KindMap, 99)
	if _, err := NewClientRootBundle(substituted); !errors.Is(err, ErrBundleCandidateMismatch) {
		t.Fatalf("candidate error = %v, want ErrBundleCandidateMismatch", err)
	}
}

func TestClientRootBundleRejectsOutputKindBackendAndPayloadSetSubstitution(t *testing.T) {
	view, intent, childOutput, topOutput, payload := clientRootFixture(t)
	viewDigest, err := view.Digest()
	if err != nil {
		t.Fatal(err)
	}
	normalizedIntent, err := NormalizeSemanticIntent(view, intent)
	if err != nil {
		t.Fatal(err)
	}
	intentDigest, err := normalizedIntent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	base := ClientRootBundle{
		Profile: ClientRootBundleProfile, OperationID: "operation-hostile", View: view, Intent: intent,
		Outputs: []TransitionOutput{childOutput, topOutput}, Candidate: topOutput.Root,
		PayloadCIDs: []cid.Cid{payload}, ViewDigest: viewDigest, IntentDigest: intentDigest,
	}

	wrongKind := base
	wrongKind.Outputs = append([]TransitionOutput(nil), base.Outputs...)
	wrongKind.Outputs[0].Root = typedRoot(t, arcset.KindList, 31)
	if _, err := NewClientRootBundle(wrongKind); !errors.Is(err, ErrInvalidClientRootBundle) {
		t.Fatalf("wrong output kind error = %v, want ErrInvalidClientRootBundle", err)
	}

	wrongBackend := base
	wrongBackend.Outputs = append([]TransitionOutput(nil), base.Outputs...)
	wrongBackend.Outputs[0].Root = typedIPARoot(t, arcset.KindMap, 41)
	if _, err := NewClientRootBundle(wrongBackend); !errors.Is(err, ErrInvalidClientRootBundle) {
		t.Fatalf("wrong output backend error = %v, want ErrInvalidClientRootBundle", err)
	}

	missingPayload := base
	missingPayload.PayloadCIDs = nil
	if _, err := NewClientRootBundle(missingPayload); !errors.Is(err, ErrInvalidClientRootBundle) {
		t.Fatalf("missing payload error = %v, want ErrInvalidClientRootBundle", err)
	}

	extraPayload := base
	extraPayload.PayloadCIDs = []cid.Cid{payload, rawCID(t, "unreferenced-payload")}
	if _, err := NewClientRootBundle(extraPayload); !errors.Is(err, ErrInvalidClientRootBundle) {
		t.Fatalf("extra payload error = %v, want ErrInvalidClientRootBundle", err)
	}
}

func clientRootFixture(t *testing.T) (UpdateView, SemanticIntent, TransitionOutput, TransitionOutput, cid.Cid) {
	t.Helper()
	oldPayload := rawCID(t, "old-payload")
	newPayload := rawCID(t, "new-payload")
	childRoot := typedRoot(t, arcset.KindMap, 1)
	parentRoot := typedRoot(t, arcset.KindMap, 2)
	childNewRoot := typedRoot(t, arcset.KindMap, 3)
	parentNewRoot := typedRoot(t, arcset.KindMap, 4)

	childEntries, err := arcset.NewCanonicalArcSet(arcset.KindMap, []arcset.ArcEntry{{
		Coordinate: mapCoordinate(t, "payload"),
		Target:     arcset.NewCASTarget(oldPayload),
	}})
	if err != nil {
		t.Fatal(err)
	}
	parentEntries, err := arcset.NewCanonicalArcSet(arcset.KindMap, []arcset.ArcEntry{{
		Coordinate: mapCoordinate(t, "child"),
		Target:     arcset.NewMapTarget(childRoot),
	}})
	if err != nil {
		t.Fatal(err)
	}
	view := UpdateView{
		Profile: UpdateViewProfile, StateProfile: StatefulCompleteVectorsProfile,
		BaseRoot: parentRoot,
		Bounds:   UpdateViewBounds{MaxObjects: 8, MaxTotalEntries: 64, MaxDepth: 8},
		// Deliberately reversed: normalization owns canonical object order.
		Objects: []UpdateObject{
			{ObjectID: "parent", Root: parentRoot, Kind: arcset.KindMap, Entries: parentEntries},
			{ObjectID: "child", Root: childRoot, Kind: arcset.KindMap, Entries: childEntries},
		},
	}
	oldPayloadTarget := arcset.NewCASTarget(oldPayload)
	newPayloadTarget := arcset.NewCASTarget(newPayload)
	childTarget := arcset.NewMapTarget(childRoot)
	intent := SemanticIntent{
		Profile: SemanticIntentProfile, BaseRoot: parentRoot, TopOutputID: "top-output",
		// Deliberately parent-first: normalization must schedule bottom-up.
		Transitions: []IntentTransition{
			{
				ID: "top-output", ObjectID: "parent", OldRoot: parentRoot, Kind: arcset.KindMap, Backend: maltcid.BackendKindKZG,
				ExpectedUses: 0,
				Changes: []IntentChange{{
					Coordinate: mapCoordinate(t, "child"), Before: &childTarget,
					OutputID: "child-output", OutputKind: arcset.TargetKindMap,
				}},
			},
			{
				ID: "child-output", ObjectID: "child", OldRoot: childRoot, Kind: arcset.KindMap, Backend: maltcid.BackendKindKZG,
				ExpectedUses: 1,
				Changes: []IntentChange{{
					Coordinate: mapCoordinate(t, "payload"), Before: &oldPayloadTarget, After: &newPayloadTarget,
				}},
			},
		},
	}
	return view, intent,
		TransitionOutput{TransitionID: "child-output", Root: childNewRoot},
		TransitionOutput{TransitionID: "top-output", Root: parentNewRoot},
		newPayload
}

func cloneIntentTransitions(values []IntentTransition) []IntentTransition {
	out := make([]IntentTransition, len(values))
	copy(out, values)
	for index := range out {
		out[index].Changes = append([]IntentChange(nil), values[index].Changes...)
	}
	return out
}

func mapCoordinate(t *testing.T, value string) arcset.CanonicalCoordinate {
	t.Helper()
	coordinate, err := arcset.NewMapCoordinate(value)
	if err != nil {
		t.Fatal(err)
	}
	return coordinate
}

func typedRoot(t *testing.T, kind arcset.Kind, seed byte) cid.Cid {
	t.Helper()
	commitment := make([]byte, maltcid.KZGCommitmentSize)
	for index := range commitment {
		commitment[index] = seed + byte(index)
	}
	var (
		root cid.Cid
		err  error
	)
	if kind == arcset.KindMap {
		root, err = maltcid.NewMapKZGCid(commitment)
	} else {
		root, err = maltcid.NewListKZGCid(commitment)
	}
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func typedIPARoot(t *testing.T, kind arcset.Kind, seed byte) cid.Cid {
	t.Helper()
	commitment := make([]byte, maltcid.IPACommitmentSize)
	for index := range commitment {
		commitment[index] = seed + byte(index)
	}
	var (
		root cid.Cid
		err  error
	)
	if kind == arcset.KindMap {
		root, err = maltcid.NewMapIPACid(commitment)
	} else {
		root, err = maltcid.NewListIPACid(commitment)
	}
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func rawCID(t *testing.T, value string) cid.Cid {
	t.Helper()
	hash, err := mh.Sum([]byte(value), mh.SHA2_256, -1)
	if err != nil {
		t.Fatal(err)
	}
	return cid.NewCidV1(cid.Raw, hash)
}

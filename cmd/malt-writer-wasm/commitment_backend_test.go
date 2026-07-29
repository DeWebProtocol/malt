package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/dewebprotocol/malt/auth/arcset"
	materializermemory "github.com/dewebprotocol/malt/auth/arcset/materializer/memory"
	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/auth/commitment/ipa"
	"github.com/dewebprotocol/malt/auth/commitment/kzg"
	"github.com/dewebprotocol/malt/auth/semantic/list"
	listtree "github.com/dewebprotocol/malt/auth/semantic/list/tree"
	"github.com/dewebprotocol/malt/auth/semantic/mapping"
	mappingradix "github.com/dewebprotocol/malt/auth/semantic/mapping/radix"
	"github.com/dewebprotocol/malt/mutation"
	"github.com/dewebprotocol/malt/protocol"
	clientwriter "github.com/dewebprotocol/malt/sdk/writer"
	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

const commitmentTestSession = "test-session"

func TestCommitmentComputerMatchesWriterCandidate(t *testing.T) {
	view, intent := computeFixture(t, maltcid.BackendKindKZG)
	wireView, err := protocol.NewUpdateView(view)
	if err != nil {
		t.Fatalf("NewUpdateView failed: %v", err)
	}
	wireIntent, err := protocol.NewSemanticIntent(view, intent)
	if err != nil {
		t.Fatalf("NewSemanticIntent failed: %v", err)
	}
	expected := referenceCompute(t, maltcid.BackendKindKZG, "backend-operation", view, intent)

	computer, err := newCompiledCommitmentComputer(maltcid.BackendKindKZG)
	if err != nil {
		t.Fatalf("newCompiledCommitmentComputer failed: %v", err)
	}
	loadJSON, err := json.Marshal(objectsRequest(wireView))
	if err != nil {
		t.Fatalf("marshal objects: %v", err)
	}
	rawLoad, err := computer.loadObjects(t.Context(), loadJSON)
	if err != nil {
		t.Fatalf("loadObjects failed: %v", err)
	}
	var loadResult commitmentLoadResult
	if err := json.Unmarshal(rawLoad, &loadResult); err != nil {
		t.Fatalf("decode load result: %v", err)
	}
	if loadResult.Profile != commitmentResultProfile || loadResult.Backend != "kzg" || loadResult.VerifiedObjects != 1 {
		t.Fatalf("unexpected load result: %+v", loadResult)
	}

	deltaJSON, err := json.Marshal(deltaRequest(view.BaseRoot.String(), wireIntent.Transitions[0]))
	if err != nil {
		t.Fatalf("marshal delta: %v", err)
	}
	rawResult, err := computer.applyDelta(t.Context(), deltaJSON)
	if err != nil {
		t.Fatalf("applyDelta failed: %v", err)
	}
	var result commitmentApplyResult
	if err := json.Unmarshal(rawResult, &result); err != nil {
		t.Fatalf("decode apply result: %v", err)
	}
	if result.Root != expected.Bundle.Candidate || result.Backend != "kzg" || result.CommitmentNS == 0 {
		t.Fatalf("commitment result = %+v, want candidate %s", result, expected.Bundle.Candidate)
	}
}

func TestCommitmentComputerIsolatesAndReclaimsSessions(t *testing.T) {
	firstView, firstIntent := computeFixture(t, maltcid.BackendKindIPA)
	secondView, _ := computeNestedFixture(t, maltcid.BackendKindIPA)
	firstWireView, _ := protocol.NewUpdateView(firstView)
	firstWireIntent, _ := protocol.NewSemanticIntent(firstView, firstIntent)
	secondWireView, _ := protocol.NewUpdateView(secondView)

	computer, err := newCompiledCommitmentComputer(maltcid.BackendKindIPA)
	if err != nil {
		t.Fatal(err)
	}
	firstLoad, _ := json.Marshal(objectsRequestForSession("first-session", firstWireView))
	secondLoad, _ := json.Marshal(objectsRequestForSession("second-session", secondWireView))
	if _, err := computer.loadObjects(t.Context(), firstLoad); err != nil {
		t.Fatal(err)
	}
	firstBaseline := computer.engines["first-session"].store.EntryCount()
	if _, err := computer.loadObjects(t.Context(), secondLoad); err != nil {
		t.Fatal(err)
	}
	firstDelta := deltaRequestForSession("first-session", firstView.BaseRoot.String(), firstWireIntent.Transitions[0])
	firstDeltaJSON, _ := json.Marshal(firstDelta)
	if _, err := computer.applyDelta(t.Context(), firstDeltaJSON); err != nil {
		t.Fatalf("second session load corrupted first session: %v", err)
	}
	if got := computer.engines["first-session"].store.EntryCount(); got <= firstBaseline {
		t.Fatalf("delta retained %d entries, want more than baseline %d", got, firstBaseline)
	}
	retainJSON, _ := json.Marshal(commitmentRetain{
		Profile: commitmentRetainProfile, SessionID: "first-session",
		Objects: []commitmentRetainRoot{{
			ObjectID: firstWireView.Objects[0].ObjectID,
			Root:     firstWireView.Objects[0].Root,
		}},
	})
	if _, err := computer.retainRoots(retainJSON); err != nil {
		t.Fatal(err)
	}
	if got := computer.engines["first-session"].store.EntryCount(); got != firstBaseline {
		t.Fatalf("retain roots left %d entries, want baseline %d", got, firstBaseline)
	}
	if err := computer.dropSession("first-session"); err != nil {
		t.Fatal(err)
	}
	if _, exists := computer.engines["first-session"]; exists {
		t.Fatal("dropped commitment session remains loaded")
	}
	if err := computer.dropSession("first-session"); err != nil {
		t.Fatalf("dropSession is not idempotent: %v", err)
	}
	if computer.engines["second-session"] == nil {
		t.Fatal("dropping first session removed second session")
	}
}

func TestCommitmentComputerRejectsWrongBackendRootAndBeforeImage(t *testing.T) {
	view, intent := computeFixture(t, maltcid.BackendKindKZG)
	wireView, _ := protocol.NewUpdateView(view)
	wireIntent, _ := protocol.NewSemanticIntent(view, intent)

	ipaComputer, err := newCompiledCommitmentComputer(maltcid.BackendKindIPA)
	if err != nil {
		t.Fatalf("newCompiledCommitmentComputer(ipa): %v", err)
	}
	loadJSON, _ := json.Marshal(objectsRequest(wireView))
	if _, err := ipaComputer.loadObjects(t.Context(), loadJSON); err == nil {
		t.Fatal("IPA commitment backend accepted KZG roots")
	}

	kzgComputer, err := newCompiledCommitmentComputer(maltcid.BackendKindKZG)
	if err != nil {
		t.Fatalf("newCompiledCommitmentComputer(kzg): %v", err)
	}
	if _, err := kzgComputer.loadObjects(t.Context(), loadJSON); err != nil {
		t.Fatalf("loadObjects(kzg): %v", err)
	}
	request := deltaRequest(view.BaseRoot.String(), wireIntent.Transitions[0])
	request.Changes[0].Before.CID = request.Changes[0].After.CID
	deltaJSON, _ := json.Marshal(request)
	if _, err := kzgComputer.applyDelta(t.Context(), deltaJSON); err == nil {
		t.Fatal("commitment backend accepted a mismatched before-image")
	}
	if _, err := kzgComputer.applyDelta(t.Context(), []byte(`{}`)); err == nil {
		t.Fatal("commitment backend accepted an invalid delta")
	}
}

type wasmComputeFixture struct {
	Case             string                          `json:"case"`
	Backend          maltcid.BackendKind             `json:"backend"`
	OperationID      string                          `json:"operation_id"`
	UpdateView       protocol.UpdateView             `json:"update_view"`
	SemanticIntent   protocol.SemanticIntent         `json:"semantic_intent"`
	ExpectedBundle   protocol.ClientRootBundle       `json:"expected_bundle"`
	ExpectedNextView protocol.UpdateView             `json:"expected_next_view"`
	ExpectedReceipt  protocol.MaterializationReceipt `json:"expected_receipt"`
}

func TestGenerateWASMFixtures(t *testing.T) {
	outputPath := os.Getenv("MALT_WRITER_WASM_FIXTURE_OUT")
	if outputPath == "" {
		t.Skip("MALT_WRITER_WASM_FIXTURE_OUT is not set")
	}

	fixtures := make([]wasmComputeFixture, 0, 8)
	for _, backend := range []maltcid.BackendKind{maltcid.BackendKindKZG, maltcid.BackendKindIPA} {
		for _, testCase := range []struct {
			name  string
			build func(*testing.T, maltcid.BackendKind) (mutation.UpdateView, mutation.SemanticIntent)
		}{
			{name: "map-replace", build: computeFixture},
			{name: "nested-map", build: computeNestedFixture},
			{name: "list-replace-append", build: computeListFixture},
			{name: "fixed-list-u64", build: computeFixedListUint64Fixture},
		} {
			view, intent := testCase.build(t, backend)
			wireView, err := protocol.NewUpdateView(view)
			if err != nil {
				t.Fatalf("NewUpdateView(%s/%s): %v", backend, testCase.name, err)
			}
			wireIntent, err := protocol.NewSemanticIntent(view, intent)
			if err != nil {
				t.Fatalf("NewSemanticIntent(%s/%s): %v", backend, testCase.name, err)
			}
			operationID := "wasm-" + string(backend) + "-" + testCase.name
			response := referenceCompute(t, backend, operationID, view, intent)
			bundle, err := response.Bundle.Core()
			if err != nil {
				t.Fatalf("bundle Core(%s/%s): %v", backend, testCase.name, err)
			}
			bundleDigest, err := bundle.Digest()
			if err != nil {
				t.Fatalf("bundle Digest(%s/%s): %v", backend, testCase.name, err)
			}
			receipt, err := protocol.NewMaterializationReceipt(mutation.MaterializationReceipt{
				Profile: mutation.MaterializationReceiptProfile, OperationID: operationID,
				BaseRoot: bundle.View.BaseRoot, Candidate: bundle.Candidate,
				BundleDigest: bundleDigest, DurableBoundary: "wasm-smoke-memory-v1",
			}, bundle)
			if err != nil {
				t.Fatalf("NewMaterializationReceipt(%s/%s): %v", backend, testCase.name, err)
			}
			fixtures = append(fixtures, wasmComputeFixture{
				Case: testCase.name, Backend: backend, OperationID: operationID,
				UpdateView: wireView, SemanticIntent: wireIntent,
				ExpectedBundle: response.Bundle, ExpectedNextView: response.NextView,
				ExpectedReceipt: receipt,
			})
		}
	}
	data, err := json.Marshal(fixtures)
	if err != nil {
		t.Fatalf("marshal WASM fixtures: %v", err)
	}
	if err := os.WriteFile(outputPath, data, 0o600); err != nil {
		t.Fatalf("write WASM fixtures: %v", err)
	}
}

func objectsRequest(view protocol.UpdateView) commitmentObjects {
	return objectsRequestForSession(commitmentTestSession, view)
}

func objectsRequestForSession(sessionID string, view protocol.UpdateView) commitmentObjects {
	objects := make([]commitmentObject, len(view.Objects))
	for index, object := range view.Objects {
		objects[index] = commitmentObject{
			ObjectID: object.ObjectID, Root: object.Root, Kind: object.Kind,
			Entries: object.Entries, Commit: object.Commit,
		}
	}
	return commitmentObjects{Profile: commitmentObjectsProfile, SessionID: sessionID, Objects: objects}
}

func deltaRequest(baseRoot string, transition protocol.IntentTransition) commitmentDelta {
	return deltaRequestForSession(commitmentTestSession, baseRoot, transition)
}

func deltaRequestForSession(sessionID, baseRoot string, transition protocol.IntentTransition) commitmentDelta {
	changes := make([]commitmentChange, len(transition.Changes))
	for index, change := range transition.Changes {
		changes[index] = commitmentChange{
			Coordinate: change.Coordinate, Before: change.Before, After: change.After,
		}
	}
	return commitmentDelta{
		Profile: commitmentDeltaProfile, SessionID: sessionID, BaseRoot: baseRoot,
		ObjectID: transition.ObjectID, OldRoot: transition.OldRoot,
		Kind: transition.Kind, Changes: changes, Commit: transition.Commit,
	}
}

func referenceCompute(t *testing.T, backend maltcid.BackendKind, operationID string, view mutation.UpdateView, intent mutation.SemanticIntent) protocol.WriterComputeResult {
	t.Helper()
	runtime, err := clientwriter.NewRuntime(
		materializermemory.New(true),
		map[maltcid.BackendKind]commitment.IndexCommitment{backend: fixtureScheme(t, backend)},
	)
	if err != nil {
		t.Fatalf("NewRuntime(%s): %v", backend, err)
	}
	verified, err := runtime.VerifyUpdateView(t.Context(), view)
	if err != nil {
		t.Fatalf("VerifyUpdateView(%s): %v", backend, err)
	}
	result, err := runtime.ComputeBundle(t.Context(), operationID, verified, intent)
	if err != nil {
		t.Fatalf("ComputeBundle(%s): %v", backend, err)
	}
	wireResult, err := protocol.NewWriterComputeResult(result.Bundle, result.NextView, protocol.WriterComputeMetrics{})
	if err != nil {
		t.Fatalf("NewWriterComputeResult(%s): %v", backend, err)
	}
	return wireResult
}

func computeFixture(t *testing.T, backend maltcid.BackendKind) (mutation.UpdateView, mutation.SemanticIntent) {
	t.Helper()
	ctx := context.Background()
	semantic, err := mappingradix.NewMap(fixtureScheme(t, backend), materializermemory.New(true))
	if err != nil {
		t.Fatalf("NewMap failed: %v", err)
	}
	before := payloadCID(t, "before")
	after := payloadCID(t, "after")
	root, err := semantic.Commit(ctx, "fixture", mapping.NewViewFrom(map[string]cid.Cid{"file": before}))
	if err != nil {
		t.Fatalf("Commit fixture failed: %v", err)
	}
	coordinate, err := arcset.NewMapCoordinate("file")
	if err != nil {
		t.Fatalf("NewMapCoordinate failed: %v", err)
	}
	entries, err := arcset.NewCanonicalArcSet(arcset.KindMap, []arcset.ArcEntry{{
		Coordinate: coordinate, Target: arcset.NewCASTarget(before),
	}})
	if err != nil {
		t.Fatalf("NewCanonicalArcSet failed: %v", err)
	}
	view, err := mutation.NormalizeUpdateView(mutation.UpdateView{
		Profile: mutation.UpdateViewProfile, StateProfile: mutation.StatefulCompleteVectorsProfile,
		BaseRoot: root,
		Bounds:   mutation.UpdateViewBounds{MaxObjects: 8, MaxTotalEntries: 64, MaxDepth: 8},
		Objects: []mutation.UpdateObject{{
			ObjectID: "root", Root: root, Kind: arcset.KindMap, Entries: entries,
		}},
	})
	if err != nil {
		t.Fatalf("NormalizeUpdateView failed: %v", err)
	}
	beforeTarget := arcset.NewCASTarget(before)
	afterTarget := arcset.NewCASTarget(after)
	intent, err := mutation.NormalizeSemanticIntent(view, mutation.SemanticIntent{
		Profile: mutation.SemanticIntentProfile, BaseRoot: root, TopOutputID: "root-output",
		Transitions: []mutation.IntentTransition{{
			ID: "root-output", ObjectID: "root", OldRoot: root, Kind: arcset.KindMap,
			Backend: backend,
			Changes: []mutation.IntentChange{{
				Coordinate: coordinate, Before: &beforeTarget, After: &afterTarget,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("NormalizeSemanticIntent failed: %v", err)
	}
	return view, intent
}

func computeNestedFixture(t *testing.T, backend maltcid.BackendKind) (mutation.UpdateView, mutation.SemanticIntent) {
	t.Helper()
	ctx := context.Background()
	semantic, err := mappingradix.NewMap(fixtureScheme(t, backend), materializermemory.New(true))
	if err != nil {
		t.Fatalf("NewMap nested fixture: %v", err)
	}
	before := payloadCID(t, "nested-before")
	after := payloadCID(t, "nested-after")
	childRoot, err := semantic.Commit(ctx, "nested-child", mapping.NewViewFrom(map[string]cid.Cid{"leaf": before}))
	if err != nil {
		t.Fatalf("Commit nested child: %v", err)
	}
	parentRoot, err := semantic.Commit(ctx, "nested-parent", mapping.NewViewFrom(map[string]cid.Cid{"child": childRoot}))
	if err != nil {
		t.Fatalf("Commit nested parent: %v", err)
	}
	childCoordinate, _ := arcset.NewMapCoordinate("leaf")
	parentCoordinate, _ := arcset.NewMapCoordinate("child")
	childEntries, err := arcset.NewCanonicalArcSet(arcset.KindMap, []arcset.ArcEntry{{
		Coordinate: childCoordinate, Target: arcset.NewCASTarget(before),
	}})
	if err != nil {
		t.Fatalf("child arcset: %v", err)
	}
	parentEntries, err := arcset.NewCanonicalArcSet(arcset.KindMap, []arcset.ArcEntry{{
		Coordinate: parentCoordinate, Target: arcset.NewMapTarget(childRoot),
	}})
	if err != nil {
		t.Fatalf("parent arcset: %v", err)
	}
	view, err := mutation.NormalizeUpdateView(mutation.UpdateView{
		Profile: mutation.UpdateViewProfile, StateProfile: mutation.StatefulCompleteVectorsProfile,
		BaseRoot: parentRoot,
		Bounds:   mutation.UpdateViewBounds{MaxObjects: 8, MaxTotalEntries: 64, MaxDepth: 8},
		Objects: []mutation.UpdateObject{
			{ObjectID: "root", Root: parentRoot, Kind: arcset.KindMap, Entries: parentEntries},
			{ObjectID: "child", Root: childRoot, Kind: arcset.KindMap, Entries: childEntries},
		},
	})
	if err != nil {
		t.Fatalf("NormalizeUpdateView nested fixture: %v", err)
	}
	childBefore := arcset.NewCASTarget(before)
	childAfter := arcset.NewCASTarget(after)
	parentBefore := arcset.NewMapTarget(childRoot)
	intent, err := mutation.NormalizeSemanticIntent(view, mutation.SemanticIntent{
		Profile: mutation.SemanticIntentProfile, BaseRoot: parentRoot, TopOutputID: "root-output",
		Transitions: []mutation.IntentTransition{
			{
				ID: "root-output", ObjectID: "root", OldRoot: parentRoot,
				Kind: arcset.KindMap, Backend: backend,
				Changes: []mutation.IntentChange{{
					Coordinate: parentCoordinate, Before: &parentBefore,
					OutputID: "child-output", OutputKind: arcset.TargetKindMap,
				}},
			},
			{
				ID: "child-output", ObjectID: "child", OldRoot: childRoot,
				Kind: arcset.KindMap, Backend: backend, ExpectedUses: 1,
				Changes: []mutation.IntentChange{{
					Coordinate: childCoordinate, Before: &childBefore, After: &childAfter,
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("NormalizeSemanticIntent nested fixture: %v", err)
	}
	return view, intent
}

func computeListFixture(t *testing.T, backend maltcid.BackendKind) (mutation.UpdateView, mutation.SemanticIntent) {
	t.Helper()
	ctx := context.Background()
	semantic, err := listtree.NewList(fixtureScheme(t, backend), materializermemory.New(true))
	if err != nil {
		t.Fatalf("NewList fixture: %v", err)
	}
	before0 := payloadCID(t, "list-before-0")
	before1 := payloadCID(t, "list-before-1")
	after1 := payloadCID(t, "list-after-1")
	after2 := payloadCID(t, "list-after-2")
	root, err := semantic.Commit(ctx, "list-fixture", list.NewViewFromSlice([]cid.Cid{before0, before1}))
	if err != nil {
		t.Fatalf("Commit list fixture: %v", err)
	}
	coordinate0 := arcset.NewListCoordinateUint64(0)
	coordinate1 := arcset.NewListCoordinateUint64(1)
	coordinate2 := arcset.NewListCoordinateUint64(2)
	entries, err := arcset.NewCanonicalArcSet(arcset.KindList, []arcset.ArcEntry{
		{Coordinate: coordinate0, Target: arcset.NewCASTarget(before0)},
		{Coordinate: coordinate1, Target: arcset.NewCASTarget(before1)},
	})
	if err != nil {
		t.Fatalf("list arcset: %v", err)
	}
	view, err := mutation.NormalizeUpdateView(mutation.UpdateView{
		Profile: mutation.UpdateViewProfile, StateProfile: mutation.StatefulCompleteVectorsProfile,
		BaseRoot: root,
		Bounds:   mutation.UpdateViewBounds{MaxObjects: 8, MaxTotalEntries: 64, MaxDepth: 8},
		Objects: []mutation.UpdateObject{{
			ObjectID: "root", Root: root, Kind: arcset.KindList, Entries: entries,
		}},
	})
	if err != nil {
		t.Fatalf("NormalizeUpdateView list fixture: %v", err)
	}
	beforeTarget := arcset.NewCASTarget(before1)
	afterTarget1 := arcset.NewCASTarget(after1)
	afterTarget2 := arcset.NewCASTarget(after2)
	intent, err := mutation.NormalizeSemanticIntent(view, mutation.SemanticIntent{
		Profile: mutation.SemanticIntentProfile, BaseRoot: root, TopOutputID: "root-output",
		Transitions: []mutation.IntentTransition{{
			ID: "root-output", ObjectID: "root", OldRoot: root,
			Kind: arcset.KindList, Backend: backend,
			Changes: []mutation.IntentChange{
				{Coordinate: coordinate2, After: &afterTarget2},
				{Coordinate: coordinate1, Before: &beforeTarget, After: &afterTarget1},
			},
		}},
	})
	if err != nil {
		t.Fatalf("NormalizeSemanticIntent list fixture: %v", err)
	}
	return view, intent
}

func computeFixedListUint64Fixture(t *testing.T, backend maltcid.BackendKind) (mutation.UpdateView, mutation.SemanticIntent) {
	t.Helper()
	ctx := context.Background()
	semantic, err := listtree.NewList(fixtureScheme(t, backend), materializermemory.New(true))
	if err != nil {
		t.Fatalf("NewList fixed fixture: %v", err)
	}
	const totalSize = uint64(1<<53 + 1)
	before := payloadCID(t, "fixed-list-u64-before")
	after := payloadCID(t, "fixed-list-u64-after")
	root, err := semantic.CommitFixed(ctx, "fixed-list-u64-fixture", []cid.Cid{before}, totalSize, totalSize)
	if err != nil {
		t.Fatalf("CommitFixed fixture: %v", err)
	}
	coordinate := arcset.NewListCoordinateUint64(0)
	entries, err := arcset.NewCanonicalArcSet(arcset.KindList, []arcset.ArcEntry{{
		Coordinate: coordinate, Target: arcset.NewCASTarget(before),
	}})
	if err != nil {
		t.Fatalf("fixed-list arcset: %v", err)
	}
	commit := mutation.CommitDescriptor{FixedList: &mutation.FixedListCommit{
		TotalSize: totalSize, ChunkSize: totalSize,
	}}
	view, err := mutation.NormalizeUpdateView(mutation.UpdateView{
		Profile: mutation.UpdateViewProfile, StateProfile: mutation.StatefulCompleteVectorsProfile,
		BaseRoot: root,
		Bounds:   mutation.UpdateViewBounds{MaxObjects: 8, MaxTotalEntries: 64, MaxDepth: 8},
		Objects: []mutation.UpdateObject{{
			ObjectID: "root", Root: root, Kind: arcset.KindList, Entries: entries, Commit: commit,
		}},
	})
	if err != nil {
		t.Fatalf("NormalizeUpdateView fixed-list fixture: %v", err)
	}
	beforeTarget := arcset.NewCASTarget(before)
	afterTarget := arcset.NewCASTarget(after)
	intent, err := mutation.NormalizeSemanticIntent(view, mutation.SemanticIntent{
		Profile: mutation.SemanticIntentProfile, BaseRoot: root, TopOutputID: "root-output",
		Transitions: []mutation.IntentTransition{{
			ID: "root-output", ObjectID: "root", OldRoot: root,
			Kind: arcset.KindList, Backend: backend, Commit: commit,
			Changes: []mutation.IntentChange{{
				Coordinate: coordinate, Before: &beforeTarget, After: &afterTarget,
			}},
		}},
	})
	if err != nil {
		t.Fatalf("NormalizeSemanticIntent fixed-list fixture: %v", err)
	}
	return view, intent
}

func fixtureScheme(t *testing.T, backend maltcid.BackendKind) commitment.IndexCommitment {
	t.Helper()
	var (
		scheme commitment.IndexCommitment
		err    error
	)
	switch backend {
	case maltcid.BackendKindKZG:
		scheme, err = kzg.NewScheme()
	case maltcid.BackendKindIPA:
		scheme, err = ipa.NewScheme()
	default:
		t.Fatalf("unsupported fixture backend %q", backend)
	}
	if err != nil {
		t.Fatalf("NewScheme(%s): %v", backend, err)
	}
	return scheme
}

func payloadCID(t *testing.T, value string) cid.Cid {
	t.Helper()
	digest, err := mh.Sum([]byte(value), mh.SHA2_256, -1)
	if err != nil {
		t.Fatalf("multihash: %v", err)
	}
	return cid.NewCidV1(cid.Raw, digest)
}

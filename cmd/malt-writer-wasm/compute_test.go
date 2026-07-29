package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt/auth/arcset"
	materializermemory "github.com/dewebprotocol/malt/auth/arcset/materializer/memory"
	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/auth/commitment/ipa"
	"github.com/dewebprotocol/malt/auth/commitment/kzg"
	"github.com/dewebprotocol/malt/auth/semantic/mapping"
	mappingradix "github.com/dewebprotocol/malt/auth/semantic/mapping/radix"
	"github.com/dewebprotocol/malt/mutation"
	"github.com/dewebprotocol/malt/protocol"
	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestParseStartupBackend(t *testing.T) {
	for _, backend := range []string{"kzg", "ipa"} {
		t.Run(backend, func(t *testing.T) {
			got, err := parseStartupBackend([]string{backendArgumentPrefix + backend})
			if err != nil {
				t.Fatalf("parseStartupBackend failed: %v", err)
			}
			if got != backend {
				t.Fatalf("backend = %q, want %q", got, backend)
			}
		})
	}
	for _, args := range [][]string{
		nil,
		{"kzg"},
		{backendArgumentPrefix},
		{backendArgumentPrefix + "all"},
		{backendArgumentPrefix + "kzg", backendArgumentPrefix + "ipa"},
	} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			if backend, err := parseStartupBackend(args); err == nil {
				t.Fatalf("parseStartupBackend(%q) = %q, want error", args, backend)
			}
		})
	}
}

func TestComputerComputesCanonicalClientRootBundle(t *testing.T) {
	view, intent := computeFixture(t, maltcid.BackendKindKZG)
	wireView, err := protocol.NewUpdateView(view)
	if err != nil {
		t.Fatalf("NewUpdateView failed: %v", err)
	}
	wireIntent, err := protocol.NewSemanticIntent(view, intent)
	if err != nil {
		t.Fatalf("NewSemanticIntent failed: %v", err)
	}
	viewJSON, err := json.Marshal(wireView)
	if err != nil {
		t.Fatalf("marshal update view: %v", err)
	}
	intentJSON, err := json.Marshal(wireIntent)
	if err != nil {
		t.Fatalf("marshal semantic intent: %v", err)
	}

	writer, err := newComputer("kzg")
	if err != nil {
		t.Fatalf("newComputer failed: %v", err)
	}
	raw, err := writer.compute(t.Context(), "browser-operation-1", viewJSON, intentJSON)
	if err != nil {
		t.Fatalf("compute failed: %v", err)
	}
	response, err := protocol.DecodeWriterComputeResult(raw)
	if err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Profile != protocol.WriterComputeResultProfile {
		t.Fatalf("profile = %q, want %q", response.Profile, protocol.WriterComputeResultProfile)
	}
	bundle, err := response.Bundle.Core()
	if err != nil {
		t.Fatalf("bundle Core failed: %v", err)
	}
	if bundle.Candidate.Equals(view.BaseRoot) {
		t.Fatal("client writer did not change the candidate root")
	}
	if maltcid.BackendKindOf(bundle.Candidate) != maltcid.BackendKindKZG {
		t.Fatalf("candidate backend = %q, want KZG", maltcid.BackendKindOf(bundle.Candidate))
	}
	if bundle.OperationID != "browser-operation-1" || len(bundle.Outputs) != 1 || len(bundle.PayloadCIDs) != 1 {
		t.Fatalf("unexpected bundle summary: operation=%q outputs=%d payloads=%d", bundle.OperationID, len(bundle.Outputs), len(bundle.PayloadCIDs))
	}
	next, err := response.NextView.Core()
	if err != nil {
		t.Fatalf("next view Core failed: %v", err)
	}
	if !next.BaseRoot.Equals(bundle.Candidate) {
		t.Fatalf("next base %s does not match candidate %s", next.BaseRoot, bundle.Candidate)
	}
	if response.Metrics.CommitmentUpdateNS == 0 || response.Metrics.TotalNS == 0 {
		t.Fatalf("writer metrics did not record local commitment work: %+v", response.Metrics)
	}
}

func TestComputerRejectsUnavailableBackendAndInvalidJSON(t *testing.T) {
	view, intent := computeFixture(t, maltcid.BackendKindKZG)
	wireView, err := protocol.NewUpdateView(view)
	if err != nil {
		t.Fatalf("NewUpdateView failed: %v", err)
	}
	wireIntent, err := protocol.NewSemanticIntent(view, intent)
	if err != nil {
		t.Fatalf("NewSemanticIntent failed: %v", err)
	}
	viewJSON, _ := json.Marshal(wireView)
	intentJSON, _ := json.Marshal(wireIntent)

	writer, err := newComputer("ipa")
	if err != nil {
		t.Fatalf("newComputer failed: %v", err)
	}
	if _, err := writer.compute(t.Context(), "browser-operation-2", viewJSON, intentJSON); err == nil {
		t.Fatal("IPA-only writer accepted a KZG update view")
	}
	if _, err := writer.compute(t.Context(), "browser-operation-3", []byte(`{}`), []byte(`{}`)); err == nil {
		t.Fatal("writer accepted invalid client-root JSON")
	}
}

func TestSessionComputerAdvancesOnlyAfterExactReceipt(t *testing.T) {
	view, intent := computeFixture(t, maltcid.BackendKindKZG)
	wireView, err := protocol.NewUpdateView(view)
	if err != nil {
		t.Fatalf("NewUpdateView failed: %v", err)
	}
	wireIntent, err := protocol.NewSemanticIntent(view, intent)
	if err != nil {
		t.Fatalf("NewSemanticIntent failed: %v", err)
	}
	viewJSON, _ := json.Marshal(wireView)
	intentJSON, _ := json.Marshal(wireIntent)
	computer, err := newComputer("kzg")
	if err != nil {
		t.Fatalf("newComputer failed: %v", err)
	}
	session, err := newSessionComputer(computer)
	if err != nil {
		t.Fatalf("newSessionComputer failed: %v", err)
	}
	loadedRoot, err := session.load(t.Context(), viewJSON)
	if err != nil {
		t.Fatalf("load failed: %v", err)
	}
	if loadedRoot != view.BaseRoot.String() {
		t.Fatalf("loaded root = %s, want %s", loadedRoot, view.BaseRoot)
	}

	const operationID = "session-operation-1"
	raw, err := session.prepare(t.Context(), operationID, intentJSON)
	if err != nil {
		t.Fatalf("prepare failed: %v", err)
	}
	if _, err := session.prepare(t.Context(), operationID, intentJSON); err == nil {
		t.Fatal("session accepted a duplicate prepared operation")
	}
	response, err := protocol.DecodeWriterComputeResult(raw)
	if err != nil {
		t.Fatalf("DecodeWriterComputeResult failed: %v", err)
	}
	bundle, err := response.Bundle.Core()
	if err != nil {
		t.Fatalf("bundle Core failed: %v", err)
	}
	bundleDigest, err := bundle.Digest()
	if err != nil {
		t.Fatalf("bundle Digest failed: %v", err)
	}
	receipt, err := protocol.NewMaterializationReceipt(mutation.MaterializationReceipt{
		Profile: mutation.MaterializationReceiptProfile, OperationID: operationID,
		BaseRoot: bundle.View.BaseRoot, Candidate: bundle.Candidate,
		BundleDigest: bundleDigest, DurableBoundary: "unit-memory-v1",
	}, bundle)
	if err != nil {
		t.Fatalf("NewMaterializationReceipt failed: %v", err)
	}
	badReceipt := receipt
	badReceipt.Candidate = view.BaseRoot.String()
	badReceiptJSON, _ := json.Marshal(badReceipt)
	if _, err := session.acceptReceipt(operationID, badReceiptJSON); err == nil {
		t.Fatal("session accepted a mismatched receipt")
	}
	if got := session.session.BaseRoot(); !got.Equals(view.BaseRoot) {
		t.Fatalf("base advanced after bad receipt: %s", got)
	}

	receiptJSON, _ := json.Marshal(receipt)
	acceptedRoot, err := session.acceptReceipt(operationID, receiptJSON)
	if err != nil {
		t.Fatalf("acceptReceipt failed: %v", err)
	}
	if acceptedRoot != bundle.Candidate.String() {
		t.Fatalf("accepted root = %s, want %s", acceptedRoot, bundle.Candidate)
	}
	if got := session.session.BaseRoot(); !got.Equals(bundle.Candidate) {
		t.Fatalf("retained base = %s, want %s", got, bundle.Candidate)
	}
	fresh, err := newSessionComputer(computer)
	if err != nil {
		t.Fatal(err)
	}
	nextViewJSON, err := json.Marshal(response.NextView)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.load(t.Context(), nextViewJSON); err != nil {
		t.Fatal(err)
	}
	if got, want := session.store.EntryCount(), fresh.store.EntryCount(); got != want {
		t.Fatalf("accepted session retained %d entries, fresh accepted view retains %d", got, want)
	}
	if _, err := session.prepare(t.Context(), "stale-operation", intentJSON); err == nil {
		t.Fatal("session accepted an intent at the stale base")
	}
}

func TestSessionComputerDiscardReclaimsCandidateSnapshots(t *testing.T) {
	view, intent := computeFixture(t, maltcid.BackendKindIPA)
	wireView, err := protocol.NewUpdateView(view)
	if err != nil {
		t.Fatal(err)
	}
	wireIntent, err := protocol.NewSemanticIntent(view, intent)
	if err != nil {
		t.Fatal(err)
	}
	viewJSON, _ := json.Marshal(wireView)
	intentJSON, _ := json.Marshal(wireIntent)
	computer, err := newComputer("ipa")
	if err != nil {
		t.Fatal(err)
	}
	session, err := newSessionComputer(computer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.load(t.Context(), viewJSON); err != nil {
		t.Fatal(err)
	}
	baseline := session.store.EntryCount()
	if _, err := session.prepare(t.Context(), "discard-operation", intentJSON); err != nil {
		t.Fatal(err)
	}
	if got := session.store.EntryCount(); got <= baseline {
		t.Fatalf("prepare retained %d entries, want more than loaded baseline %d", got, baseline)
	}
	if err := session.discard("discard-operation"); err != nil {
		t.Fatal(err)
	}
	if got := session.store.EntryCount(); got != baseline {
		t.Fatalf("discard retained %d entries, want loaded baseline %d", got, baseline)
	}
}

func TestSessionComputerPrepareCompactReturnsCompleteRootSummary(t *testing.T) {
	view, intent := computeFixture(t, maltcid.BackendKindKZG)
	wireView, err := protocol.NewUpdateView(view)
	if err != nil {
		t.Fatal(err)
	}
	wireIntent, err := protocol.NewSemanticIntent(view, intent)
	if err != nil {
		t.Fatal(err)
	}
	viewJSON, _ := json.Marshal(wireView)
	intentJSON, _ := json.Marshal(wireIntent)
	computer, err := newComputer("kzg")
	if err != nil {
		t.Fatal(err)
	}
	session, err := newSessionComputer(computer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.load(t.Context(), viewJSON); err != nil {
		t.Fatal(err)
	}

	const operationID = "compact-operation"
	raw, err := session.prepareCompact(t.Context(), operationID, intentJSON)
	if err != nil {
		t.Fatal(err)
	}
	var summary writerPrepareSummary
	if err := json.Unmarshal(raw, &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Profile != writerPrepareSummaryV1 {
		t.Fatalf("profile = %q, want %q", summary.Profile, writerPrepareSummaryV1)
	}
	if summary.OperationID != operationID {
		t.Fatalf("operation ID = %q, want %q", summary.OperationID, operationID)
	}
	candidate := session.prepared[operationID]
	if summary.Candidate != candidate.result.Bundle.Candidate.String() {
		t.Fatalf("candidate = %q, want %q", summary.Candidate, candidate.result.Bundle.Candidate)
	}
	if got, want := len(summary.Outputs), len(candidate.result.Bundle.Outputs); got != want {
		t.Fatalf("outputs = %d, want %d", got, want)
	}
	for index, output := range candidate.result.Bundle.Outputs {
		if got := summary.Outputs[index]; got.TransitionID != output.TransitionID || got.Root != output.Root.String() {
			t.Fatalf("output %d = %+v, want %s/%s", index, got, output.TransitionID, output.Root)
		}
	}
	if got, want := len(summary.PayloadCIDs), len(candidate.result.Bundle.PayloadCIDs); got != want {
		t.Fatalf("payload CIDs = %d, want %d", got, want)
	}
	for index, payload := range candidate.result.Bundle.PayloadCIDs {
		if summary.PayloadCIDs[index] != payload.String() {
			t.Fatalf("payload CID %d = %q, want %q", index, summary.PayloadCIDs[index], payload)
		}
	}
	if candidate.responseBytes <= len(raw) {
		t.Fatalf("retained full response = %d bytes, compact response = %d bytes", candidate.responseBytes, len(raw))
	}
	if err := session.discard(operationID); err != nil {
		t.Fatal(err)
	}
}

type wasmComputeFixture struct {
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

	fixtures := make([]wasmComputeFixture, 0, 2)
	for _, backend := range []maltcid.BackendKind{maltcid.BackendKindKZG, maltcid.BackendKindIPA} {
		view, intent := computeFixture(t, backend)
		wireView, err := protocol.NewUpdateView(view)
		if err != nil {
			t.Fatalf("NewUpdateView(%s) failed: %v", backend, err)
		}
		wireIntent, err := protocol.NewSemanticIntent(view, intent)
		if err != nil {
			t.Fatalf("NewSemanticIntent(%s) failed: %v", backend, err)
		}
		viewJSON, err := json.Marshal(wireView)
		if err != nil {
			t.Fatalf("marshal update view (%s): %v", backend, err)
		}
		intentJSON, err := json.Marshal(wireIntent)
		if err != nil {
			t.Fatalf("marshal semantic intent (%s): %v", backend, err)
		}
		operationID := "wasm-" + string(backend) + "-fixture"
		writer, err := newComputer(string(backend))
		if err != nil {
			t.Fatalf("newComputer(%s) failed: %v", backend, err)
		}
		raw, err := writer.compute(t.Context(), operationID, viewJSON, intentJSON)
		if err != nil {
			t.Fatalf("compute fixture (%s) failed: %v", backend, err)
		}
		response, err := protocol.DecodeWriterComputeResult(raw)
		if err != nil {
			t.Fatalf("decode fixture response (%s): %v", backend, err)
		}
		bundle, err := response.Bundle.Core()
		if err != nil {
			t.Fatalf("decode fixture bundle (%s): %v", backend, err)
		}
		bundleDigest, err := bundle.Digest()
		if err != nil {
			t.Fatalf("digest fixture bundle (%s): %v", backend, err)
		}
		receipt, err := protocol.NewMaterializationReceipt(mutation.MaterializationReceipt{
			Profile: mutation.MaterializationReceiptProfile, OperationID: operationID,
			BaseRoot: bundle.View.BaseRoot, Candidate: bundle.Candidate,
			BundleDigest: bundleDigest, DurableBoundary: "wasm-smoke-memory-v1",
		}, bundle)
		if err != nil {
			t.Fatalf("encode fixture receipt (%s): %v", backend, err)
		}
		fixtures = append(fixtures, wasmComputeFixture{
			Backend: backend, OperationID: operationID,
			UpdateView: wireView, SemanticIntent: wireIntent,
			ExpectedBundle: response.Bundle, ExpectedNextView: response.NextView,
			ExpectedReceipt: receipt,
		})
	}

	data, err := json.Marshal(fixtures)
	if err != nil {
		t.Fatalf("marshal WASM fixtures: %v", err)
	}
	if err := os.WriteFile(outputPath, data, 0o600); err != nil {
		t.Fatalf("write WASM fixtures: %v", err)
	}
}

func computeFixture(t *testing.T, backend maltcid.BackendKind) (mutation.UpdateView, mutation.SemanticIntent) {
	t.Helper()
	ctx := context.Background()
	scheme := fixtureScheme(t, backend)
	semantic, err := mappingradix.NewMap(scheme, materializermemory.New(true))
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
		Coordinate: coordinate,
		Target:     arcset.NewCASTarget(before),
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
		t.Fatalf("NewScheme(%s) failed: %v", backend, err)
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

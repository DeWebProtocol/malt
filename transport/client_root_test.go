package transport_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	client "github.com/dewebprotocol/malt-client/transport"
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

func TestClientRootTransportStrictlyBindsViewBundleReceiptAndMetrics(t *testing.T) {
	view, bundle := clientRootTransportFixture(t)
	wireView, err := protocol.NewUpdateView(view)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := bundle.Digest()
	if err != nil {
		t.Fatal(err)
	}
	receipt := mutation.MaterializationReceipt{
		Profile: mutation.MaterializationReceiptProfile, OperationID: bundle.OperationID,
		BaseRoot: bundle.View.BaseRoot, Candidate: bundle.Candidate, BundleDigest: digest,
		DurableBoundary: "gateway-client-root-atomic-v1",
	}
	wireReceipt, err := protocol.NewMaterializationReceipt(receipt, bundle)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Cache-Control", "private, no-store")
		switch request.URL.Path {
		case "/v1/roots/" + view.BaseRoot.String() + "/update-view":
			if request.Method != http.MethodGet || request.URL.Query().Get("max_objects") != "8" ||
				request.URL.Query().Get("max_total_entries") != "64" || request.URL.Query().Get("max_depth") != "8" {
				t.Fatalf("unexpected update-view request: %s %s", request.Method, request.URL.String())
			}
			_ = json.NewEncoder(response).Encode(wireView)
		case "/v1/client-roots":
			if request.Method != http.MethodPost {
				t.Fatalf("client-root method = %s", request.Method)
			}
			var submitted protocol.ClientRootBundle
			if err := json.NewDecoder(request.Body).Decode(&submitted); err != nil {
				t.Fatal(err)
			}
			core, err := submitted.Core()
			if err != nil {
				t.Fatal(err)
			}
			if !core.Candidate.Equals(bundle.Candidate) || core.OperationID != bundle.OperationID {
				t.Fatalf("submitted bundle = %#v", core)
			}
			response.Header().Set("X-Malt-Client-Root-Old-State-Validation-Nanos", "11")
			response.Header().Set("X-Malt-Client-Root-Gateway-Replay-Nanos", "22")
			response.Header().Set("X-Malt-Client-Root-Persist-Nanos", "33")
			response.Header().Set("X-Malt-Client-Root-Receipt-Nanos", "44")
			response.Header().Set("X-Malt-Client-Root-Durable-Boundary", receipt.DurableBoundary)
			response.Header().Set("X-Malt-Client-Root-Idempotent", "false")
			setWriteAccountingHeader(t, response.Header())
			response.Header().Set("Server-Timing", "old-state-validation;dur=0.000011, gateway-replay;dur=0.000022, persist;dur=0.000033, receipt;dur=0.000044")
			_ = json.NewEncoder(response).Encode(wireReceipt)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	transport, err := client.NewWithBaseURL(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	fetched, err := transport.FetchUpdateView(t.Context(), view.BaseRoot, &protocol.UpdateViewBounds{MaxObjects: 8, MaxTotalEntries: 64, MaxDepth: 8})
	if err != nil {
		t.Fatal(err)
	}
	if !fetched.View.BaseRoot.Equals(view.BaseRoot) || fetched.WireBytes == 0 {
		t.Fatalf("fetched view = %#v", fetched)
	}
	accepted, err := transport.SubmitClientRoot(t.Context(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !accepted.Receipt.Candidate.Equals(bundle.Candidate) || accepted.RequestWireBytes == 0 || accepted.ResponseWireBytes == 0 ||
		accepted.RequestEncodingNS == 0 || accepted.ResponseVerifyNS == 0 {
		t.Fatalf("accepted response = %#v", accepted)
	}
	if accepted.Gateway != (client.ClientRootPhaseMetrics{OldStateValidationNS: 11, GatewayReplayNS: 22, PersistNS: 33, ReceiptNS: 44}) {
		t.Fatalf("Gateway metrics = %#v", accepted.Gateway)
	}
	if !accepted.WriteAccounting.Available || accepted.WriteAccountingWireBytes == 0 || accepted.WriteAccounting.ObjectLedgerSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("Gateway write accounting = %#v bytes=%d", accepted.WriteAccounting, accepted.WriteAccountingWireBytes)
	}
}

func TestClientRootTransportUsesAuthenticatedBucketRoutes(t *testing.T) {
	view, prepared := clientRootTransportComputeFixture(t)
	bundle := prepared.Bundle
	wireView, err := protocol.NewUpdateView(view)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := bundle.Digest()
	if err != nil {
		t.Fatal(err)
	}
	receipt := mutation.MaterializationReceipt{
		Profile: mutation.MaterializationReceiptProfile, OperationID: bundle.OperationID,
		BaseRoot: bundle.View.BaseRoot, Candidate: bundle.Candidate, BundleDigest: digest,
		DurableBoundary: "gateway-client-root-atomic-v1",
	}
	wireReceipt, err := protocol.NewMaterializationReceipt(receipt, bundle)
	if err != nil {
		t.Fatal(err)
	}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests++
		if request.Header.Get("Authorization") != "Bearer tenant-secret" {
			t.Fatalf("Authorization=%q", request.Header.Get("Authorization"))
		}
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Cache-Control", "private, no-store")
		switch request.URL.Path {
		case "/v1/buckets/bkt_test/roots/" + view.BaseRoot.String() + "/update-view":
			if request.Method != http.MethodGet {
				t.Fatalf("update-view method=%s", request.Method)
			}
			_ = json.NewEncoder(response).Encode(wireView)
		case "/v1/buckets/bkt_test/client-roots":
			if request.Method != http.MethodPost {
				t.Fatalf("client-root method=%s", request.Method)
			}
			var submitted protocol.WriterComputeResult
			if err := json.NewDecoder(request.Body).Decode(&submitted); err != nil {
				t.Fatal(err)
			}
			if submitted.Profile != protocol.WriterComputeResultProfile {
				t.Fatalf("writer result profile=%q", submitted.Profile)
			}
			submittedBundle, err := submitted.Bundle.Core()
			if err != nil || !submittedBundle.Candidate.Equals(bundle.Candidate) {
				t.Fatalf("submitted bundle=%#v err=%v", submittedBundle, err)
			}
			if _, err := submitted.Materialization.Core(submittedBundle); err != nil {
				t.Fatalf("submitted materialization: %v", err)
			}
			response.Header().Set("X-Malt-Client-Root-Old-State-Validation-Nanos", "1")
			response.Header().Set("X-Malt-Client-Root-Gateway-Replay-Nanos", "1")
			response.Header().Set("X-Malt-Client-Root-Persist-Nanos", "1")
			response.Header().Set("X-Malt-Client-Root-Receipt-Nanos", "1")
			response.Header().Set("X-Malt-Client-Root-Durable-Boundary", receipt.DurableBoundary)
			response.Header().Set("X-Malt-Client-Root-Idempotent", "false")
			response.Header().Set("Server-Timing", "old-state-validation;dur=1, gateway-replay;dur=1, persist;dur=1, receipt;dur=1")
			setWriteAccountingHeader(t, response.Header())
			_ = json.NewEncoder(response).Encode(wireReceipt)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	transport, err := client.New(client.Options{
		BaseURL: server.URL, BucketID: "bkt_test", TenantBearerToken: "tenant-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transport.FetchUpdateView(t.Context(), view.BaseRoot, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := transport.SubmitClientRoot(t.Context(), bundle); err == nil {
		t.Fatal("managed Bucket transport accepted a bare client-root bundle")
	}
	if _, err := transport.SubmitClientRootResult(t.Context(), prepared); err != nil {
		t.Fatal(err)
	}
	if requests != 2 {
		t.Fatalf("request count=%d, want 2", requests)
	}
	plain, err := client.NewWithBaseURL(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := plain.SubmitClientRootResult(t.Context(), prepared); err == nil {
		t.Fatal("unscoped transport accepted a managed writer result")
	}
}

func setWriteAccountingHeader(t *testing.T, header http.Header) {
	t.Helper()
	accounting := validWriteAccounting()
	raw, err := json.Marshal(accounting)
	if err != nil {
		t.Fatal(err)
	}
	header.Set("X-Malt-Client-Root-Write-Accounting", base64.RawURLEncoding.EncodeToString(raw))
}

func validWriteAccounting() client.ClientRootWriteAccounting {
	accounting := client.ClientRootWriteAccounting{
		Profile: "gateway.client-root-write-accounting/v1", Available: true,
		ByteMethod: "logical-kv-key-plus-value-bytes/v1", ObjectLedgerSHA256: strings.Repeat("a", 64),
	}
	for _, category := range []string{"semantic-materialization", "arctable-records", "root-version-metadata"} {
		accounting.Categories = append(accounting.Categories, client.ClientRootWriteCategoryAccounting{
			Category: category, AttemptedWrites: 1, AttemptedBytes: 2, AttemptedNewWrites: 1, AttemptedNewBytes: 2,
			NewlyPersistedWrites: 1, GrossNewBytes: 2, NewWrites: 1, NewBytes: 2, NetBytes: 2,
		})
	}
	return accounting
}

func TestClientRootTransportRejectsHostileWriteAccounting(t *testing.T) {
	_, bundle := clientRootTransportFixture(t)
	digest, err := bundle.Digest()
	if err != nil {
		t.Fatal(err)
	}
	receipt := mutation.MaterializationReceipt{
		Profile: mutation.MaterializationReceiptProfile, OperationID: bundle.OperationID,
		BaseRoot: bundle.View.BaseRoot, Candidate: bundle.Candidate, BundleDigest: digest,
		DurableBoundary: "gateway-client-root-atomic-v1",
	}
	wireReceipt, err := protocol.NewMaterializationReceipt(receipt, bundle)
	if err != nil {
		t.Fatal(err)
	}

	validRaw, err := json.Marshal(validWriteAccounting())
	if err != nil {
		t.Fatal(err)
	}
	inconsistent := validWriteAccounting()
	inconsistent.Categories[0].AttemptedWrites++
	inconsistentRaw, err := json.Marshal(inconsistent)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"duplicate key":       bytes.Replace(validRaw, []byte(`{"profile":`), []byte(`{"profile":"gateway.client-root-write-accounting/v1","profile":`), 1),
		"unknown field":       bytes.Replace(validRaw, []byte(`{"profile":`), []byte(`{"surprise":1,"profile":`), 1),
		"inconsistent totals": inconsistentRaw,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set("Content-Type", "application/json")
				response.Header().Set("Cache-Control", "no-store")
				response.Header().Set("X-Malt-Client-Root-Old-State-Validation-Nanos", "1")
				response.Header().Set("X-Malt-Client-Root-Gateway-Replay-Nanos", "1")
				response.Header().Set("X-Malt-Client-Root-Persist-Nanos", "1")
				response.Header().Set("X-Malt-Client-Root-Receipt-Nanos", "1")
				response.Header().Set("X-Malt-Client-Root-Durable-Boundary", receipt.DurableBoundary)
				response.Header().Set("X-Malt-Client-Root-Idempotent", "false")
				response.Header().Set("X-Malt-Client-Root-Write-Accounting", base64.RawURLEncoding.EncodeToString(raw))
				response.Header().Set("Server-Timing", "old-state-validation;dur=1, gateway-replay;dur=1, persist;dur=1, receipt;dur=1")
				_ = json.NewEncoder(response).Encode(wireReceipt)
			}))
			defer server.Close()
			transport, err := client.NewWithBaseURL(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := transport.SubmitClientRoot(t.Context(), bundle); err == nil {
				t.Fatalf("hostile write accounting was accepted: %s", raw)
			}
		})
	}
}

func TestClientRootTransportRejectsCacheableOrIncompleteResponses(t *testing.T) {
	view, bundle := clientRootTransportFixture(t)
	wireView, err := protocol.NewUpdateView(view)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("cacheable update view", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(wireView)
		}))
		defer server.Close()
		transport, _ := client.NewWithBaseURL(server.URL)
		if _, err := transport.FetchUpdateView(t.Context(), view.BaseRoot, nil); err == nil {
			t.Fatal("cacheable update view was accepted")
		}
	})

	t.Run("missing phase metric", func(t *testing.T) {
		digest, _ := bundle.Digest()
		wireReceipt, _ := protocol.NewMaterializationReceipt(mutation.MaterializationReceipt{
			Profile: mutation.MaterializationReceiptProfile, OperationID: bundle.OperationID,
			BaseRoot: bundle.View.BaseRoot, Candidate: bundle.Candidate, BundleDigest: digest,
			DurableBoundary: "gateway-client-root-atomic-v1",
		}, bundle)
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.Header().Set("Content-Type", "application/json")
			response.Header().Set("Cache-Control", "no-store")
			response.Header().Set("Server-Timing", "old-state-validation;dur=1, gateway-replay;dur=1, persist;dur=1, receipt;dur=1")
			response.Header().Set("X-Malt-Client-Root-Old-State-Validation-Nanos", "1")
			response.Header().Set("X-Malt-Client-Root-Gateway-Replay-Nanos", "1")
			response.Header().Set("X-Malt-Client-Root-Persist-Nanos", "1")
			response.Header().Set("X-Malt-Client-Root-Durable-Boundary", "gateway-client-root-atomic-v1")
			response.Header().Set("X-Malt-Client-Root-Idempotent", "false")
			_ = json.NewEncoder(response).Encode(wireReceipt)
		}))
		defer server.Close()
		transport, _ := client.NewWithBaseURL(server.URL)
		if _, err := transport.SubmitClientRoot(t.Context(), bundle); err == nil {
			t.Fatal("receipt with incomplete phase metrics was accepted")
		}
	})

	if _, err := (&client.Client{}).FetchUpdateView(t.Context(), view.BaseRoot, &protocol.UpdateViewBounds{MaxObjects: 1}); err == nil {
		t.Fatal("partial update-view bounds were accepted")
	}
}

func clientRootTransportFixture(t *testing.T) (mutation.UpdateView, mutation.ClientRootBundle) {
	t.Helper()
	view, prepared := clientRootTransportComputeFixture(t)
	return view, prepared.Bundle
}

func clientRootTransportComputeFixture(t *testing.T) (mutation.UpdateView, clientwriter.ComputeResult) {
	t.Helper()
	ctx := context.Background()
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatal(err)
	}
	oldPayload := clientRootRawCID(t, "old")
	newPayload := clientRootRawCID(t, "new")
	oldStore := materializermemory.New(true)
	oldMap, err := mappingradix.NewMap(scheme, oldStore)
	if err != nil {
		t.Fatal(err)
	}
	oldRoot, err := oldMap.Commit(ctx, "fixture", mapping.NewViewFrom(map[string]cid.Cid{"payload": oldPayload}))
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
	view := mutation.UpdateView{
		Profile: mutation.UpdateViewProfile, StateProfile: mutation.StatefulCompleteVectorsProfile,
		BaseRoot: oldRoot, Bounds: mutation.UpdateViewBounds{MaxObjects: 8, MaxTotalEntries: 64, MaxDepth: 8},
		Objects: []mutation.UpdateObject{{ObjectID: "root", Root: oldRoot, Kind: arcset.KindMap, Entries: entries}},
	}
	before := arcset.NewCASTarget(oldPayload)
	after := arcset.NewCASTarget(newPayload)
	intent := mutation.SemanticIntent{
		Profile: mutation.SemanticIntentProfile, BaseRoot: oldRoot, TopOutputID: "root-output",
		Transitions: []mutation.IntentTransition{{
			ID: "root-output", ObjectID: "root", OldRoot: oldRoot, Kind: arcset.KindMap, Backend: maltcid.BackendKindKZG,
			Changes: []mutation.IntentChange{{Coordinate: coordinate, Before: &before, After: &after}},
		}},
	}
	runtime, err := clientwriter.NewRuntime(materializermemory.New(true), map[maltcid.BackendKind]commitment.IndexCommitment{maltcid.BackendKindKZG: scheme})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := runtime.VerifyUpdateView(ctx, view)
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.ComputeBundle(ctx, "transport-fixture", verified, intent)
	if err != nil {
		t.Fatal(err)
	}
	return view, result
}

func clientRootRawCID(t *testing.T, value string) cid.Cid {
	t.Helper()
	hash, err := mh.Sum([]byte(value), mh.SHA2_256, -1)
	if err != nil {
		t.Fatal(err)
	}
	return cid.NewCidV1(cid.Raw, hash)
}

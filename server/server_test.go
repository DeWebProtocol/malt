package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/core/arctable/versioned"
	casmock "github.com/dewebprotocol/malt/core/cas/mock"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/types/prooflist"
	"github.com/dewebprotocol/malt/httpapi"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestServerHealthAndBucketLifecycle(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/health")
	if err != nil {
		t.Fatalf("health request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var health httpapi.HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("decode health response: %v", err)
	}
	if health.Status != "ok" {
		t.Fatalf("health status payload = %q, want %q", health.Status, "ok")
	}

	createBody, err := json.Marshal(&httpapi.BucketCreateRequest{ID: "demo"})
	if err != nil {
		t.Fatalf("marshal create bucket request: %v", err)
	}
	resp, err = http.Post(ts.URL+"/api/v1/buckets", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create bucket request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create bucket status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read bucket response: %v", err)
	}
	if bytes.Contains(body, []byte(`"root":"b"`)) {
		t.Fatalf("bucket response leaked cid.Undef serialization: %s", string(body))
	}

	var bucketResp httpapi.BucketResponse
	if err := json.Unmarshal(body, &bucketResp); err != nil {
		t.Fatalf("decode bucket response: %v", err)
	}
	if bucketResp.Bucket == nil || bucketResp.Bucket.ID != "demo" {
		t.Fatalf("bucket response = %+v, want id demo", bucketResp.Bucket)
	}
	if bucketResp.Bucket.Root != "" {
		t.Fatalf("bucket root = %q, want empty for undefined head", bucketResp.Bucket.Root)
	}
}

func TestServerLegacyGraphRoutesRemoved(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/v1/graphs")
	if err != nil {
		t.Fatalf("legacy graphs request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("legacy graphs status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestServerRootCreateResolveAndVerify(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	target := fakeCIDString("alice")
	createBody, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"name": target}),
	})
	if err != nil {
		t.Fatalf("marshal create structure request: %v", err)
	}

	resp, err := http.Post(ts.URL+"/api/v1/roots", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create structure status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create structure response: %v", err)
	}
	if createResp.Root == "" {
		t.Fatal("expected non-empty root")
	}

	resp, err = http.Get(ts.URL + "/api/v1/roots/" + createResp.Root + "/resolve?path=name")
	if err != nil {
		t.Fatalf("resolve request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var resolveResp httpapi.ResolveResponse
	if err := json.NewDecoder(resp.Body).Decode(&resolveResp); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	if resolveResp.Target != target {
		t.Fatalf("resolved target = %q, want %q", resolveResp.Target, target)
	}

	verifyBody, err := json.Marshal(&httpapi.VerifyRequest{
		Root:       createResp.Root,
		Transcript: transcriptToVerifySteps(resolveResp.Transcript),
	})
	if err != nil {
		t.Fatalf("marshal verify request: %v", err)
	}

	resp, err = http.Post(ts.URL+"/api/v1/verify", "application/json", bytes.NewReader(verifyBody))
	if err != nil {
		t.Fatalf("verify request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("verify status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var verifyResp httpapi.VerifyResponse
	if err := json.NewDecoder(resp.Body).Decode(&verifyResp); err != nil {
		t.Fatalf("decode verify response: %v", err)
	}
	if !verifyResp.Valid {
		t.Fatal("expected transcript verification to succeed")
	}
}

func TestServerProofListReadEndpoints(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBucketBody, err := json.Marshal(&httpapi.BucketCreateRequest{ID: "demo"})
	if err != nil {
		t.Fatalf("marshal create bucket request: %v", err)
	}
	resp, err := http.Post(ts.URL+"/api/v1/buckets", "application/json", bytes.NewReader(createBucketBody))
	if err != nil {
		t.Fatalf("create bucket request failed: %v", err)
	}
	resp.Body.Close()

	target := fakeCIDString("prooflist-target")
	payload := fakeCIDString("prooflist-payload")
	createBody, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: map[string]string{
			"@payload": payload,
			"name":     target,
		},
	})
	if err != nil {
		t.Fatalf("marshal create structure request: %v", err)
	}
	resp, err = http.Post(ts.URL+"/api/v1/buckets/demo/structure", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create bucket structure request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create bucket structure status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()

	resp, err = http.Get(ts.URL + "/api/v1/buckets/demo/prooflist?path=name")
	if err != nil {
		t.Fatalf("bucket prooflist request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("bucket prooflist status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var bucketResp httpapi.ProofListResponse
	if err := json.NewDecoder(resp.Body).Decode(&bucketResp); err != nil {
		t.Fatalf("decode bucket prooflist response: %v", err)
	}
	resp.Body.Close()
	if bucketResp.Target != target {
		t.Fatalf("bucket prooflist target = %q, want %q", bucketResp.Target, target)
	}
	if len(bucketResp.ProofList.Steps) == 0 {
		t.Fatal("expected non-empty bucket prooflist")
	}

	rootPayload := fakeCIDString("root-prooflist-payload")
	createRootBody, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{
			"@payload": rootPayload,
		}),
	})
	if err != nil {
		t.Fatalf("marshal create root structure request: %v", err)
	}
	resp, err = http.Post(ts.URL+"/api/v1/roots", "application/json", bytes.NewReader(createRootBody))
	if err != nil {
		t.Fatalf("create root structure request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create root structure status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var rootCreateResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&rootCreateResp); err != nil {
		t.Fatalf("decode root create response: %v", err)
	}
	resp.Body.Close()

	resp, err = http.Get(ts.URL + "/api/v1/roots/" + rootCreateResp.Root + "/prooflist")
	if err != nil {
		t.Fatalf("root prooflist request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("root prooflist status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var rootResp httpapi.ProofListResponse
	if err := json.NewDecoder(resp.Body).Decode(&rootResp); err != nil {
		t.Fatalf("decode root prooflist response: %v", err)
	}
	resp.Body.Close()
	if rootResp.Target != rootPayload {
		t.Fatalf("root prooflist target = %q, want %q", rootResp.Target, rootPayload)
	}
	if len(rootResp.ProofList.Steps) != 1 {
		t.Fatalf("root prooflist steps = %d, want 1", len(rootResp.ProofList.Steps))
	}
	if rootResp.ProofList.Steps[0].Kind != prooflist.KindPayloadBinding {
		t.Fatalf("root prooflist step kind = %q, want %q", rootResp.ProofList.Steps[0].Kind, prooflist.KindPayloadBinding)
	}
}

func TestServerManagedBucketCreateCanonicalizesArcCount(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBucketBody, err := json.Marshal(&httpapi.BucketCreateRequest{ID: "demo"})
	if err != nil {
		t.Fatalf("marshal create bucket request: %v", err)
	}
	resp, err := http.Post(ts.URL+"/api/v1/buckets", "application/json", bytes.NewReader(createBucketBody))
	if err != nil {
		t.Fatalf("create bucket request failed: %v", err)
	}
	resp.Body.Close()

	target := fakeCIDString("canonical-target")
	createStructureBody, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{
			"foo/bar":   target,
			"/foo//bar": target,
		}),
	})
	if err != nil {
		t.Fatalf("marshal create structure request: %v", err)
	}

	resp, err = http.Post(ts.URL+"/api/v1/buckets/demo/structure", "application/json", bytes.NewReader(createStructureBody))
	if err != nil {
		t.Fatalf("create structure request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create structure status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	resp, err = http.Get(ts.URL + "/api/v1/buckets/demo")
	if err != nil {
		t.Fatalf("get bucket request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get graph status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var bucketResp httpapi.BucketResponse
	if err := json.NewDecoder(resp.Body).Decode(&bucketResp); err != nil {
		t.Fatalf("decode bucket response: %v", err)
	}
	if bucketResp.Bucket == nil {
		t.Fatal("expected bucket payload")
	}
	if bucketResp.Bucket.ArcCount != 2 {
		t.Fatalf("bucket arc_count = %d, want 2 after canonicalization and mandatory payload", bucketResp.Bucket.ArcCount)
	}
}

func TestServerBucketHeadSet_ExpectedOldRoot(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	// Create bucket.
	createBucketBody, err := json.Marshal(&httpapi.BucketCreateRequest{ID: "demo"})
	if err != nil {
		t.Fatalf("marshal create bucket request: %v", err)
	}
	resp, err := http.Post(ts.URL+"/api/v1/buckets", "application/json", bytes.NewReader(createBucketBody))
	if err != nil {
		t.Fatalf("create bucket request failed: %v", err)
	}
	resp.Body.Close()

	createMapBody, err := json.Marshal(&httpapi.BucketMapCreateRequest{
		Bindings: withPayloadBinding(map[string]string{"file.txt": fakeCIDString("bucket-file")}),
	})
	if err != nil {
		t.Fatalf("marshal create map request: %v", err)
	}
	resp, err = http.Post(ts.URL+"/api/v1/buckets/demo/maps", "application/json", bytes.NewReader(createMapBody))
	if err != nil {
		t.Fatalf("create map request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create map status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var mapResp httpapi.BucketMapCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&mapResp); err != nil {
		t.Fatalf("decode create map response: %v", err)
	}
	resp.Body.Close()
	if mapResp.Root == "" {
		t.Fatal("expected non-empty map root")
	}

	// Set head without expected_old_root.
	newRoot := mapResp.Root
	setBody, err := json.Marshal(&httpapi.BucketHeadSetRequest{NewRoot: newRoot, ArcCount: 2})
	if err != nil {
		t.Fatalf("marshal head set request: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/v1/buckets/demo/head", bytes.NewReader(setBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("set head request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("set head status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	// Get bucket and verify head advanced.
	resp, err = http.Get(ts.URL + "/api/v1/buckets/demo")
	if err != nil {
		t.Fatalf("get bucket request failed: %v", err)
	}
	defer resp.Body.Close()
	var getResp httpapi.BucketResponse
	if err := json.NewDecoder(resp.Body).Decode(&getResp); err != nil {
		t.Fatalf("decode bucket response: %v", err)
	}
	if getResp.Bucket == nil || getResp.Bucket.Root != newRoot {
		t.Fatalf("bucket root = %q, want %q", getResp.Bucket.Root, newRoot)
	}
	if getResp.Bucket.ArcCount != 2 {
		t.Fatalf("bucket arc_count = %d, want 2", getResp.Bucket.ArcCount)
	}

	// Non-map roots must be rejected.
	listBody, err := json.Marshal(&httpapi.BucketListCreateRequest{
		Chunks:    []string{fakeCIDString("chunk-a")},
		ChunkSize: 262144,
	})
	if err != nil {
		t.Fatalf("marshal list request: %v", err)
	}
	resp, err = http.Post(ts.URL+"/api/v1/buckets/demo/lists", "application/json", bytes.NewReader(listBody))
	if err != nil {
		t.Fatalf("create list request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create list status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var listResp httpapi.BucketListStatResponse
	if err := json.NewDecoder(resp.Body).Decode(&listResp); err != nil {
		t.Fatalf("decode list create response: %v", err)
	}
	resp.Body.Close()

	for _, invalidRoot := range []string{fakeCIDString("raw-root"), listResp.Root} {
		invalidBody, err := json.Marshal(&httpapi.BucketHeadSetRequest{NewRoot: invalidRoot, ArcCount: 1})
		if err != nil {
			t.Fatalf("marshal invalid head set request: %v", err)
		}
		req, _ = http.NewRequest(http.MethodPut, ts.URL+"/api/v1/buckets/demo/head", bytes.NewReader(invalidBody))
		req.Header.Set("Content-Type", "application/json")
		resp, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("invalid head set request failed: %v", err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("invalid head set status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
		}
	}

	// Now try setting with stale expected_old_root and ensure conflict.
	secondMapBody, err := json.Marshal(&httpapi.BucketMapCreateRequest{
		Bindings: withPayloadBinding(map[string]string{"other.txt": fakeCIDString("other-file")}),
	})
	if err != nil {
		t.Fatalf("marshal second map request: %v", err)
	}
	resp, err = http.Post(ts.URL+"/api/v1/buckets/demo/maps", "application/json", bytes.NewReader(secondMapBody))
	if err != nil {
		t.Fatalf("create second map request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create second map status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var secondMapResp httpapi.BucketMapCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&secondMapResp); err != nil {
		t.Fatalf("decode second map response: %v", err)
	}
	resp.Body.Close()

	staleBody, err := json.Marshal(&httpapi.BucketHeadSetRequest{
		NewRoot:         secondMapResp.Root,
		ArcCount:        1,
		ExpectedOldRoot: fakeCIDString("stale"),
	})
	if err != nil {
		t.Fatalf("marshal stale head set request: %v", err)
	}
	req, _ = http.NewRequest(http.MethodPut, ts.URL+"/api/v1/buckets/demo/head", bytes.NewReader(staleBody))
	req.Header.Set("Content-Type", "application/json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("stale head set request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale expected_old_root status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}
}

func TestServerBucketScopedMapAndListAPIs(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBucketBody, err := json.Marshal(&httpapi.BucketCreateRequest{ID: "demo"})
	if err != nil {
		t.Fatalf("marshal create bucket request: %v", err)
	}
	resp, err := http.Post(ts.URL+"/api/v1/buckets", "application/json", bytes.NewReader(createBucketBody))
	if err != nil {
		t.Fatalf("create bucket request failed: %v", err)
	}
	resp.Body.Close()

	target := fakeCIDString("bucket-map-target")
	createMapBody, err := json.Marshal(&httpapi.BucketMapCreateRequest{
		Bindings: withPayloadBinding(map[string]string{"docs/readme.md": target}),
	})
	if err != nil {
		t.Fatalf("marshal create map request: %v", err)
	}
	resp, err = http.Post(ts.URL+"/api/v1/buckets/demo/maps", "application/json", bytes.NewReader(createMapBody))
	if err != nil {
		t.Fatalf("create map request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create map status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var createMapResp httpapi.BucketMapCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&createMapResp); err != nil {
		t.Fatalf("decode create map response: %v", err)
	}
	resp.Body.Close()
	if createMapResp.Root == "" {
		t.Fatal("expected non-empty map root")
	}

	resp, err = http.Get(ts.URL + "/api/v1/buckets/demo/maps/" + createMapResp.Root + "/resolve?path=docs/readme.md")
	if err != nil {
		t.Fatalf("resolve map request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("resolve map status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var resolveResp httpapi.BucketMapResolveResponse
	if err := json.NewDecoder(resp.Body).Decode(&resolveResp); err != nil {
		t.Fatalf("decode map resolve response: %v", err)
	}
	resp.Body.Close()
	if resolveResp.Key != target {
		t.Fatalf("map resolve key = %q, want %q", resolveResp.Key, target)
	}

	resp, err = http.Get(ts.URL + "/api/v1/buckets/demo/maps/" + createMapResp.Root + "/resolve?path=missing")
	if err != nil {
		t.Fatalf("resolve missing map request failed: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("resolve missing map status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}

	chunk1 := fakeCIDString("chunk1")
	chunk2 := fakeCIDString("chunk2")
	createListBody, err := json.Marshal(&httpapi.BucketListCreateRequest{
		Chunks:    []string{chunk1, chunk2},
		ChunkSize: 262144,
	})
	if err != nil {
		t.Fatalf("marshal create list request: %v", err)
	}
	resp, err = http.Post(ts.URL+"/api/v1/buckets/demo/lists", "application/json", bytes.NewReader(createListBody))
	if err != nil {
		t.Fatalf("create list request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create list status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var createListResp httpapi.BucketListStatResponse
	if err := json.NewDecoder(resp.Body).Decode(&createListResp); err != nil {
		t.Fatalf("decode list create response: %v", err)
	}
	resp.Body.Close()
	if createListResp.Root == "" || createListResp.ChunkCount != 2 || createListResp.ChunkSize != 262144 {
		t.Fatalf("unexpected list create response: %+v", createListResp)
	}

	resp, err = http.Get(ts.URL + "/api/v1/buckets/demo/lists/" + createListResp.Root)
	if err != nil {
		t.Fatalf("get list request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get list status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var statResp httpapi.BucketListStatResponse
	if err := json.NewDecoder(resp.Body).Decode(&statResp); err != nil {
		t.Fatalf("decode list get response: %v", err)
	}
	resp.Body.Close()
	if statResp.ChunkCount != 2 || statResp.ChunkSize != 262144 {
		t.Fatalf("unexpected list stat response: %+v", statResp)
	}
}

func TestServerBucketSemanticMutationUpdatesHead(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBucketBody, _ := json.Marshal(&httpapi.BucketCreateRequest{ID: "demo"})
	resp, err := http.Post(ts.URL+"/api/v1/buckets", "application/json", bytes.NewReader(createBucketBody))
	if err != nil {
		t.Fatalf("create bucket request failed: %v", err)
	}
	resp.Body.Close()

	initialPayload := fakeCIDString("initial-payload")
	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: map[string]string{
			"@payload": initialPayload,
			"name":     fakeCIDString("initial-name"),
		},
	})
	resp, err = http.Post(ts.URL+"/api/v1/buckets/demo/structure", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create structure status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()

	nextPayload := fakeCIDString("next-payload")
	nextName := fakeCIDString("next-name")
	mutationBody, _ := json.Marshal(&httpapi.BucketSemanticMutationRequest{
		Puts: []httpapi.SemanticMutationPut{{
			Object: createResp.Root,
			Kind:   "map",
			Entries: []httpapi.SemanticMutationEntry{
				{Path: "@payload", Target: nextPayload},
				{Path: "name", Target: nextName},
			},
		}},
	})
	resp, err = http.Post(ts.URL+"/api/v1/buckets/demo/semantic-mutations", "application/json", bytes.NewReader(mutationBody))
	if err != nil {
		t.Fatalf("semantic mutation request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("semantic mutation status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var mutationResp httpapi.BucketSemanticMutationResponse
	if err := json.NewDecoder(resp.Body).Decode(&mutationResp); err != nil {
		t.Fatalf("decode semantic mutation response: %v", err)
	}
	resp.Body.Close()
	if mutationResp.Bucket != "demo" {
		t.Fatalf("bucket = %q, want demo", mutationResp.Bucket)
	}
	if mutationResp.BaseRoot != createResp.Root {
		t.Fatalf("base_root = %q, want %q", mutationResp.BaseRoot, createResp.Root)
	}
	if mutationResp.NewRoot == "" || mutationResp.NewRoot == createResp.Root {
		t.Fatalf("new_root = %q, want a new defined root", mutationResp.NewRoot)
	}
	if mutationResp.PutCount != 1 || mutationResp.ArcCount != 2 {
		t.Fatalf("receipt counts = puts %d arcs %d, want 1/2", mutationResp.PutCount, mutationResp.ArcCount)
	}

	resp, err = http.Get(ts.URL + "/api/v1/buckets/demo")
	if err != nil {
		t.Fatalf("get bucket request failed: %v", err)
	}
	var bucketResp httpapi.BucketResponse
	if err := json.NewDecoder(resp.Body).Decode(&bucketResp); err != nil {
		t.Fatalf("decode bucket response: %v", err)
	}
	resp.Body.Close()
	if bucketResp.Bucket.Root != mutationResp.NewRoot || bucketResp.Bucket.ArcCount != 2 {
		t.Fatalf("bucket root=%q arcs=%d, want root=%q arcs=2", bucketResp.Bucket.Root, bucketResp.Bucket.ArcCount, mutationResp.NewRoot)
	}

	resp, err = http.Get(ts.URL + "/api/v1/buckets/demo/resolve?path=name")
	if err != nil {
		t.Fatalf("resolve request failed: %v", err)
	}
	var resolveResp httpapi.ResolveResponse
	if err := json.NewDecoder(resp.Body).Decode(&resolveResp); err != nil {
		t.Fatalf("decode resolve response: %v", err)
	}
	resp.Body.Close()
	if resolveResp.Target != nextName {
		t.Fatalf("resolved target = %q, want %q", resolveResp.Target, nextName)
	}
}

func TestServerBucketSemanticMutationRejectsInvalidHead(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBucketBody, _ := json.Marshal(&httpapi.BucketCreateRequest{ID: "demo"})
	resp, err := http.Post(ts.URL+"/api/v1/buckets", "application/json", bytes.NewReader(createBucketBody))
	if err != nil {
		t.Fatalf("create bucket request failed: %v", err)
	}
	resp.Body.Close()

	createBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"name": fakeCIDString("initial-name")}),
	})
	resp, err = http.Post(ts.URL+"/api/v1/buckets/demo/structure", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create structure request failed: %v", err)
	}
	var createResp httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&createResp); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	resp.Body.Close()

	index := uint64(0)
	tests := []struct {
		name string
		req  httpapi.BucketSemanticMutationRequest
	}{
		{
			name: "list only root",
			req: httpapi.BucketSemanticMutationRequest{
				Puts: []httpapi.SemanticMutationPut{{
					Object: createResp.Root,
					Kind:   "list",
					Entries: []httpapi.SemanticMutationEntry{{
						Index:  &index,
						Target: fakeCIDString("chunk"),
					}},
				}},
			},
		},
		{
			name: "map missing payload",
			req: httpapi.BucketSemanticMutationRequest{
				Puts: []httpapi.SemanticMutationPut{{
					Object: createResp.Root,
					Kind:   "map",
					Entries: []httpapi.SemanticMutationEntry{{
						Path:   "name",
						Target: fakeCIDString("next-name"),
					}},
				}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, _ := json.Marshal(&tt.req)
			resp, err := http.Post(ts.URL+"/api/v1/buckets/demo/semantic-mutations", "application/json", bytes.NewReader(body))
			if err != nil {
				t.Fatalf("semantic mutation request failed: %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Fatalf("semantic mutation status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
			}

			resp, err = http.Get(ts.URL + "/api/v1/buckets/demo")
			if err != nil {
				t.Fatalf("get bucket request failed: %v", err)
			}
			var bucketResp httpapi.BucketResponse
			if err := json.NewDecoder(resp.Body).Decode(&bucketResp); err != nil {
				t.Fatalf("decode bucket response: %v", err)
			}
			resp.Body.Close()
			if bucketResp.Bucket.Root != createResp.Root {
				t.Fatalf("bucket root changed to %q, want %q", bucketResp.Bucket.Root, createResp.Root)
			}
		})
	}
}

func TestServerUnixFSWritesPublishGatewayReadableRoot(t *testing.T) {
	node := newTestNode(t)

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBucketBody, _ := json.Marshal(&httpapi.BucketCreateRequest{ID: "demo"})
	resp, err := http.Post(ts.URL+"/api/v1/buckets", "application/json", bytes.NewReader(createBucketBody))
	if err != nil {
		t.Fatalf("create bucket request failed: %v", err)
	}
	resp.Body.Close()

	resp, err = http.Post(ts.URL+"/api/v1/buckets/demo/unixfs/directories?path=docs", "application/json", nil)
	if err != nil {
		t.Fatalf("create unixfs directory request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create unixfs directory status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	resp.Body.Close()

	fileBody := []byte("hello from gateway unixfs")
	resp, err = http.Post(ts.URL+"/api/v1/buckets/demo/unixfs/files?path=docs/readme.txt", "application/octet-stream", bytes.NewReader(fileBody))
	if err != nil {
		t.Fatalf("create unixfs file request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create unixfs file status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var writeResp httpapi.BucketUnixFSWriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&writeResp); err != nil {
		t.Fatalf("decode unixfs write response: %v", err)
	}
	resp.Body.Close()
	if writeResp.Bucket != "demo" || writeResp.Path != "docs/readme.txt" || writeResp.Kind != "file" {
		t.Fatalf("unexpected unixfs write response: %+v", writeResp)
	}
	if writeResp.NewRoot == "" || writeResp.ArcCount == 0 {
		t.Fatalf("unixfs write root=%q arc_count=%d, want defined", writeResp.NewRoot, writeResp.ArcCount)
	}

	resp, err = http.Get(ts.URL + "/api/v1/buckets/demo")
	if err != nil {
		t.Fatalf("get bucket request failed: %v", err)
	}
	var bucketResp httpapi.BucketResponse
	if err := json.NewDecoder(resp.Body).Decode(&bucketResp); err != nil {
		t.Fatalf("decode bucket response: %v", err)
	}
	resp.Body.Close()
	if bucketResp.Bucket.Root != writeResp.NewRoot || bucketResp.Bucket.ArcCount != writeResp.ArcCount {
		t.Fatalf("bucket root=%q arcs=%d, want root=%q arcs=%d", bucketResp.Bucket.Root, bucketResp.Bucket.ArcCount, writeResp.NewRoot, writeResp.ArcCount)
	}

	rootCID, err := cid.Decode(writeResp.NewRoot)
	if err != nil {
		t.Fatalf("decode write root: %v", err)
	}
	if payload, err := node.ArcTable().Get(t.Context(), "demo", rootCID, arcset.CanonicalizePath("@payload")); err != nil || !payload.Defined() {
		t.Fatalf("root @payload from arctable = %s, err %v; want defined", payload, err)
	}

	resp, err = http.Get(ts.URL + "/api/v1/buckets/demo/prooflist?path=docs/readme.txt")
	if err != nil {
		t.Fatalf("prooflist request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("prooflist status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var proofResp httpapi.ProofListResponse
	if err := json.NewDecoder(resp.Body).Decode(&proofResp); err != nil {
		t.Fatalf("decode prooflist response: %v", err)
	}
	resp.Body.Close()
	if proofResp.Target == "" || len(proofResp.ProofList.Steps) == 0 {
		t.Fatalf("unexpected prooflist response: %+v", proofResp)
	}

	resp, err = http.Get(ts.URL + "/api/v1/buckets/demo/stat?path=docs/readme.txt")
	if err != nil {
		t.Fatalf("stat request failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stat status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	var statResp httpapi.BucketStatResponse
	if err := json.NewDecoder(resp.Body).Decode(&statResp); err != nil {
		t.Fatalf("decode stat response: %v", err)
	}
	resp.Body.Close()
	if statResp.Kind != "file" || statResp.Size == nil || *statResp.Size != int64(len(fileBody)) {
		t.Fatalf("unexpected stat response: %+v", statResp)
	}

	resp, err = http.Get(ts.URL + "/api/v1/buckets/demo/content?path=docs/readme.txt")
	if err != nil {
		t.Fatalf("content request failed: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !bytes.Equal(got, fileBody) {
		t.Fatalf("content status/body = %d %q, want %d %q", resp.StatusCode, string(got), http.StatusOK, string(fileBody))
	}
}

func TestServerUnixFSGatewayRootDoesNotSelfParent(t *testing.T) {
	node := newTestNode(t)
	arcs, ok := node.ArcTable().(*versioned.ArcTable)
	if !ok {
		t.Fatalf("test node ArcTable = %T, want *versioned.ArcTable", node.ArcTable())
	}

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	createBucketBody, _ := json.Marshal(&httpapi.BucketCreateRequest{ID: "demo"})
	resp, err := http.Post(ts.URL+"/api/v1/buckets", "application/json", bytes.NewReader(createBucketBody))
	if err != nil {
		t.Fatalf("create bucket request failed: %v", err)
	}
	resp.Body.Close()

	resp, err = http.Post(ts.URL+"/api/v1/buckets/demo/unixfs/files?path=readme.txt", "application/octet-stream", bytes.NewReader([]byte("hello")))
	if err != nil {
		t.Fatalf("create unixfs file request failed: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create unixfs file status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var writeResp httpapi.BucketUnixFSWriteResponse
	if err := json.NewDecoder(resp.Body).Decode(&writeResp); err != nil {
		t.Fatalf("decode unixfs write response: %v", err)
	}
	resp.Body.Close()

	root, err := cid.Decode(writeResp.NewRoot)
	if err != nil {
		t.Fatalf("decode unixfs write root: %v", err)
	}
	parent, err := arcs.GetParent(t.Context(), "demo", root)
	if err != nil {
		t.Fatalf("read gateway root parent: %v", err)
	}
	if parent.Equals(root) {
		t.Fatalf("gateway root self-parented: %s", root)
	}
}

func TestServerBucketStatAndContentContracts(t *testing.T) {
	node := newTestNode(t)
	mockCAS, ok := node.CAS().(*casmock.CAS)
	if !ok {
		t.Fatal("expected mock CAS")
	}

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	// Create bucket.
	createBucketBody, _ := json.Marshal(&httpapi.BucketCreateRequest{ID: "demo"})
	resp, err := http.Post(ts.URL+"/api/v1/buckets", "application/json", bytes.NewReader(createBucketBody))
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	resp.Body.Close()

	// Prepare a raw file and a list-backed file in CAS.
	rawData := []byte("hello raw")
	rawCID, _ := fakeCID(rawData)
	mockCAS.AddBlock(rawCID, rawData)

	chunk1 := bytes.Repeat([]byte{'a'}, 262144)
	chunk2 := []byte("ef")
	chunk1CID, _ := fakeCID(chunk1)
	chunk2CID, _ := fakeCID(chunk2)
	mockCAS.AddBlock(chunk1CID, chunk1)
	mockCAS.AddBlock(chunk2CID, chunk2)

	createListBody, _ := json.Marshal(&httpapi.BucketListCreateRequest{
		Chunks:    []string{chunk1CID.String(), chunk2CID.String()},
		ChunkSize: 262144,
	})
	resp, err = http.Post(ts.URL+"/api/v1/buckets/demo/lists", "application/json", bytes.NewReader(createListBody))
	if err != nil {
		t.Fatalf("create list: %v", err)
	}
	var listResp httpapi.BucketListStatResponse
	_ = json.NewDecoder(resp.Body).Decode(&listResp)
	resp.Body.Close()

	// Create bucket head bindings.
	rootManifest := []byte(`{"entries":["large.bin","raw.txt"]}`)
	rootManifestCID, _ := fakeCID(rootManifest)
	mockCAS.AddBlock(rootManifestCID, rootManifest)
	createMapBody, _ := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: map[string]string{
			"@payload":  rootManifestCID.String(),
			"raw.txt":   rawCID.String(),
			"large.bin": listResp.Root,
		},
	})
	resp, err = http.Post(ts.URL+"/api/v1/buckets/demo/structure", "application/json", bytes.NewReader(createMapBody))
	if err != nil {
		t.Fatalf("create structure: %v", err)
	}
	resp.Body.Close()

	// stat raw file
	resp, err = http.Get(ts.URL + "/api/v1/buckets/demo/stat?path=/raw.txt")
	if err != nil {
		t.Fatalf("stat raw: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("stat raw status = %d", resp.StatusCode)
	}
	var rawStat httpapi.BucketStatResponse
	_ = json.NewDecoder(resp.Body).Decode(&rawStat)
	resp.Body.Close()
	if rawStat.Kind != "file" || rawStat.StorageKind != "raw" || rawStat.Size == nil || *rawStat.Size != int64(len(rawData)) {
		t.Fatalf("unexpected raw stat: %+v", rawStat)
	}

	// stat list file
	resp, err = http.Get(ts.URL + "/api/v1/buckets/demo/stat?path=large.bin")
	if err != nil {
		t.Fatalf("stat list: %v", err)
	}
	var listStat httpapi.BucketStatResponse
	_ = json.NewDecoder(resp.Body).Decode(&listStat)
	resp.Body.Close()
	if listStat.Kind != "file" || listStat.StorageKind != "list" || listStat.Size == nil || *listStat.Size != int64(len(chunk1)+len(chunk2)) {
		t.Fatalf("unexpected list stat: %+v", listStat)
	}

	// content raw full
	resp, err = http.Get(ts.URL + "/api/v1/buckets/demo/content?path=raw.txt")
	if err != nil {
		t.Fatalf("content raw: %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != string(rawData) {
		t.Fatalf("unexpected raw content status/body: %d %q", resp.StatusCode, string(body))
	}

	// content raw range
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/v1/buckets/demo/content?path=raw.txt", nil)
	req.Header.Set("Range", "bytes=0-4")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("content raw range: %v", err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusPartialContent || string(body) != "hello" {
		t.Fatalf("unexpected raw range status/body: %d %q", resp.StatusCode, string(body))
	}

	// missing path => 404
	resp, err = http.Get(ts.URL + "/api/v1/buckets/demo/stat?path=missing")
	if err != nil {
		t.Fatalf("stat missing: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing stat status = %d", resp.StatusCode)
	}
}

func newTestNode(t *testing.T) *api.Node {
	t.Helper()

	cfg := config.DefaultConfig()
	cfg.State.RootDir = t.TempDir()
	cfg.State.KVStore.Type = "badger"
	cfg.State.KVStore.Path = filepath.Join(cfg.State.RootDir, "kv")
	cfg.CAS.Mode = "mock"

	node, err := api.NewNode(api.WithConfig(cfg))
	if err != nil {
		t.Fatalf("create test node: %v", err)
	}
	t.Cleanup(func() {
		_ = node.Close()
	})
	return node
}

func transcriptToVerifySteps(steps []httpapi.StepEvidence) []httpapi.VerifyStep {
	out := make([]httpapi.VerifyStep, len(steps))
	for i, step := range steps {
		out[i] = httpapi.VerifyStep{
			Path:     step.Path,
			Target:   step.Target,
			Evidence: step.Evidence,
			Kind:     step.Kind,
		}
	}
	return out
}

func fakeCIDString(seed string) string {
	sum, err := mh.Sum([]byte(seed), mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cid.Raw, sum).String()
}

func withPayloadBinding(bindings map[string]string) map[string]string {
	out := make(map[string]string, len(bindings)+1)
	for path, target := range bindings {
		out[path] = target
	}
	if _, ok := out["@payload"]; !ok {
		out["@payload"] = fakeCIDString("payload")
	}
	return out
}

func fakeCID(data []byte) (cid.Cid, error) {
	sum, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, sum), nil
}

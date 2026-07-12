package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	httpapi "github.com/dewebprotocol/malt/api/http"
	"github.com/dewebprotocol/malt/artifact"
	casmock "github.com/dewebprotocol/malt/storage/cas/mock"
)

func TestArtifactResolveProveVerify(t *testing.T) {
	node := newTestNode(t)
	mockCAS, ok := node.CAS().(*casmock.CAS)
	if !ok {
		t.Fatal("expected mock CAS")
	}

	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	targetCID, err := mockCAS.Put(t.Context(), []byte("artifact target"))
	if err != nil {
		t.Fatalf("put target: %v", err)
	}
	createBody, err := json.Marshal(&httpapi.CreateStructureRequest{
		Arcs: withPayloadBinding(map[string]string{"name": targetCID.String(), ".": targetCID.String()}),
	})
	if err != nil {
		t.Fatalf("marshal create request: %v", err)
	}
	resp, err := http.Post(ts.URL+"/_", "application/json", bytes.NewReader(createBody))
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create root status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
	var created httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	resolved := postArtifactRequest[artifact.ResolveRequest, artifact.Artifact](t, ts.URL+"/v1/artifacts/resolve", artifact.ResolveRequest{
		Profile:  artifact.Profile,
		Root:     created.Root,
		Segments: []string{"name"},
	})
	if resolved.Operation != artifact.OperationResolve {
		t.Fatalf("resolve operation = %q, want %q", resolved.Operation, artifact.OperationResolve)
	}
	if resolved.Target != targetCID.String() {
		t.Fatalf("resolve target = %q, want %q", resolved.Target, targetCID)
	}
	verified := postArtifactRequest[artifact.VerifyRequest, artifact.VerifyResult](t, ts.URL+"/v1/artifacts/verify", artifact.VerifyRequest{
		Profile:  artifact.Profile,
		Artifact: resolved,
	})
	if !verified.Valid {
		t.Fatal("resolve artifact did not verify")
	}
	dotResolved := postArtifactRequest[artifact.ResolveRequest, artifact.Artifact](t, ts.URL+"/v1/artifacts/resolve", artifact.ResolveRequest{
		Profile:  artifact.Profile,
		Root:     created.Root,
		Segments: []string{"."},
	})
	if dotResolved.Target != targetCID.String() {
		t.Fatalf("dot-coordinate target = %q, want %q", dotResolved.Target, targetCID)
	}
	dotVerified := postArtifactRequest[artifact.VerifyRequest, artifact.VerifyResult](t, ts.URL+"/v1/artifacts/verify", artifact.VerifyRequest{
		Profile: artifact.Profile, Artifact: dotResolved,
	})
	if !dotVerified.Valid {
		t.Fatal("dot-coordinate artifact did not verify")
	}

	proved := postArtifactRequest[artifact.ProveRequest, artifact.Artifact](t, ts.URL+"/v1/artifacts/prove", artifact.ProveRequest{
		Profile: artifact.Profile,
		Root:    created.Root,
		Query: artifact.Query{
			Kind:     artifact.QueryMapKey,
			Segments: []string{"name"},
		},
	})
	if proved.Operation != artifact.OperationProve {
		t.Fatalf("prove operation = %q, want %q", proved.Operation, artifact.OperationProve)
	}
	verified = postArtifactRequest[artifact.VerifyRequest, artifact.VerifyResult](t, ts.URL+"/v1/artifacts/verify", artifact.VerifyRequest{
		Profile:  artifact.Profile,
		Artifact: proved,
	})
	if !verified.Valid {
		t.Fatal("prove artifact did not verify")
	}

	resolved.Target = created.Root
	verified = postArtifactRequest[artifact.VerifyRequest, artifact.VerifyResult](t, ts.URL+"/v1/artifacts/verify", artifact.VerifyRequest{
		Profile:  artifact.Profile,
		Artifact: resolved,
	})
	if verified.Valid {
		t.Fatal("tampered artifact verified")
	}
}

func TestArtifactResolveRootIdentity(t *testing.T) {
	node := newTestNode(t)
	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	mockCAS := node.CAS().(*casmock.CAS)
	targetCID, err := mockCAS.Put(t.Context(), []byte("identity target"))
	if err != nil {
		t.Fatal(err)
	}
	created := postCreateStructure(t, ts.URL, withPayloadBinding(map[string]string{"name": targetCID.String()}))
	resolved := postArtifactRequest[artifact.ResolveRequest, artifact.Artifact](t, ts.URL+"/v1/artifacts/resolve", artifact.ResolveRequest{
		Profile: artifact.Profile,
		Root:    created.Root,
	})
	if resolved.Target != created.Root || len(resolved.ProofList.Steps) != 0 {
		t.Fatalf("identity artifact = %+v", resolved)
	}
	verified := postArtifactRequest[artifact.VerifyRequest, artifact.VerifyResult](t, ts.URL+"/v1/artifacts/verify", artifact.VerifyRequest{
		Profile: artifact.Profile, Artifact: resolved,
	})
	if !verified.Valid {
		t.Fatal("root identity artifact did not verify")
	}
}

func postCreateStructure(t *testing.T, baseURL string, arcs map[string]string) httpapi.CreateStructureResponse {
	t.Helper()
	body, err := json.Marshal(&httpapi.CreateStructureRequest{Arcs: arcs})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(baseURL+"/_", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create root status = %d", resp.StatusCode)
	}
	var created httpapi.CreateStructureResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	return created
}

func postArtifactRequest[Request, Response any](t *testing.T, url string, request Request) Response {
	t.Helper()
	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("post %s status = %d, want %d", url, resp.StatusCode, http.StatusOK)
	}
	var response Response
	if err := json.NewDecoder(resp.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return response
}

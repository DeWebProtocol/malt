package server

import (
	"net/http/httptest"
	"testing"

	"github.com/dewebprotocol/malt/protocol"
	casmock "github.com/dewebprotocol/malt/storage/cas/mock"
)

func TestResolveAndReadContracts(t *testing.T) {
	node := newTestNode(t)
	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	mockCAS := node.CAS().(*casmock.CAS)
	target, err := mockCAS.Put(t.Context(), []byte("protocol target"))
	if err != nil {
		t.Fatal(err)
	}
	created := postCreateStructure(t, ts.URL, withPayloadBinding(map[string]string{"name": target.String(), "@payload": target.String()}))

	resolveRequest := protocol.ResolveRequest{
		Profile:  protocol.ResolveProfile,
		Root:     created.Root,
		Segments: []string{"@payload"},
	}
	resolved := postArtifactRequest[protocol.ResolveRequest, protocol.ResolveResult](t, ts.URL+"/v1/resolve", resolveRequest)
	if resolved.Target != target.String() || resolved.ProofList.Query != "@payload" {
		t.Fatalf("resolve result = %+v", resolved)
	}
	verifiedResolve := postArtifactRequest[protocol.ResolveVerification, protocol.VerificationResult](t, ts.URL+"/v1/verify/resolve", protocol.ResolveVerification{
		Request: resolveRequest,
		Result:  resolved,
	})
	if !verifiedResolve.Valid {
		t.Fatalf("resolve verification = %+v", verifiedResolve)
	}

	readRequest := protocol.ReadRequest{
		Profile: protocol.ReadProfile,
		Root:    created.Root,
		Query: protocol.Query{
			Kind:     protocol.QueryMapKey,
			Segments: []string{"name"},
		},
	}
	read := postArtifactRequest[protocol.ReadRequest, protocol.ReadResult](t, ts.URL+"/v1/read", readRequest)
	if read.Target != target.String() || read.ProofList.Query != "name" {
		t.Fatalf("read result = %+v", read)
	}
	verifiedRead := postArtifactRequest[protocol.ReadVerification, protocol.VerificationResult](t, ts.URL+"/v1/verify/read", protocol.ReadVerification{
		Request: readRequest,
		Result:  read,
	})
	if !verifiedRead.Valid {
		t.Fatalf("read verification = %+v", verifiedRead)
	}
}

func TestResolveContractRootIdentityIsStrict(t *testing.T) {
	node := newTestNode(t)
	ts := httptest.NewServer(New(node, "127.0.0.1:0").Handler())
	defer ts.Close()

	mockCAS := node.CAS().(*casmock.CAS)
	target, err := mockCAS.Put(t.Context(), []byte("identity target"))
	if err != nil {
		t.Fatal(err)
	}
	created := postCreateStructure(t, ts.URL, withPayloadBinding(map[string]string{"name": target.String()}))
	request := protocol.ResolveRequest{Profile: protocol.ResolveProfile, Root: created.Root, Segments: []string{}}
	result := postArtifactRequest[protocol.ResolveRequest, protocol.ResolveResult](t, ts.URL+"/v1/resolve", request)
	if result.Target != created.Root || len(result.ProofList.Steps) != 0 {
		t.Fatalf("identity result = %+v", result)
	}
	verified := postArtifactRequest[protocol.ResolveVerification, protocol.VerificationResult](t, ts.URL+"/v1/verify/resolve", protocol.ResolveVerification{Request: request, Result: result})
	if !verified.Valid {
		t.Fatalf("identity verification = %+v", verified)
	}

	payloadRequest := protocol.ResolveRequest{Profile: protocol.ResolveProfile, Root: created.Root, Segments: []string{"@payload"}}
	payloadResult := postArtifactRequest[protocol.ResolveRequest, protocol.ResolveResult](t, ts.URL+"/v1/resolve", payloadRequest)
	payloadResult.ProofList.Query = ""
	tampered := postArtifactRequest[protocol.ResolveVerification, protocol.VerificationResult](t, ts.URL+"/v1/verify/resolve", protocol.ResolveVerification{Request: request, Result: payloadResult})
	if tampered.Valid {
		t.Fatal("root identity accepted traversal evidence")
	}
}

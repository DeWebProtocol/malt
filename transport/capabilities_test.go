package transport_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	client "github.com/dewebprotocol/malt-client/transport"
	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
	"github.com/dewebprotocol/malt-core/mutation"
)

func TestGatewayHTTPAdapterImplementsTypedMutationCapability(t *testing.T) {
	base := mustBlockCID(t, []byte("capability-base"))
	candidate := mustBlockCID(t, []byte("capability-candidate"))
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/roots/" + base.String() + "/mutations":
			_ = json.NewEncoder(response).Encode(client.SemanticMutationResponse{
				BaseRoot: base.String(), NewRoot: candidate.String(), DeltaCount: 1, ArcCount: 2, MALTObjectCount: 3,
			})
		case "/v1/roots":
			_ = json.NewEncoder(response).Encode(client.CreateStructureResponse{Root: candidate.String()})
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	remote, err := client.New(client.Options{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	result, err := remote.ApplyMutation(t.Context(), mutation.SemanticMutation{BaseRoot: base})
	if err != nil {
		t.Fatal(err)
	}
	if !result.BaseRoot.Equals(base) || !result.CandidateRoot.Equals(candidate) || result.ArcCount != 2 || result.MALTObjectCount != 3 {
		t.Fatalf("mutation result = %#v", result)
	}
	created, err := remote.CreateStructureCandidate(t.Context(), map[string]string{"docs": candidate.String()})
	if err != nil || !created.Equals(candidate) {
		t.Fatalf("structure candidate = %s err=%v", created, err)
	}
}

func TestGatewayHTTPAdapterRejectsInvalidSemanticApplyBeforeRequest(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		calls++
		http.Error(response, "unexpected request", http.StatusInternalServerError)
	}))
	defer server.Close()
	remote, err := client.New(client.Options{
		BaseURL: server.URL, TenantBearerToken: "tenant-secret", BucketID: "dataset-one",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := remote.ApplyCandidate(t.Context(), transportcap.ApplyRequest{}); err == nil {
		t.Fatal("ApplyCandidate accepted an incomplete semantic request")
	}
	if calls != 0 {
		t.Fatalf("invalid semantic request performed %d HTTP calls", calls)
	}
}

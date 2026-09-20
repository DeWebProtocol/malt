package transport_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	client "github.com/dewebprotocol/malt-client/transport"
	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
)

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

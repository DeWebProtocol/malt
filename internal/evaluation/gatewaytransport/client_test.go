package gatewaytransport_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	"github.com/dewebprotocol/malt-client/internal/evaluation/gatewaytransport"
	"github.com/dewebprotocol/malt-client/merkledag"
	"github.com/dewebprotocol/malt-client/transport"
	"github.com/dewebprotocol/malt/auth/arcset"
	materializermemory "github.com/dewebprotocol/malt/auth/arcset/materializer/memory"
	"github.com/dewebprotocol/malt/auth/commitment/kzg"
	"github.com/dewebprotocol/malt/auth/semantic/mapping"
	mappingradix "github.com/dewebprotocol/malt/auth/semantic/mapping/radix"
	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

const testInstanceToken = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestNewRequiresCanonicalTokenAndProtectedOrigin(t *testing.T) {
	for _, test := range []struct {
		name    string
		baseURL string
		token   string
		want    string
	}{
		{name: "invalid token", baseURL: "https://gateway.example", token: "not-a-token", want: "canonical SHA-256"},
		{name: "plaintext remote", baseURL: "http://192.0.2.1:8080", token: testInstanceToken, want: "HTTPS or a loopback"},
		{name: "query is forbidden", baseURL: "https://gateway.example/prefix?secret=true", token: testInstanceToken, want: "without query"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := gatewaytransport.New(gatewaytransport.Options{BaseURL: test.baseURL, InstanceToken: test.token}); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("New error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestInstanceClientSupportsGatewayPrefixAndRejectsOtherTargets(t *testing.T) {
	var prefixRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/gateway/healthz" {
			http.NotFound(response, request)
			return
		}
		prefixRequests.Add(1)
		if request.Header.Get(gatewaytransport.InstanceTokenHeader) != testInstanceToken {
			t.Errorf("instance token = %q", request.Header.Get(gatewaytransport.InstanceTokenHeader))
		}
		_, _ = io.WriteString(response, `{"status":"ok"}`)
	}))
	defer server.Close()
	evaluation := newEvaluationClient(t, server.URL+"/gateway", 0)
	if _, err := evaluation.Health(t.Context()); err != nil {
		t.Fatal(err)
	}

	other := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get(gatewaytransport.InstanceTokenHeader) != "" {
			t.Errorf("other origin received instance token")
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer other.Close()
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, other.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := evaluation.InstanceHTTPClient().Do(request); err == nil || !strings.Contains(err.Error(), "outside configured Gateway base URL") {
		t.Fatalf("cross-origin request error = %v", err)
	}
	if prefixRequests.Load() != 1 {
		t.Fatalf("prefix requests = %d", prefixRequests.Load())
	}
}

func TestInstanceClientRejectsPrefixTraversalAndHostOverride(t *testing.T) {
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		received.Add(1)
		if request.Header.Get(gatewaytransport.InstanceTokenHeader) != "" {
			t.Errorf("escaped request leaked instance token")
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	evaluation := newEvaluationClient(t, server.URL+"/gateway", 0)

	tests := []struct {
		name string
		url  string
		host string
	}{
		{name: "dot traversal", url: server.URL + "/gateway/../outside"},
		{name: "encoded traversal", url: server.URL + "/gateway/%2e%2e/outside"},
		{name: "encoded slash", url: server.URL + "/gateway/%2Foutside"},
		{name: "duplicate slash", url: server.URL + "/gateway//outside"},
		{name: "host override", url: server.URL + "/gateway/healthz", host: "other.example"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, test.url, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Host = test.host
			if _, err := evaluation.InstanceHTTPClient().Do(request); err == nil ||
				!strings.Contains(err.Error(), "outside configured Gateway base URL") {
				t.Fatalf("request error = %v", err)
			}
		})
	}
	if received.Load() != 0 {
		t.Fatalf("prefix escape reached Gateway %d times", received.Load())
	}
}

func TestInstanceClientAndHealthBindEveryRequestToToken(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if got := request.Header.Get(gatewaytransport.InstanceTokenHeader); got != testInstanceToken {
			t.Errorf("instance token = %q", got)
		}
		switch request.URL.Path {
		case "/healthz":
			_, _ = io.WriteString(response, `{"status":"ok","evaluation_instance_token":"`+testInstanceToken+`"}`)
		case "/v1/client-roots":
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	evaluation := newEvaluationClient(t, server.URL, 0)
	health, err := evaluation.Health(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if health.Status != "ok" || health.EvaluationInstanceToken != testInstanceToken {
		t.Fatalf("health = %#v", health)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, server.URL+"/v1/client-roots", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := evaluation.InstanceHTTPClient().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNoContent || requests.Load() != 2 {
		t.Fatalf("generic instance request = status %d requests %d", response.StatusCode, requests.Load())
	}
}

func TestHealthRejectsDuplicateOrTrailingFields(t *testing.T) {
	for _, body := range []string{
		`{"status":"ok","status":"bad"}`,
		`{"status":"ok"}{"status":"bad"}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(response, body)
		}))
		evaluation := newEvaluationClient(t, server.URL, 0)
		_, err := evaluation.Health(t.Context())
		server.Close()
		if err == nil {
			t.Fatalf("Health accepted %s", body)
		}
	}
}

func TestHealthAllowsUnrelatedGatewayCapabilities(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, `{"service":"malt-gateway","status":"ok","future_capability":"value"}`)
	}))
	defer server.Close()
	health, err := newEvaluationClient(t, server.URL, 0).Health(t.Context())
	if err != nil || health.Status != "ok" {
		t.Fatalf("Health = %#v err=%v", health, err)
	}
}

func TestBootstrapUsesDistinctAuthorizationAndBindsRootAccounting(t *testing.T) {
	object := validBootstrapMapObject(t)
	bootstrapToken := strings.Repeat("b", 64)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/evaluation/client-root/bootstrap-object" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get(gatewaytransport.BootstrapAuthorizationTokenHeader) != bootstrapToken ||
			request.Header.Get(gatewaytransport.InstanceTokenHeader) != "" ||
			request.Header.Get("Content-Type") != "application/json" {
			t.Fatalf("request headers = %#v", request.Header)
		}
		var body struct {
			Profile      string `json:"profile"`
			OperationID  string `json:"operation_id"`
			Kind         string `json:"kind"`
			Backend      string `json:"backend"`
			ExpectedRoot string `json:"expected_root"`
			Entries      []struct {
				Path   *string `json:"path"`
				Target string  `json:"target"`
			} `json:"entries"`
		}
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Profile != gatewaytransport.BootstrapProfile || body.OperationID != object.OperationID ||
			body.Kind != string(object.Kind) || body.Backend != string(object.Backend) ||
			body.ExpectedRoot != object.ExpectedRoot.String() || len(body.Entries) != 1 ||
			body.Entries[0].Path == nil || *body.Entries[0].Path != "payload" {
			t.Fatalf("bootstrap request = %#v", body)
		}
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Cache-Control", "private, no-store")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"profile": gatewaytransport.BootstrapProfile, "root": object.ExpectedRoot.String(),
			"replay_nanos": uint64(11), "persist_nanos": uint64(22),
			"write_accounting": validWriteAccounting(),
		})
	}))
	defer server.Close()

	result, err := newEvaluationClient(t, server.URL, 0).BootstrapEvaluationObject(t.Context(), bootstrapToken, object)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Root.Equals(object.ExpectedRoot) || result.ReplayNanos != 11 || result.PersistNanos != 22 ||
		!result.WriteAccounting.Available {
		t.Fatalf("bootstrap result = %#v", result)
	}
}

func TestBootstrapRejectsRedirectWithoutLeakingAuthorization(t *testing.T) {
	object := validBootstrapMapObject(t)
	bootstrapToken := strings.Repeat("b", 64)
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		targetRequests.Add(1)
		if got := request.Header.Get(gatewaytransport.BootstrapAuthorizationTokenHeader); got != "" {
			t.Errorf("redirect target received bootstrap authorization %q", got)
		}
		response.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get(gatewaytransport.BootstrapAuthorizationTokenHeader) != bootstrapToken {
			t.Errorf("source did not receive bootstrap authorization")
		}
		http.Redirect(response, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	_, err := newEvaluationClient(t, source.URL, 0).BootstrapEvaluationObject(t.Context(), bootstrapToken, object)
	if err == nil || !strings.Contains(err.Error(), "refusing evaluation Gateway redirect") {
		t.Fatalf("bootstrap redirect error = %v", err)
	}
	if targetRequests.Load() != 0 {
		t.Fatalf("redirect target received %d requests", targetRequests.Load())
	}
}

func TestRawCASDefersCIDVerificationButKeepsTokenAndBounds(t *testing.T) {
	requested := mustRawCID(t, "expected")
	hostile := []byte("wrong")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/cas/"+requested.String() {
			http.NotFound(response, request)
			return
		}
		if request.Header.Get(gatewaytransport.InstanceTokenHeader) != testInstanceToken {
			http.Error(response, "missing token", http.StatusUnauthorized)
			return
		}
		_, _ = response.Write(hostile)
	}))
	defer server.Close()

	raw, err := newEvaluationClient(t, server.URL, int64(len(hostile))).GetRawCAS(t.Context(), requested)
	if err != nil || string(raw) != string(hostile) {
		t.Fatalf("raw CAS = %q err=%v", raw, err)
	}
	_, err = newEvaluationClient(t, server.URL, int64(len(hostile)-1)).GetRawCAS(t.Context(), requested)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("bounded raw CAS error = %v", err)
	}
}

func TestRawCASClassifiesOnlyNotFound(t *testing.T) {
	requested := mustRawCID(t, "missing")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		http.Error(response, "missing", http.StatusNotFound)
	}))
	defer server.Close()

	_, err := newEvaluationClient(t, server.URL, 0).GetRawCAS(t.Context(), requested)
	if !errors.Is(err, clientcas.ErrNotFound) {
		t.Fatalf("raw CAS error = %v, want cas.ErrNotFound", err)
	}
	var apiErr *transport.Error
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusNotFound {
		t.Fatalf("raw CAS error = %T %v, want structured 404", err, err)
	}
}

func TestReadCARRequiresTokenExactMediaTypeAndBound(t *testing.T) {
	const requestBody = `{"profile":"merkledag.read/v0alpha1"}`
	responseBody := []byte("untrusted CAR bytes")
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/compat/merkledag/car/read" ||
			request.Header.Get("Content-Type") != "application/json" ||
			request.Header.Get(gatewaytransport.InstanceTokenHeader) != testInstanceToken {
			http.Error(response, "bad request", http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(request.Body)
		if string(body) != requestBody {
			t.Errorf("CAR request = %q", body)
		}
		response.Header().Set("Content-Type", merkledag.MerkleDAGCARReadMediaType)
		_, _ = response.Write(responseBody)
	}))
	defer server.Close()

	got, err := newEvaluationClient(t, server.URL, int64(len(responseBody))).ReadCAR(t.Context(), []byte(requestBody))
	if err != nil || string(got) != string(responseBody) {
		t.Fatalf("CAR response = %q err=%v", got, err)
	}
	_, err = newEvaluationClient(t, server.URL, int64(len(responseBody)-1)).ReadCAR(t.Context(), []byte(requestBody))
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("bounded CAR error = %v", err)
	}
}

func TestReadCARRejectsWrongMediaType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/octet-stream")
		_, _ = response.Write([]byte("not a CAR"))
	}))
	defer server.Close()
	_, err := newEvaluationClient(t, server.URL, 0).ReadCAR(t.Context(), []byte(`{}`))
	if err == nil || !strings.Contains(err.Error(), "content type") {
		t.Fatalf("wrong media type error = %v", err)
	}
}

func newEvaluationClient(t *testing.T, baseURL string, maxBlob int64) *gatewaytransport.Client {
	t.Helper()
	client, err := gatewaytransport.New(gatewaytransport.Options{
		BaseURL: baseURL, InstanceToken: testInstanceToken, MaxBlobResponseBytes: maxBlob,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func validBootstrapMapObject(t *testing.T) gatewaytransport.BootstrapObject {
	t.Helper()
	target := mustRawCID(t, "bootstrap-target")
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatal(err)
	}
	semanticMap, err := mappingradix.NewMap(scheme, materializermemory.New(true))
	if err != nil {
		t.Fatal(err)
	}
	path := "payload"
	root, err := semanticMap.Commit(context.Background(), "bootstrap-map", mapping.NewViewFrom(map[string]cid.Cid{path: target}))
	if err != nil {
		t.Fatal(err)
	}
	return gatewaytransport.BootstrapObject{
		OperationID: "bootstrap-1", Kind: arcset.KindMap, Backend: maltcid.BackendKindKZG, ExpectedRoot: root,
		Entries: []gatewaytransport.BootstrapEntry{{Path: &path, Target: target}},
	}
}

func validWriteAccounting() transport.ClientRootWriteAccounting {
	categories := make([]transport.ClientRootWriteCategoryAccounting, 0, 3)
	for _, category := range []string{"semantic-materialization", "arctable-records", "root-version-metadata"} {
		categories = append(categories, transport.ClientRootWriteCategoryAccounting{
			Category: category, AttemptedWrites: 1, AttemptedBytes: 2,
			AttemptedNewWrites: 1, AttemptedNewBytes: 2,
			NewlyPersistedWrites: 1, GrossNewBytes: 2,
			NewWrites: 1, NewBytes: 2, NetBytes: 2,
		})
	}
	return transport.ClientRootWriteAccounting{
		Profile: "gateway.client-root-write-accounting/v1", Available: true,
		ByteMethod:         "logical-kv-key-plus-value-bytes/v1",
		ObjectLedgerSHA256: strings.Repeat("c", 64), Categories: categories,
	}
}

func mustRawCID(t *testing.T, value string) cid.Cid {
	t.Helper()
	key, err := clientcas.CIDForBlock(clientcas.Block{Data: []byte(value), Codec: cid.Raw})
	if err != nil {
		t.Fatal(err)
	}
	return key
}

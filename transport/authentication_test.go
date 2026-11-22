package transport_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/dewebprotocol/malt-client/transport"
	"github.com/dewebprotocol/malt-core/auth/arcset/materializer/memory"
	"github.com/dewebprotocol/malt-core/auth/commitment/ipa"
	"github.com/dewebprotocol/malt-core/auth/coordinate"
	"github.com/dewebprotocol/malt-core/derivation"
	"github.com/dewebprotocol/malt-core/engine"
	"github.com/dewebprotocol/malt-core/maltcid"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	cid "github.com/ipfs/go-cid"
)

func bindingFixture(t *testing.T, layout maltcid.Layout, label []byte, target cid.Cid) (cid.Cid, []byte) {
	t.Helper()
	scheme, err := ipa.NewCommitterScheme(ipa.ProfileDirect)
	if err != nil {
		t.Fatal(err)
	}
	registry := engine.NewRegistry()
	if err := registry.Register(scheme); err != nil {
		t.Fatal(err)
	}
	e := engine.New(registry)
	d := maltcid.RootDescriptor{Layout: layout, DerivationProfile: uint8(derivation.SHA256), Profile: maltcid.IPA256}
	if layout == maltcid.List {
		d.DerivationProfile = uint8(derivation.Direct)
	}
	state := engine.State{Descriptor: d}
	if target.Defined() {
		state.Entries = []engine.Entry{{Label: label, Target: target}}
	}
	nodes := memory.NewNodes()
	root, err := e.Build(t.Context(), state, nodes)
	if err != nil {
		t.Fatal(err)
	}
	q := protocol.AuthenticationRequest{Profile: protocol.AuthenticationPathProfile, Root: root.String(), Operation: "binding", Label: &label}
	result, err := authentication.Execute(t.Context(), e, q, nodes)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := protocol.EncodeProofList(q, result)
	if err != nil {
		t.Fatal(err)
	}
	return root, wire
}

func writeProofList(t *testing.T, w http.ResponseWriter, r *http.Request, wire []byte) {
	t.Helper()
	if r.Header.Get("Accept") != protocol.ProofListMediaType {
		t.Error("missing ProofList media type")
	}
	w.Header().Set("Content-Type", protocol.ProofListMediaType)
	_, _ = w.Write(wire)
}

func TestAuthenticationTransportPreservesUint64AndRejectsChangedReceipt(t *testing.T) {
	descriptor := maltcid.RootDescriptor{DerivationProfile: uint8(derivation.Direct), Layout: maltcid.List, Profile: maltcid.IPA256}
	index := coordinate.EncodeIndex(^uint64(0))
	root, wire := bindingFixture(t, maltcid.List, index, cid.Undef)
	var observed protocol.AuthenticationRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/authentication/query":
			raw := json.NewDecoder(r.Body)
			if err := raw.Decode(&observed); err != nil {
				t.Error(err)
			}
			writeProofList(t, w, r, wire)
		case "/v1/authentication/candidates":
			_ = json.NewEncoder(w).Encode(map[string]string{"profile": protocol.AuthenticationProfile, "root": "bafkqaaa"})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	client, err := transport.NewWithBaseURL(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	query := protocol.AuthenticationRequest{Profile: protocol.AuthenticationPathProfile, Root: root.String(), Operation: "binding", Label: &index}
	if _, err = client.Authenticate(t.Context(), query); err != nil {
		t.Fatal(err)
	}
	if observed.Label == nil || string(*observed.Label) != string(coordinate.EncodeIndex(^uint64(0))) || observed.Root != query.Root {
		t.Fatal("transport changed authentication coordinates")
	}
	candidate := protocol.AuthenticationCandidate{Profile: protocol.AuthenticationProfile, Root: root.String(), State: engine.State{Descriptor: descriptor}}
	if _, err = client.MaterializeAuthentication(t.Context(), candidate); err == nil {
		t.Fatal("accepted a different receipt Root")
	}
}

package localapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	filesystemmount "github.com/dewebprotocol/malt-client/filesystem/mount"
	clientdaemon "github.com/dewebprotocol/malt-client/internal/daemon"
	truststore "github.com/dewebprotocol/malt-client/trust"
	cid "github.com/ipfs/go-cid"
)

func TestMountLifecycleClientParity(t *testing.T) {
	spec := filesystemmount.Spec{
		ID: "docs", DatasetID: "bucket-one", Branch: "main", Mountpoint: "/mnt/docs", TrustAlias: "docs",
		CachePolicy: filesystemmount.CacheVerified, WritePolicy: filesystemmount.WriteReadOnly,
		ConflictPolicy: filesystemmount.ConflictFailReadOnly,
	}
	root := localAPITestCID(t).String()
	controller := &fakeMountController{
		selectedRoot: root,
		statuses: []filesystemmount.Status{{
			Spec: spec, Desired: true, Active: true, Adapter: "test", SelectedRoot: root,
		}},
	}
	store, err := truststore.Open(filepath.Join(t.TempDir(), "roots.json"))
	if err != nil {
		t.Fatal(err)
	}
	daemon, err := clientdaemon.NewWithOptions(store, clientdaemon.Options{Mounts: controller})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(daemon.Handler())
	defer server.Close()
	client, err := New(Options{HTTPClient: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	statuses, err := client.ListMounts(t.Context())
	if err != nil || len(statuses) != 1 || statuses[0].Spec != spec {
		t.Fatalf("ListMounts=%#v err=%v", statuses, err)
	}
	status, err := client.Mount(t.Context(), spec)
	if err != nil || !status.Active || status.Spec != spec {
		t.Fatalf("Mount=%#v err=%v", status, err)
	}
	if err := client.Unmount(t.Context(), "docs"); err != nil {
		t.Fatal(err)
	}
	if controller.mounted != spec || controller.unmounted != "docs" {
		t.Fatalf("daemon controller mounted=%#v unmounted=%q", controller.mounted, controller.unmounted)
	}
}

func TestClientPreservesStructuredLocalAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusConflict)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "mount identity is already bound differently"})
	}))
	defer server.Close()
	client, err := New(Options{HTTPClient: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	validSpec := filesystemmount.Spec{
		ID: "docs", DatasetID: "bucket", Branch: "main", Mountpoint: "/mnt/docs", TrustAlias: "docs",
	}
	_, err = client.Mount(t.Context(), validSpec)
	var apiError *Error
	if !errors.As(err, &apiError) || apiError.StatusCode != http.StatusConflict {
		t.Fatalf("Mount error=%v", err)
	}
	if err := client.Unmount(context.Background(), "bad/id"); err == nil {
		t.Fatal("slash mount ID was accepted")
	}
	for _, id := range []string{".", ".."} {
		if err := client.Unmount(context.Background(), id); err == nil {
			t.Fatalf("reserved mount ID %q was accepted", id)
		}
		reserved := validSpec
		reserved.ID = id
		if _, err := client.Mount(context.Background(), reserved); err == nil {
			t.Fatalf("reserved mount ID %q was accepted for creation", id)
		}
	}
}

func TestClientRejectsAmbiguousBaseURL(t *testing.T) {
	for _, baseURL := range []string{
		"file:///tmp/malt.sock",
		"http://user@example.test",
		"http://example.test?route=wrong",
		"http://example.test#wrong",
	} {
		if _, err := New(Options{HTTPClient: http.DefaultClient, BaseURL: baseURL}); err == nil {
			t.Fatalf("New accepted ambiguous base URL %q", baseURL)
		}
	}
}

func TestClientRejectsInvalidSuccessfulMountResponses(t *testing.T) {
	spec := filesystemmount.Spec{
		ID: "docs", DatasetID: "bucket", Branch: "main", Mountpoint: "/mnt/docs", TrustAlias: "docs",
	}
	normalized, err := filesystemmount.NormalizeSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	root := localAPITestCID(t).String()
	valid := filesystemmount.Status{
		Spec: normalized, Desired: true, Active: true, Adapter: "test", SelectedRoot: root,
	}
	for _, test := range []struct {
		name string
		body any
	}{
		{name: "null", body: nil},
		{name: "missing fields", body: filesystemmount.Status{}},
		{name: "identity substitution", body: func() filesystemmount.Status {
			status := valid
			status.Spec.DatasetID = "other"
			return status
		}()},
		{name: "inactive", body: func() filesystemmount.Status {
			status := valid
			status.Active = false
			status.SelectedRoot = ""
			return status
		}()},
		{name: "missing root", body: func() filesystemmount.Status {
			status := valid
			status.SelectedRoot = ""
			return status
		}()},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(test.body)
			}))
			defer server.Close()
			client, err := New(Options{HTTPClient: server.Client(), BaseURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Mount(t.Context(), spec); err == nil {
				t.Fatal("invalid successful response was accepted")
			}
		})
	}
}

func TestClientRejectsInvalidSuccessfulListResponses(t *testing.T) {
	root := localAPITestCID(t).String()
	spec, err := filesystemmount.NormalizeSpec(filesystemmount.Spec{
		ID: "docs", DatasetID: "bucket", Branch: "main", Mountpoint: "/mnt/docs", TrustAlias: "docs",
	})
	if err != nil {
		t.Fatal(err)
	}
	valid := filesystemmount.Status{
		Spec: spec, Desired: true, Active: true, Adapter: "test", SelectedRoot: root,
	}
	for _, test := range []struct {
		name string
		body any
	}{
		{name: "null list", body: map[string]any{"mounts": nil}},
		{name: "invalid entry", body: map[string]any{"mounts": []filesystemmount.Status{{}}}},
		{name: "duplicate identity", body: map[string]any{"mounts": []filesystemmount.Status{valid, valid}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(test.body)
			}))
			defer server.Close()
			client, err := New(Options{HTTPClient: server.Client(), BaseURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.ListMounts(t.Context()); err == nil {
				t.Fatal("invalid mount list was accepted")
			}
		})
	}
}

func TestClientRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(make([]byte, (4<<20)+1))
	}))
	defer server.Close()
	client, err := New(Options{HTTPClient: server.Client(), BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListMounts(t.Context()); err == nil {
		t.Fatal("oversized local API response was accepted")
	}
}

type fakeMountController struct {
	statuses     []filesystemmount.Status
	mounted      filesystemmount.Spec
	unmounted    string
	selectedRoot string
}

func (f *fakeMountController) List() ([]filesystemmount.Status, error) {
	return append([]filesystemmount.Status(nil), f.statuses...), nil
}

func (f *fakeMountController) Mount(_ context.Context, spec filesystemmount.Spec) (filesystemmount.Status, error) {
	f.mounted = spec
	return filesystemmount.Status{
		Spec: spec, Desired: true, Active: true, Adapter: "test", SelectedRoot: f.selectedRoot,
	}, nil
}

func localAPITestCID(t *testing.T) cid.Cid {
	t.Helper()
	value, err := (cid.Prefix{Version: 1, Codec: cid.Raw, MhType: 0x12, MhLength: -1}).Sum([]byte("accepted root"))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func (f *fakeMountController) Unmount(_ context.Context, id string) error {
	f.unmounted = id
	return nil
}

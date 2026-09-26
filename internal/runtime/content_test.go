package runtime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt-client/transport"
	"github.com/dewebprotocol/malt-client/unixfs"
)

func TestDatasetLayoutUsesBucketAndRejectsConflictingOverride(t *testing.T) {
	for _, layout := range []unixfs.LayoutKind{unixfs.LayoutFlatV1, unixfs.LayoutHybridV1, unixfs.LayoutRootedV1} {
		t.Run(string(layout), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v1/buckets/one" || r.Header.Get("Authorization") != "Bearer token" {
					t.Errorf("unexpected metadata request: %s", r.URL.Path)
					http.Error(w, "unexpected", 400)
					return
				}
				_ = json.NewEncoder(w).Encode(transport.Bucket{ID: "one", TenantID: "tenant", Name: "One", State: "active", Role: "owner", CreatedBy: "owner", Layout: transport.BucketLayout(layout)})
			}))
			defer server.Close()
			remote, err := transport.New(transport.Options{BaseURL: server.URL, BucketID: "one", TenantBearerToken: "token"})
			if err != nil {
				t.Fatal(err)
			}
			for _, override := range []string{"", string(layout)} {
				got, err := DatasetLayout(t.Context(), remote, override)
				if err != nil || got != layout {
					t.Fatalf("override %q: layout=%q err=%v", override, got, err)
				}
			}
			other := unixfs.LayoutFlatV1
			if layout == other {
				other = unixfs.LayoutRootedV1
			}
			if _, err := DatasetLayout(t.Context(), remote, string(other)); err == nil || !strings.Contains(err.Error(), "conflicts") {
				t.Fatalf("conflicting override: %v", err)
			}
		})
	}
}

func TestUnscopedLayoutDoesNotFetchBucketMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s", r.URL)
		http.Error(w, "unexpected", 500)
	}))
	defer server.Close()
	remote, err := transport.New(transport.Options{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	for override, want := range map[string]unixfs.LayoutKind{"": unixfs.LayoutHybridV1, "flat-v1": unixfs.LayoutFlatV1, "rooted-v1": unixfs.LayoutRootedV1} {
		got, err := DatasetLayout(t.Context(), remote, override)
		if err != nil || got != want {
			t.Fatalf("override %q: layout=%q err=%v", override, got, err)
		}
	}
	if _, err := DatasetLayout(t.Context(), remote, "unknown"); err == nil {
		t.Fatal("unknown layout accepted")
	}
}

package unixfs_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/dewebprotocol/malt-client/application/add"
	client "github.com/dewebprotocol/malt-client/transport"
	"github.com/dewebprotocol/malt-client/unixfs"
	"github.com/dewebprotocol/malt-core/maltcid"
	cid "github.com/ipfs/go-cid"
)

type addPlanRemote struct{ *realRemote }

func (r addPlanRemote) DefaultBackend(context.Context) (maltcid.BackendKind, error) {
	return maltcid.BackendKindKZG, nil
}

func TestOrdinaryAddPreservesHTTPBatchUploads(t *testing.T) {
	remote := addPlanRemote{newRealRemote(t)}
	var requests, uploaded atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/cas/batch" {
			http.Error(w, "expected a batched CAS upload", http.StatusBadRequest)
			return
		}
		var request struct {
			Profile string         `json:"profile"`
			Blocks  []client.Block `json:"blocks"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request.Profile != client.CASPutBatchProfile {
			http.Error(w, "invalid batch", http.StatusBadRequest)
			return
		}
		results := make([]map[string]string, len(request.Blocks))
		for i, block := range request.Blocks {
			key, err := remote.PutWithCodec(r.Context(), block.Data, block.Codec)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			results[i] = map[string]string{"cid": key.String(), "status": "stored"}
		}
		uploaded.Add(int64(len(results)))
		_ = json.NewEncoder(w).Encode(map[string]any{"profile": client.CASPutBatchProfile, "results": results})
	}))
	defer server.Close()
	blocks, err := client.New(client.Options{BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	var paths []string
	for i := 0; i < 32; i++ {
		path := filepath.Join(directory, fmt.Sprintf("file-%02d", i))
		if err := os.WriteFile(path, []byte(fmt.Sprintf("body-%02d", i)), 0o600); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}
	result, err := add.Run(t.Context(), nil, remote, blocks, add.Request{Inputs: paths})
	if err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 1 || uploaded.Load() < int64(len(paths)) {
		t.Fatalf("ordinary add issued %d requests for %d blocks", requests.Load(), uploaded.Load())
	}
	reader, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: remote, Blocks: remote})
	if err != nil {
		t.Fatal(err)
	}
	value, err := reader.ReadFile(t.Context(), cid.MustParse(result.Result.NewRoot), "file-31")
	if err != nil || string(value.Body) != "body-31" {
		t.Fatalf("batched add readback = %v, %v", value, err)
	}
}

func TestOrdinaryAddReusesInstalledSiblingRoots(t *testing.T) {
	for _, layout := range []unixfs.LayoutKind{unixfs.LayoutFlatV1, unixfs.LayoutHybridV1, unixfs.LayoutRootedV1} {
		for _, swap := range []bool{false, true} {
			t.Run(string(layout)+map[bool]string{false: "/converge", true: "/swap"}[swap], func(t *testing.T) {
				remote := addPlanRemote{newRealRemote(t)}
				dir := t.TempDir()
				paths := []string{filepath.Join(dir, "a"), filepath.Join(dir, "b")}
				for i, path := range paths {
					if err := os.Mkdir(path, 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(path, "file"), []byte{byte('X' + i)}, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				request := add.Request{Inputs: paths, Options: add.Options{Layout: string(layout)}}
				first, err := add.Run(t.Context(), nil, remote, remote, request)
				if err != nil {
					t.Fatal(err)
				}
				for i, path := range paths {
					value := byte('Y')
					if i == 1 && swap {
						value = 'X'
					}
					if err := os.WriteFile(filepath.Join(path, "file"), []byte{value}, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				request.Root = first.Result.NewRoot
				second, err := add.Run(t.Context(), nil, remote, remote, request)
				if err != nil {
					t.Fatal(err)
				}
				reader, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: remote, Blocks: remote, Layout: layout})
				if err != nil {
					t.Fatal(err)
				}
				for i, name := range []string{"a/file", "b/file"} {
					value, err := reader.ReadFile(t.Context(), cid.MustParse(second.Result.NewRoot), name)
					if err != nil {
						t.Fatal(err)
					}
					want := "Y"
					if i == 1 && swap {
						want = "X"
					}
					if string(value.Body) != want {
						t.Fatalf("%s = %q, want %s", name, value.Body, want)
					}
				}
			})
		}
	}
}

package mock

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dewebprotocol/malt/core/cas"
	cashttpapi "github.com/dewebprotocol/malt/core/cas/httpapi"
	"github.com/dewebprotocol/malt/core/cas/ipfs"
	"github.com/dewebprotocol/malt/core/codec"
	cid "github.com/ipfs/go-cid"
)

func TestHTTPServerKuboCompatibleBlockAPI(t *testing.T) {
	mockCAS := NewCAS(WithoutLatency())
	ts := httptest.NewServer(NewHTTPServer("", mockCAS).Handler())
	defer ts.Close()

	client := ipfs.NewClient(ts.URL, ipfs.WithTimeout(2*time.Second))
	ctx := context.Background()

	blockCID, err := client.Put(ctx, []byte("hello malt"))
	if err != nil {
		t.Fatalf("put block via kubo-compatible API: %v", err)
	}

	ok, err := client.Has(ctx, blockCID)
	if err != nil {
		t.Fatalf("has block via kubo-compatible API: %v", err)
	}
	if !ok {
		t.Fatal("expected uploaded block to exist")
	}

	data, err := client.Get(ctx, blockCID)
	if err != nil {
		t.Fatalf("get block via kubo-compatible API: %v", err)
	}
	if string(data) != "hello malt" {
		t.Fatalf("block data = %q, want %q", string(data), "hello malt")
	}
}

func TestHTTPServerSupportsTypedBlockPut(t *testing.T) {
	mockCAS := NewCAS(WithoutLatency())
	ts := httptest.NewServer(NewHTTPServer("", mockCAS).Handler())
	defer ts.Close()

	client := ipfs.NewClient(ts.URL, ipfs.WithTimeout(2*time.Second))
	ctx := context.Background()
	payload := []byte(`{"entries":["a.txt"]}`)

	blockCID, err := client.PutWithCodec(ctx, payload, codec.CodecMaltManifest)
	if err != nil {
		t.Fatalf("put typed block: %v", err)
	}
	if blockCID.Prefix().Codec != codec.CodecMaltManifest {
		t.Fatalf("codec = %x, want %x", blockCID.Prefix().Codec, codec.CodecMaltManifest)
	}
	data, err := client.Get(ctx, blockCID)
	if err != nil {
		t.Fatalf("get typed block: %v", err)
	}
	if string(data) != string(payload) {
		t.Fatalf("typed block data = %q", string(data))
	}
}

func TestHTTPServerSupportsHasBatch(t *testing.T) {
	mockCAS := NewCAS(WithoutLatency())
	ts := httptest.NewServer(NewHTTPServer("", mockCAS).Handler())
	defer ts.Close()

	ctx := context.Background()
	present, err := mockCAS.Put(ctx, []byte("present"))
	if err != nil {
		t.Fatalf("put present: %v", err)
	}
	missing, err := cas.CIDForBlock(cas.Block{Data: []byte("missing")})
	if err != nil {
		t.Fatalf("CIDForBlock missing: %v", err)
	}

	req := cashttpapi.HasBatchRequest{CIDs: []string{missing.String(), present.String()}}
	var resp cashttpapi.HasBatchResponse
	postJSON(t, ts.URL+"/api/v0/malt/block/has-batch", req, &resp, http.StatusOK)
	if len(resp.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(resp.Results))
	}
	if resp.Results[0].Present || !resp.Results[1].Present {
		t.Fatalf("presence results = %+v, want false/true", resp.Results)
	}
}

func TestHTTPServerSupportsPutBatch(t *testing.T) {
	mockCAS := NewCAS(WithoutLatency())
	ts := httptest.NewServer(NewHTTPServer("", mockCAS).Handler())
	defer ts.Close()

	req := cashttpapi.PutBatchRequest{Blocks: []cashttpapi.PutBatchBlock{
		{Data: base64.StdEncoding.EncodeToString([]byte("raw"))},
		{Codec: codec.CodecMaltManifest, Data: base64.StdEncoding.EncodeToString([]byte(`{"entries":["a.txt"]}`))},
	}}
	var resp cashttpapi.PutBatchResponse
	postJSON(t, ts.URL+"/api/v0/malt/block/put-batch", req, &resp, http.StatusOK)
	if len(resp.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(resp.Results))
	}
	if resp.Results[0].Status != string(cas.PutStatusStored) || resp.Results[1].Status != string(cas.PutStatusStored) {
		t.Fatalf("statuses = %+v, want stored/stored", resp.Results)
	}
	typed, err := cid.Decode(resp.Results[1].CID)
	if err != nil {
		t.Fatalf("decode typed CID: %v", err)
	}
	if typed.Prefix().Codec != codec.CodecMaltManifest {
		t.Fatalf("codec = %x, want %x", typed.Prefix().Codec, codec.CodecMaltManifest)
	}
}

func TestHTTPServerPutBatchRejectsInvalidBase64(t *testing.T) {
	mockCAS := NewCAS(WithoutLatency())
	ts := httptest.NewServer(NewHTTPServer("", mockCAS).Handler())
	defer ts.Close()

	req := cashttpapi.PutBatchRequest{Blocks: []cashttpapi.PutBatchBlock{{Data: "not base64 %%%"}}}
	postJSON(t, ts.URL+"/api/v0/malt/block/put-batch", req, nil, http.StatusBadRequest)
}

func TestHTTPServerPutBatchPreservesResultOrder(t *testing.T) {
	mockCAS := NewCAS(WithoutLatency())
	ts := httptest.NewServer(NewHTTPServer("", mockCAS).Handler())
	defer ts.Close()

	blocks := []cas.Block{
		{Data: []byte("first")},
		{Data: []byte("second")},
		{Data: []byte("first")},
	}
	req := cashttpapi.PutBatchRequest{Blocks: make([]cashttpapi.PutBatchBlock, len(blocks))}
	for i, block := range blocks {
		req.Blocks[i] = cashttpapi.PutBatchBlock{
			Codec: block.Codec,
			Data:  base64.StdEncoding.EncodeToString(block.Data),
		}
	}

	var resp cashttpapi.PutBatchResponse
	postJSON(t, ts.URL+"/api/v0/malt/block/put-batch", req, &resp, http.StatusOK)
	if len(resp.Results) != len(blocks) {
		t.Fatalf("results = %d, want %d", len(resp.Results), len(blocks))
	}
	for i, block := range blocks {
		want, err := cas.CIDForBlock(block)
		if err != nil {
			t.Fatalf("CIDForBlock(%d): %v", i, err)
		}
		if resp.Results[i].CID != want.String() {
			t.Fatalf("result[%d] CID = %s, want %s", i, resp.Results[i].CID, want)
		}
	}
}

func postJSON(t *testing.T, url string, req any, out any, wantStatus int) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post json: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		t.Fatalf("status = %d, want %d", resp.StatusCode, wantStatus)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			t.Fatalf("decode response: %v", err)
		}
	}
}

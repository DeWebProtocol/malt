package mock

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/dewebprotocol/malt/core/cas/ipfs"
	"github.com/dewebprotocol/malt/core/codec"
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

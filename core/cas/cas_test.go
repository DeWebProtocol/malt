package cas_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/core/cas"
	"github.com/dewebprotocol/malt/core/codec"
	cid "github.com/ipfs/go-cid"
)

type fallbackWriter struct {
	raw   [][]byte
	typed []cas.Block
}

func (w *fallbackWriter) Put(_ context.Context, data []byte) (cid.Cid, error) {
	w.raw = append(w.raw, append([]byte(nil), data...))
	return cas.CIDForBlock(cas.Block{Data: data})
}

func (w *fallbackWriter) PutWithCodec(_ context.Context, data []byte, codec uint64) (cid.Cid, error) {
	w.typed = append(w.typed, cas.Block{Data: append([]byte(nil), data...), Codec: codec})
	return cas.CIDForBlock(cas.Block{Data: data, Codec: codec})
}

type rawOnlyWriter struct{}

func (rawOnlyWriter) Put(_ context.Context, data []byte) (cid.Cid, error) {
	return cas.CIDForBlock(cas.Block{Data: data})
}

func TestCIDForBlockRaw(t *testing.T) {
	got, err := cas.CIDForBlock(cas.Block{Data: []byte("hello")})
	if err != nil {
		t.Fatalf("CIDForBlock: %v", err)
	}
	want, err := newPayloadCID([]byte("hello"))
	if err != nil {
		t.Fatalf("newPayloadCID: %v", err)
	}
	if !got.Equals(want) {
		t.Fatalf("raw CID = %s, want %s", got, want)
	}
}

func TestCIDForBlockTypedCodec(t *testing.T) {
	got, err := cas.CIDForBlock(cas.Block{Data: []byte(`{"entries":["a.txt"]}`), Codec: codec.CodecMaltManifest})
	if err != nil {
		t.Fatalf("CIDForBlock: %v", err)
	}
	if got.Prefix().Codec != codec.CodecMaltManifest {
		t.Fatalf("codec = %x, want %x", got.Prefix().Codec, codec.CodecMaltManifest)
	}
}

func TestPutBlocksFallbackPreservesOrder(t *testing.T) {
	ctx := context.Background()
	writer := &fallbackWriter{}
	blocks := []cas.Block{
		{Data: []byte("first")},
		{Data: []byte("second"), Codec: codec.CodecMaltManifest},
		{Data: []byte("third")},
	}

	results, err := cas.PutBlocks(ctx, writer, blocks)
	if err != nil {
		t.Fatalf("PutBlocks: %v", err)
	}
	if len(results) != len(blocks) {
		t.Fatalf("results = %d, want %d", len(results), len(blocks))
	}
	for i, block := range blocks {
		want, err := cas.CIDForBlock(block)
		if err != nil {
			t.Fatalf("CIDForBlock(%d): %v", i, err)
		}
		if !results[i].CID.Equals(want) {
			t.Fatalf("result[%d] CID = %s, want %s", i, results[i].CID, want)
		}
		if results[i].Status != cas.PutStatusStored {
			t.Fatalf("result[%d] status = %q, want stored", i, results[i].Status)
		}
	}
	if len(writer.raw) != 2 || len(writer.typed) != 1 {
		t.Fatalf("raw writes = %d typed writes = %d, want 2/1", len(writer.raw), len(writer.typed))
	}
}

func TestPutBlocksNonRawWithoutTypedWriterReturnsError(t *testing.T) {
	_, err := cas.PutBlocks(context.Background(), rawOnlyWriter{}, []cas.Block{
		{Data: []byte("typed"), Codec: codec.CodecMaltManifest},
	})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPutBlocksEmptyBatch(t *testing.T) {
	results, err := cas.PutBlocks(context.Background(), rawOnlyWriter{}, nil)
	if err != nil {
		t.Fatalf("PutBlocks empty: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("empty results = %d, want 0", len(results))
	}
}

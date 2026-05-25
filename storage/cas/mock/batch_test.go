package mock

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/storage/cas"
	cid "github.com/ipfs/go-cid"
)

func TestCASPutBatchStoresBlocks(t *testing.T) {
	ctx := context.Background()
	store := NewCAS(WithoutLatency())
	blocks := []cas.Block{
		{Data: []byte("first")},
		{Data: []byte("second")},
	}

	results, err := store.PutBatch(ctx, blocks)
	if err != nil {
		t.Fatalf("PutBatch: %v", err)
	}
	for i, result := range results {
		if result.Status != cas.PutStatusStored {
			t.Fatalf("result[%d] status = %q, want stored", i, result.Status)
		}
		data, err := store.Get(ctx, result.CID)
		if err != nil {
			t.Fatalf("Get result[%d]: %v", i, err)
		}
		if string(data) != string(blocks[i].Data) {
			t.Fatalf("data[%d] = %q, want %q", i, data, blocks[i].Data)
		}
	}
	stats := store.SnapshotStats()
	if stats.PutCount != 2 || stats.BytesPut != uint64(len("first")+len("second")) {
		t.Fatalf("stats = %+v, want PutCount 2 and batch bytes", stats)
	}
}

func TestCASPutBatchReportsAlreadyPresent(t *testing.T) {
	ctx := context.Background()
	store := NewCAS(WithoutLatency())
	block := cas.Block{Data: []byte("same")}
	if _, err := store.PutBatch(ctx, []cas.Block{block}); err != nil {
		t.Fatalf("initial PutBatch: %v", err)
	}

	results, err := store.PutBatch(ctx, []cas.Block{block})
	if err != nil {
		t.Fatalf("second PutBatch: %v", err)
	}
	if results[0].Status != cas.PutStatusAlreadyPresent {
		t.Fatalf("status = %q, want already_present", results[0].Status)
	}
}

func TestCASHasBatchPreservesOrder(t *testing.T) {
	ctx := context.Background()
	store := NewCAS(WithoutLatency())
	present, err := cas.CIDForBlock(cas.Block{Data: []byte("present")})
	if err != nil {
		t.Fatalf("CIDForBlock present: %v", err)
	}
	missing, err := cas.CIDForBlock(cas.Block{Data: []byte("missing")})
	if err != nil {
		t.Fatalf("CIDForBlock missing: %v", err)
	}
	if _, err := store.PutBatch(ctx, []cas.Block{{Data: []byte("present")}}); err != nil {
		t.Fatalf("PutBatch present: %v", err)
	}

	results, err := store.HasBatch(ctx, []cid.Cid{missing, present, missing})
	if err != nil {
		t.Fatalf("HasBatch: %v", err)
	}
	want := []bool{false, true, false}
	for i := range want {
		if results[i] != want[i] {
			t.Fatalf("results[%d] = %v, want %v", i, results[i], want[i])
		}
	}
	stats := store.SnapshotStats()
	if stats.HasCount != 3 {
		t.Fatalf("HasCount = %d, want 3", stats.HasCount)
	}
}

func TestCASPutBatchSupportsTypedCodecs(t *testing.T) {
	ctx := context.Background()
	store := NewCAS(WithoutLatency())
	payload := []byte(`{"entries":["a.txt"]}`)

	results, err := store.PutBatch(ctx, []cas.Block{{Data: payload, Codec: testTypedCodec}})
	if err != nil {
		t.Fatalf("PutBatch typed: %v", err)
	}
	if results[0].CID.Prefix().Codec != testTypedCodec {
		t.Fatalf("codec = %x, want %x", results[0].CID.Prefix().Codec, testTypedCodec)
	}
	data, err := store.Get(ctx, results[0].CID)
	if err != nil {
		t.Fatalf("Get typed: %v", err)
	}
	if string(data) != string(payload) {
		t.Fatalf("typed data = %q, want %q", data, payload)
	}
}

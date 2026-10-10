package unixfs

import (
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
	"math"
	"testing"
)

func TestChunkMetadataBindsCountAndRejectsAmbiguousDocuments(t *testing.T) {
	for _, tc := range []struct{ total, width, count uint64 }{{0, 8, 0}, {17, 8, 3}, {math.MaxUint64, 1, math.MaxUint64}, {math.MaxUint64, math.MaxUint64, 1}} {
		key, err := InlineChunkMetadata(tc.total, tc.width, tc.count)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseInlineChunkMetadata(key, tc.count)
		if err != nil || got.TotalSize != tc.total || got.ChunkSize != tc.width {
			t.Fatal("roundtrip", err)
		}
		if _, err := ParseInlineChunkMetadata(key, tc.count^1); err == nil {
			t.Fatal("count mismatch accepted")
		}
	}
	for _, data := range []string{
		`{"profile":"malt.unixfs.chunks/1","chunk_size":"0","total_size":"8"}`,
		`{"profile":"malt.unixfs.chunks/1","chunk_size":"4","total_size":"8","total_size":"8"}`,
		`{"profile":"malt.unixfs.chunks/1","chunk_size":"04","total_size":"8"}`,
		`{"profile":"other","chunk_size":"4","total_size":"8"}`,
		`{"profile":"malt.unixfs.chunks/1","chunk_size":"4","total_size":"8","height":0}`,
	} {
		key, _ := (cid.Prefix{Version: 1, Codec: cid.Raw, MhType: mh.SHA2_256, MhLength: -1}).Sum([]byte(data))
		if _, err := ParseChunkMetadata(key, []byte(data), 2); err == nil {
			t.Fatal("accepted malformed document", data)
		}
	}
	key, _ := InlineChunkMetadata(8, 4, 2)
	if _, err := ParseChunkMetadata(key, []byte("{}"), 2); err == nil {
		t.Fatal("unbound bytes accepted")
	}
}

package indexed_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/core/eat/overwrite"
	kvmemory "github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/sce/commitment"
	"github.com/dewebprotocol/malt/core/sce/commitment/ipa"
	"github.com/dewebprotocol/malt/core/sce/commitment/kzg"
	"github.com/dewebprotocol/malt/core/structure/list"
	"github.com/dewebprotocol/malt/core/structure/list/indexed"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

type schemeFactory func(t *testing.T) commitment.Scheme

func newPayloadCID(data []byte) cid.Cid {
	sum, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cid.Raw, sum)
}

func listSchemes() map[string]schemeFactory {
	return map[string]schemeFactory{
		"ipa": func(t *testing.T) commitment.Scheme {
			t.Helper()
			scheme, err := ipa.NewScheme()
			if err != nil {
				t.Fatalf("ipa.NewScheme failed: %v", err)
			}
			return scheme
		},
		"kzg": func(t *testing.T) commitment.Scheme {
			t.Helper()
			scheme, err := kzg.NewScheme()
			if err != nil {
				t.Fatalf("kzg.NewScheme failed: %v", err)
			}
			return scheme
		},
	}
}

func newSemantic(t *testing.T, factory schemeFactory, kv *kvmemory.KV, bucketID string) *indexed.Semantic {
	t.Helper()

	e, err := overwrite.NewEAT(overwrite.WithKVStore(kv))
	if err != nil {
		t.Fatalf("overwrite.NewEAT failed: %v", err)
	}
	semantic, err := indexed.New(factory(t), e, bucketID)
	if err != nil {
		t.Fatalf("indexed.New failed: %v", err)
	}
	return semantic
}

func assertVerifiedQuery(t *testing.T, semantic *indexed.Semantic, root cid.Cid, index uint64, expected list.Query) {
	t.Helper()

	query, proof, err := semantic.Prove(context.Background(), root, index)
	if err != nil {
		t.Fatalf("Prove(%d) failed: %v", index, err)
	}
	if query.Length != expected.Length {
		t.Fatalf("query length mismatch for %d: want %d got %d", index, expected.Length, query.Length)
	}
	if !query.Key.Equals(expected.Key) {
		t.Fatalf("query key mismatch for %d: want %s got %s", index, expected.Key, query.Key)
	}

	ok, err := semantic.Verify(root, index, query, proof)
	if err != nil {
		t.Fatalf("Verify(%d) failed: %v", index, err)
	}
	if !ok {
		t.Fatalf("Verify(%d) returned false", index)
	}
}

func TestIndexedListSemanticRuntime(t *testing.T) {
	ctx := context.Background()
	values := []cid.Cid{
		newPayloadCID([]byte("v0")),
		newPayloadCID([]byte("v1")),
		newPayloadCID([]byte("v2")),
	}

	for name, factory := range listSchemes() {
		t.Run(name, func(t *testing.T) {
			kv := kvmemory.New()
			bucketID := "indexed-runtime-" + name

			semantic := newSemantic(t, factory, kv, bucketID)
			root, err := semantic.Commit(ctx, list.NewViewFromSlice(values))
			if err != nil {
				t.Fatalf("Commit failed: %v", err)
			}

			assertVerifiedQuery(t, semantic, root, 1, list.Query{
				Key:    values[1],
				Length: 3,
			})
			assertVerifiedQuery(t, semantic, root, 9, list.Query{
				Key:    cid.Undef,
				Length: 3,
			})

			restarted := newSemantic(t, factory, kv, bucketID)
			assertVerifiedQuery(t, restarted, root, 2, list.Query{
				Key:    values[2],
				Length: 3,
			})
		})
	}
}

func TestIndexedListSemanticUpdates(t *testing.T) {
	ctx := context.Background()
	initial := []cid.Cid{
		newPayloadCID([]byte("a")),
		newPayloadCID([]byte("b")),
		newPayloadCID([]byte("c")),
	}

	for name, factory := range listSchemes() {
		t.Run(name, func(t *testing.T) {
			kv := kvmemory.New()
			bucketID := "indexed-update-" + name

			semantic := newSemantic(t, factory, kv, bucketID)
			root, err := semantic.Commit(ctx, list.NewViewFromSlice(initial))
			if err != nil {
				t.Fatalf("Commit failed: %v", err)
			}

			replacement := newPayloadCID([]byte("b2"))
			replacedRoot, err := semantic.Replace(ctx, root, 1, initial[1], replacement)
			if err != nil {
				t.Fatalf("Replace failed: %v", err)
			}
			assertVerifiedQuery(t, semantic, replacedRoot, 1, list.Query{
				Key:    replacement,
				Length: 3,
			})

			appended := newPayloadCID([]byte("d"))
			appendedRoot, newIndex, err := semantic.Append(ctx, replacedRoot, appended)
			if err != nil {
				t.Fatalf("Append failed: %v", err)
			}
			if newIndex != 3 {
				t.Fatalf("unexpected append index %d", newIndex)
			}
			assertVerifiedQuery(t, semantic, appendedRoot, 3, list.Query{
				Key:    appended,
				Length: 4,
			})

			truncatedRoot, err := semantic.Truncate(ctx, appendedRoot, 2)
			if err != nil {
				t.Fatalf("Truncate failed: %v", err)
			}
			assertVerifiedQuery(t, semantic, truncatedRoot, 1, list.Query{
				Key:    replacement,
				Length: 2,
			})
			assertVerifiedQuery(t, semantic, truncatedRoot, 2, list.Query{
				Key:    cid.Undef,
				Length: 2,
			})
		})
	}
}

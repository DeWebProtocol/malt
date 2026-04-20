package tree_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/core/eat/overwrite"
	kvmemory "github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/sce/commitment"
	"github.com/dewebprotocol/malt/core/sce/commitment/ipa"
	"github.com/dewebprotocol/malt/core/sce/commitment/kzg"
	"github.com/dewebprotocol/malt/core/structure/list"
	"github.com/dewebprotocol/malt/core/structure/list/tree"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

type schemeFactory func(t *testing.T) commitment.IndexCommitment

func newPayloadCID(data []byte) cid.Cid {
	sum, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cid.Raw, sum)
}

func listSchemes() map[string]schemeFactory {
	return map[string]schemeFactory{
		"ipa": func(t *testing.T) commitment.IndexCommitment {
			t.Helper()
			scheme, err := ipa.NewScheme()
			if err != nil {
				t.Fatalf("ipa.NewScheme failed: %v", err)
			}
			return scheme
		},
		"kzg": func(t *testing.T) commitment.IndexCommitment {
			t.Helper()
			scheme, err := kzg.NewScheme()
			if err != nil {
				t.Fatalf("kzg.NewScheme failed: %v", err)
			}
			return scheme
		},
	}
}

func makeValues(count int) []cid.Cid {
	values := make([]cid.Cid, count)
	for i := range values {
		values[i] = newPayloadCID([]byte{byte(i), byte(i >> 8), byte(i >> 16), byte(i >> 24)})
	}
	return values
}

func newSemantic(t *testing.T, factory schemeFactory, kv *kvmemory.KV, bucketID string) *tree.Semantic {
	t.Helper()

	e, err := overwrite.NewEAT(overwrite.WithKVStore(kv))
	if err != nil {
		t.Fatalf("overwrite.NewEAT failed: %v", err)
	}
	semantic, err := tree.New(factory(t), e, bucketID)
	if err != nil {
		t.Fatalf("tree.New failed: %v", err)
	}
	return semantic
}

func assertVerifiedQuery(t *testing.T, semantic *tree.Semantic, root cid.Cid, index uint64, expected list.Query) {
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

func TestTreeListSemanticProofsAndRestart(t *testing.T) {
	ctx := context.Background()
	values := makeValues(300)

	for name, factory := range listSchemes() {
		t.Run(name, func(t *testing.T) {
			kv := kvmemory.New()
			bucketID := "tree-proof-" + name

			semantic := newSemantic(t, factory, kv, bucketID)
			root, err := semantic.Commit(ctx, list.NewViewFromSlice(values))
			if err != nil {
				t.Fatalf("Commit failed: %v", err)
			}

			for _, index := range []uint64{0, 254, 255, 256, 299} {
				assertVerifiedQuery(t, semantic, root, index, list.Query{
					Key:    values[index],
					Length: uint64(len(values)),
				})
			}

			assertVerifiedQuery(t, semantic, root, 999, list.Query{
				Key:    cid.Undef,
				Length: uint64(len(values)),
			})

			restarted := newSemantic(t, factory, kv, bucketID)
			assertVerifiedQuery(t, restarted, root, 256, list.Query{
				Key:    values[256],
				Length: uint64(len(values)),
			})
		})
	}
}

func TestTreeListSemanticUpdates(t *testing.T) {
	ctx := context.Background()
	initial := makeValues(255)

	for name, factory := range listSchemes() {
		t.Run(name, func(t *testing.T) {
			kv := kvmemory.New()
			bucketID := "tree-update-" + name

			semantic := newSemantic(t, factory, kv, bucketID)
			root, err := semantic.Commit(ctx, list.NewViewFromSlice(initial))
			if err != nil {
				t.Fatalf("Commit failed: %v", err)
			}

			replacement := newPayloadCID([]byte("replacement"))
			replacedRoot, err := semantic.Replace(ctx, root, 128, initial[128], replacement)
			if err != nil {
				t.Fatalf("Replace failed: %v", err)
			}
			assertVerifiedQuery(t, semantic, replacedRoot, 128, list.Query{
				Key:    replacement,
				Length: uint64(len(initial)),
			})

			appended := newPayloadCID([]byte("appended"))
			appendedRoot, newIndex, err := semantic.Append(ctx, replacedRoot, appended)
			if err != nil {
				t.Fatalf("Append failed: %v", err)
			}
			if newIndex != uint64(len(initial)) {
				t.Fatalf("unexpected append index %d", newIndex)
			}
			assertVerifiedQuery(t, semantic, appendedRoot, newIndex, list.Query{
				Key:    appended,
				Length: uint64(len(initial) + 1),
			})
			assertVerifiedQuery(t, semantic, appendedRoot, 128, list.Query{
				Key:    replacement,
				Length: uint64(len(initial) + 1),
			})

			truncatedRoot, err := semantic.Truncate(ctx, appendedRoot, 128)
			if err != nil {
				t.Fatalf("Truncate failed: %v", err)
			}
			assertVerifiedQuery(t, semantic, truncatedRoot, 127, list.Query{
				Key:    initial[127],
				Length: 128,
			})
			assertVerifiedQuery(t, semantic, truncatedRoot, 128, list.Query{
				Key:    cid.Undef,
				Length: 128,
			})

			restarted := newSemantic(t, factory, kv, bucketID)
			assertVerifiedQuery(t, restarted, truncatedRoot, 127, list.Query{
				Key:    initial[127],
				Length: 128,
			})
		})
	}
}

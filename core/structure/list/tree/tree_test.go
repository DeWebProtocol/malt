package tree_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/core/arctable/overwrite"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/commitment"
	"github.com/dewebprotocol/malt/core/commitment/kzg"
	kvmemory "github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/structure/list"
	"github.com/dewebprotocol/malt/core/structure/list/tree"
	"github.com/dewebprotocol/malt/core/structure/list/tree/internal"
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

func newList(t *testing.T, factory schemeFactory, kv *kvmemory.KV) *tree.TreeList {
	t.Helper()

	semantic, _, err := newListWithArcTable(factory(t), kv)
	if err != nil {
		t.Fatalf("newListWithArcTable failed: %v", err)
	}
	return semantic
}

func newListWithArcTable(scheme commitment.IndexCommitment, kv *kvmemory.KV) (*tree.TreeList, *overwrite.ArcTable, error) {
	e, err := overwrite.NewArcTable(overwrite.WithKVStore(kv))
	if err != nil {
		return nil, nil, err
	}
	semantic, err := tree.NewList(scheme, e)
	if err != nil {
		return nil, nil, err
	}
	return semantic, e, nil
}

func assertVerifiedQuery(t *testing.T, semantic *tree.TreeList, bucketID string, root cid.Cid, index uint64, expected list.Query) {
	t.Helper()

	query, proof, err := semantic.Prove(context.Background(), bucketID, root, index)
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

			semantic := newList(t, factory, kv)
			root, err := semantic.Commit(ctx, bucketID, list.NewViewFromSlice(values))
			if err != nil {
				t.Fatalf("Commit failed: %v", err)
			}
			if codec.SemanticKindOf(root) != codec.SemanticKindList {
				t.Fatalf("root semantic kind = %s, want %s", codec.SemanticKindOf(root), codec.SemanticKindList)
			}
			if name == "kzg" && codec.BackendKindOf(root) != codec.BackendKindKZG {
				t.Fatalf("root backend kind = %s, want %s", codec.BackendKindOf(root), codec.BackendKindKZG)
			}

			for _, index := range []uint64{0, 254, 255, 256, 299} {
				assertVerifiedQuery(t, semantic, bucketID, root, index, list.Query{
					Key:    values[index],
					Length: uint64(len(values)),
				})
			}

			assertVerifiedQuery(t, semantic, bucketID, root, 999, list.Query{
				Key:    cid.Undef,
				Length: uint64(len(values)),
			})

			restarted := newList(t, factory, kv)
			assertVerifiedQuery(t, restarted, bucketID, root, 256, list.Query{
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

			semantic := newList(t, factory, kv)
			root, err := semantic.Commit(ctx, bucketID, list.NewViewFromSlice(initial))
			if err != nil {
				t.Fatalf("Commit failed: %v", err)
			}

			replacement := newPayloadCID([]byte("replacement"))
			replacedRoot, err := semantic.Replace(ctx, bucketID, root, 128, initial[128], replacement)
			if err != nil {
				t.Fatalf("Replace failed: %v", err)
			}
			assertVerifiedQuery(t, semantic, bucketID, replacedRoot, 128, list.Query{
				Key:    replacement,
				Length: uint64(len(initial)),
			})

			appended := newPayloadCID([]byte("appended"))
			appendedRoot, newIndex, err := semantic.Append(ctx, bucketID, replacedRoot, appended)
			if err != nil {
				t.Fatalf("Append failed: %v", err)
			}
			if newIndex != uint64(len(initial)) {
				t.Fatalf("unexpected append index %d", newIndex)
			}
			assertVerifiedQuery(t, semantic, bucketID, appendedRoot, newIndex, list.Query{
				Key:    appended,
				Length: uint64(len(initial) + 1),
			})
			assertVerifiedQuery(t, semantic, bucketID, appendedRoot, 128, list.Query{
				Key:    replacement,
				Length: uint64(len(initial) + 1),
			})

			truncatedRoot, err := semantic.Truncate(ctx, bucketID, appendedRoot, 128)
			if err != nil {
				t.Fatalf("Truncate failed: %v", err)
			}
			assertVerifiedQuery(t, semantic, bucketID, truncatedRoot, 127, list.Query{
				Key:    initial[127],
				Length: 128,
			})
			assertVerifiedQuery(t, semantic, bucketID, truncatedRoot, 128, list.Query{
				Key:    cid.Undef,
				Length: 128,
			})

			restarted := newList(t, factory, kv)
			assertVerifiedQuery(t, restarted, bucketID, truncatedRoot, 127, list.Query{
				Key:    initial[127],
				Length: 128,
			})
		})
	}
}

func TestTreeListEmptyAndRegrow(t *testing.T) {
	ctx := context.Background()

	for name, factory := range listSchemes() {
		t.Run(name, func(t *testing.T) {
			kv := kvmemory.New()
			bucketID := "tree-empty-" + name

			semantic := newList(t, factory, kv)
			root, err := semantic.Commit(ctx, bucketID, list.NewViewFromSlice(nil))
			if err != nil {
				t.Fatalf("Commit(empty) failed: %v", err)
			}

			assertVerifiedQuery(t, semantic, bucketID, root, 0, list.Query{
				Key:    cid.Undef,
				Length: 0,
			})

			appended := newPayloadCID([]byte("first"))
			appendedRoot, index, err := semantic.Append(ctx, bucketID, root, appended)
			if err != nil {
				t.Fatalf("Append(empty) failed: %v", err)
			}
			if index != 0 {
				t.Fatalf("unexpected append index %d", index)
			}
			assertVerifiedQuery(t, semantic, bucketID, appendedRoot, 0, list.Query{
				Key:    appended,
				Length: 1,
			})

			truncatedRoot, err := semantic.Truncate(ctx, bucketID, appendedRoot, 0)
			if err != nil {
				t.Fatalf("Truncate(to zero) failed: %v", err)
			}
			assertVerifiedQuery(t, semantic, bucketID, truncatedRoot, 0, list.Query{
				Key:    cid.Undef,
				Length: 0,
			})

			restarted := newList(t, factory, kv)
			assertVerifiedQuery(t, restarted, bucketID, truncatedRoot, 0, list.Query{
				Key:    cid.Undef,
				Length: 0,
			})
		})
	}
}

func TestTreeListRejectsUndefinedCommittedKeys(t *testing.T) {
	ctx := context.Background()

	for name, factory := range listSchemes() {
		t.Run(name, func(t *testing.T) {
			kv := kvmemory.New()
			bucketID := "tree-undefined-" + name
			semantic := newList(t, factory, kv)

			if _, err := semantic.Commit(ctx, bucketID, list.NewViewFromSlice([]cid.Cid{newPayloadCID([]byte("a")), cid.Undef})); err == nil {
				t.Fatal("Commit should reject undefined committed keys")
			}

			root, err := semantic.Commit(ctx, bucketID, list.NewViewFromSlice([]cid.Cid{newPayloadCID([]byte("a"))}))
			if err != nil {
				t.Fatalf("Commit(valid) failed: %v", err)
			}
			if _, _, err := semantic.Append(ctx, bucketID, root, cid.Undef); err == nil {
				t.Fatal("Append should reject undefined key")
			}
			if _, err := semantic.Replace(ctx, bucketID, root, 0, newPayloadCID([]byte("a")), cid.Undef); err == nil {
				t.Fatal("Replace should reject undefined new key")
			}
		})
	}
}

func TestTreeListRejectsCorruptedMaterialization(t *testing.T) {
	ctx := context.Background()
	values := makeValues(300)

	for name, factory := range listSchemes() {
		t.Run(name, func(t *testing.T) {
			kv := kvmemory.New()
			bucketID := "tree-corrupt-" + name
			scheme := factory(t)
			semantic, e, err := newListWithArcTable(scheme, kv)
			if err != nil {
				t.Fatalf("newListWithArcTable failed: %v", err)
			}

			root, err := semantic.Commit(ctx, bucketID, list.NewViewFromSlice(values))
			if err != nil {
				t.Fatalf("Commit failed: %v", err)
			}

			childRoot, err := e.Get(ctx, bucketID, cid.Undef, layout.NodeSlotPath(root, 2).String())
			if err != nil {
				t.Fatalf("failed to fetch child root: %v", err)
			}

			corruptValue := newPayloadCID([]byte("corrupt-child-slot"))
			if err := e.Update(ctx, bucketID, cid.Undef, cid.Undef, map[string]cid.Cid{
				layout.NodeSlotPath(childRoot, 1).String(): corruptValue,
			}); err != nil {
				t.Fatalf("failed to corrupt child materialization: %v", err)
			}

			if _, err := semantic.Replace(ctx, bucketID, root, 256, values[256], newPayloadCID([]byte("replacement"))); err == nil {
				t.Fatal("Replace should reject corrupted child materialization")
			}
		})
	}
}

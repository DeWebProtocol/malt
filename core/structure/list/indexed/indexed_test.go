package indexed_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/core/commitment"
	"github.com/dewebprotocol/malt/core/commitment/ipa"
	"github.com/dewebprotocol/malt/core/commitment/kzg"
	"github.com/dewebprotocol/malt/core/eat/overwrite"
	kvmemory "github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/structure/list"
	"github.com/dewebprotocol/malt/core/structure/list/indexed"
	listruntime "github.com/dewebprotocol/malt/core/structure/list/internal/runtime"
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

func newList(t *testing.T, factory schemeFactory, kv *kvmemory.KV) *indexed.IndexedList {
	t.Helper()

	semantic, _, err := newListWithEAT(factory(t), kv)
	if err != nil {
		t.Fatalf("newListWithEAT failed: %v", err)
	}
	return semantic
}

func newListWithEAT(scheme commitment.IndexCommitment, kv *kvmemory.KV) (*indexed.IndexedList, *overwrite.EAT, error) {
	e, err := overwrite.NewEAT(overwrite.WithKVStore(kv))
	if err != nil {
		return nil, nil, err
	}
	semantic, err := indexed.NewList(scheme, e)
	if err != nil {
		return nil, nil, err
	}
	return semantic, e, nil
}

func assertVerifiedQuery(t *testing.T, semantic *indexed.IndexedList, bucketID string, root cid.Cid, index uint64, expected list.Query) {
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

			semantic := newList(t, factory, kv)
			root, err := semantic.Commit(ctx, bucketID, list.NewViewFromSlice(values))
			if err != nil {
				t.Fatalf("Commit failed: %v", err)
			}

			assertVerifiedQuery(t, semantic, bucketID, root, 1, list.Query{
				Key:    values[1],
				Length: 3,
			})
			assertVerifiedQuery(t, semantic, bucketID, root, 9, list.Query{
				Key:    cid.Undef,
				Length: 3,
			})

			restarted := newList(t, factory, kv)
			assertVerifiedQuery(t, restarted, bucketID, root, 2, list.Query{
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

			semantic := newList(t, factory, kv)
			root, err := semantic.Commit(ctx, bucketID, list.NewViewFromSlice(initial))
			if err != nil {
				t.Fatalf("Commit failed: %v", err)
			}

			replacement := newPayloadCID([]byte("b2"))
			replacedRoot, err := semantic.Replace(ctx, bucketID, root, 1, initial[1], replacement)
			if err != nil {
				t.Fatalf("Replace failed: %v", err)
			}
			assertVerifiedQuery(t, semantic, bucketID, replacedRoot, 1, list.Query{
				Key:    replacement,
				Length: 3,
			})

			appended := newPayloadCID([]byte("d"))
			appendedRoot, newIndex, err := semantic.Append(ctx, bucketID, replacedRoot, appended)
			if err != nil {
				t.Fatalf("Append failed: %v", err)
			}
			if newIndex != 3 {
				t.Fatalf("unexpected append index %d", newIndex)
			}
			assertVerifiedQuery(t, semantic, bucketID, appendedRoot, 3, list.Query{
				Key:    appended,
				Length: 4,
			})

			truncatedRoot, err := semantic.Truncate(ctx, bucketID, appendedRoot, 2)
			if err != nil {
				t.Fatalf("Truncate failed: %v", err)
			}
			assertVerifiedQuery(t, semantic, bucketID, truncatedRoot, 1, list.Query{
				Key:    replacement,
				Length: 2,
			})
			assertVerifiedQuery(t, semantic, bucketID, truncatedRoot, 2, list.Query{
				Key:    cid.Undef,
				Length: 2,
			})
		})
	}
}

func TestIndexedListEmptyAndRegrow(t *testing.T) {
	ctx := context.Background()

	for name, factory := range listSchemes() {
		t.Run(name, func(t *testing.T) {
			kv := kvmemory.New()
			bucketID := "indexed-empty-" + name

			semantic := newList(t, factory, kv)
			root, err := semantic.Commit(ctx, bucketID, list.NewViewFromSlice(nil))
			if err != nil {
				t.Fatalf("Commit(empty) failed: %v", err)
			}

			assertVerifiedQuery(t, semantic, bucketID, root, 0, list.Query{
				Key:    cid.Undef,
				Length: 0,
			})

			value := newPayloadCID([]byte("first"))
			appendedRoot, index, err := semantic.Append(ctx, bucketID, root, value)
			if err != nil {
				t.Fatalf("Append(empty) failed: %v", err)
			}
			if index != 0 {
				t.Fatalf("unexpected append index %d", index)
			}
			assertVerifiedQuery(t, semantic, bucketID, appendedRoot, 0, list.Query{
				Key:    value,
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
		})
	}
}

func TestIndexedListRejectsUndefinedCommittedKeys(t *testing.T) {
	ctx := context.Background()

	for name, factory := range listSchemes() {
		t.Run(name, func(t *testing.T) {
			kv := kvmemory.New()
			bucketID := "indexed-undefined-" + name
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

func TestIndexedListRejectsCorruptedMaterialization(t *testing.T) {
	ctx := context.Background()

	for name, factory := range listSchemes() {
		t.Run(name, func(t *testing.T) {
			kv := kvmemory.New()
			bucketID := "indexed-corrupt-" + name
			scheme := factory(t)
			semantic, e, err := newListWithEAT(scheme, kv)
			if err != nil {
				t.Fatalf("newListWithEAT failed: %v", err)
			}

			root, err := semantic.Commit(ctx, bucketID, list.NewViewFromSlice([]cid.Cid{
				newPayloadCID([]byte("a")),
				newPayloadCID([]byte("b")),
			}))
			if err != nil {
				t.Fatalf("Commit failed: %v", err)
			}

			if err := e.Update(ctx, bucketID, cid.Undef, cid.Undef, map[string]cid.Cid{
				listruntime.NodeSlotPath(root, 1).String(): newPayloadCID([]byte("corrupt-root-slot")),
			}); err != nil {
				t.Fatalf("failed to corrupt root materialization: %v", err)
			}

			if _, _, err := semantic.Append(ctx, bucketID, root, newPayloadCID([]byte("c"))); err == nil {
				t.Fatal("Append should reject corrupted root materialization")
			}
		})
	}
}

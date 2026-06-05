package prefix

import (
	"context"
	"reflect"
	"testing"

	kvstore "github.com/dewebprotocol/malt/storage/kv"
	"github.com/dewebprotocol/malt/storage/kv/memory"
)

func TestKVPrefixesPointOperations(t *testing.T) {
	ctx := context.Background()
	base := memory.New()
	view := New(base, []byte("cas/"))

	if err := view.Put(ctx, []byte("block/a"), []byte("payload")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := base.Get(ctx, []byte("block/a")); err != kvstore.ErrNotFound {
		t.Fatalf("base unprefixed Get error = %v, want ErrNotFound", err)
	}
	got, err := base.Get(ctx, []byte("cas/block/a"))
	if err != nil {
		t.Fatalf("base prefixed Get: %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("prefixed payload = %q, want payload", got)
	}
}

func TestKVStripsIteratorAndBatchGetKeys(t *testing.T) {
	ctx := context.Background()
	base := memory.New()
	view := New(base, []byte("cas/"))

	if err := view.Put(ctx, []byte("a"), []byte("one")); err != nil {
		t.Fatalf("Put a: %v", err)
	}
	if err := view.Put(ctx, []byte("b"), []byte("two")); err != nil {
		t.Fatalf("Put b: %v", err)
	}
	if err := base.Put(ctx, []byte("other"), []byte("skip")); err != nil {
		t.Fatalf("base Put: %v", err)
	}

	found, err := view.BatchGet(ctx, [][]byte{[]byte("a"), []byte("missing"), []byte("b")})
	if err != nil {
		t.Fatalf("BatchGet: %v", err)
	}
	if got := map[string]string{
		"a": string(found["a"]),
		"b": string(found["b"]),
	}; !reflect.DeepEqual(got, map[string]string{"a": "one", "b": "two"}) {
		t.Fatalf("BatchGet = %#v", got)
	}
	if _, ok := found["cas/a"]; ok {
		t.Fatalf("BatchGet returned prefixed key")
	}

	var keys []string
	it := view.NewIterator(ctx, nil, nil)
	defer it.Close()
	for it.Next() {
		keys = append(keys, string(it.Key()))
	}
	if err := it.Err(); err != nil {
		t.Fatalf("iterator error: %v", err)
	}
	if !reflect.DeepEqual(keys, []string{"a", "b"}) {
		t.Fatalf("iterator keys = %#v, want [a b]", keys)
	}
}

func TestKVPrefixesHasDeleteAndBatch(t *testing.T) {
	ctx := context.Background()
	base := memory.New()
	view := New(base, []byte("cas/"))

	batch := view.Batch()
	if err := batch.Put([]byte("a"), []byte("one")); err != nil {
		t.Fatalf("batch Put a: %v", err)
	}
	if err := batch.Put([]byte("b"), []byte("two")); err != nil {
		t.Fatalf("batch Put b: %v", err)
	}
	if err := batch.Commit(ctx); err != nil {
		t.Fatalf("batch Commit: %v", err)
	}

	exists, err := view.Has(ctx, []byte("a"))
	if err != nil {
		t.Fatalf("Has a: %v", err)
	}
	if !exists {
		t.Fatal("Has a = false, want true")
	}
	if exists, err := base.Has(ctx, []byte("a")); err != nil || exists {
		t.Fatalf("base Has unprefixed a = %v, %v; want false, nil", exists, err)
	}

	batch = view.Batch()
	if err := batch.Delete([]byte("a")); err != nil {
		t.Fatalf("batch Delete a: %v", err)
	}
	if err := batch.Commit(ctx); err != nil {
		t.Fatalf("delete batch Commit: %v", err)
	}
	if exists, err := view.Has(ctx, []byte("a")); err != nil || exists {
		t.Fatalf("Has deleted a = %v, %v; want false, nil", exists, err)
	}
	if exists, err := view.Has(ctx, []byte("b")); err != nil || !exists {
		t.Fatalf("Has b after deleting a = %v, %v; want true, nil", exists, err)
	}
}

func TestKVStripRejectsUnprefixedKey(t *testing.T) {
	view := New(memory.New(), []byte("cas/"))

	if got := view.strip([]byte("block/a")); got != nil {
		t.Fatalf("strip unprefixed key = %q, want nil", got)
	}
}

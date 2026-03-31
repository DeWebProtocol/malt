package kv

import (
	"context"
	"testing"
)

func TestMemoryKV(t *testing.T) {
	kv := NewMemoryKV()
	ctx := context.Background()

	// Test Put and Get
	err := kv.Put(ctx, []byte("key1"), []byte("value1"))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	val, err := kv.Get(ctx, []byte("key1"))
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(val) != "value1" {
		t.Errorf("Expected 'value1', got '%s'", val)
	}

	// Test Has
	has, err := kv.Has(ctx, []byte("key1"))
	if err != nil || !has {
		t.Error("Expected key1 to exist")
	}

	has, err = kv.Has(ctx, []byte("nonexistent"))
	if err != nil || has {
		t.Error("Expected nonexistent to not exist")
	}

	// Test Delete
	err = kv.Delete(ctx, []byte("key1"))
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err = kv.Get(ctx, []byte("key1"))
	if err != ErrNotFound {
		t.Errorf("Expected ErrNotFound, got %v", err)
	}

	// Test Batch
	batch := kv.Batch()
	err = batch.Put([]byte("key2"), []byte("value2"))
	if err != nil {
		t.Fatalf("Batch.Put failed: %v", err)
	}
	err = batch.Put([]byte("key3"), []byte("value3"))
	if err != nil {
		t.Fatalf("Batch.Put failed: %v", err)
	}
	err = batch.Commit(ctx)
	if err != nil {
		t.Fatalf("Batch.Commit failed: %v", err)
	}

	val, err = kv.Get(ctx, []byte("key2"))
	if err != nil || string(val) != "value2" {
		t.Errorf("Expected 'value2', got '%s', err=%v", val, err)
	}

	// Test iterator
	iter := kv.NewIterator(ctx, nil, nil)
	count := 0
	for iter.Next() {
		count++
	}
	if iter.Err() != nil {
		t.Errorf("Iterator error: %v", iter.Err())
	}
	if count != 2 {
		t.Errorf("Expected 2 keys, got %d", count)
	}
	iter.Close()

	// Close
	err = kv.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestBadgerKV(t *testing.T) {
	kv, err := NewBadgerKV(&BadgerConfig{InMemory: true})
	if err != nil {
		t.Fatalf("NewBadgerKV failed: %v", err)
	}
	ctx := context.Background()

	// Test Put and Get
	err = kv.Put(ctx, []byte("key1"), []byte("value1"))
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	val, err := kv.Get(ctx, []byte("key1"))
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if string(val) != "value1" {
		t.Errorf("Expected 'value1', got '%s'", val)
	}

	// Test Has
	has, err := kv.Has(ctx, []byte("key1"))
	if err != nil || !has {
		t.Error("Expected key1 to exist")
	}

	// Test Delete
	err = kv.Delete(ctx, []byte("key1"))
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err = kv.Get(ctx, []byte("key1"))
	if err != ErrNotFound {
		t.Errorf("Expected ErrNotFound, got %v", err)
	}

	// Close
	err = kv.Close()
	if err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}
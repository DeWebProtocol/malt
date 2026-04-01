package fs_test

import (
	"context"
	"os"
	"testing"

	"github.com/dewebprotocol/malt/types/kvstore"
	"github.com/dewebprotocol/malt/types/kvstore/fs"
)

func TestFSKV(t *testing.T) {
	dir, err := os.MkdirTemp("", "malt-fs-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	kv, err := fs.New(dir)
	if err != nil {
		t.Fatalf("Failed to create fs kv: %v", err)
	}
	defer kv.Close()

	ctx := context.Background()

	// Test Put and Get
	key := []byte("test-key")
	value := []byte("test-value")

	err = kv.Put(ctx, key, value)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	got, err := kv.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if string(got) != string(value) {
		t.Errorf("Expected %s, got %s", value, got)
	}

	// Test Has
	has, err := kv.Has(ctx, key)
	if err != nil {
		t.Fatalf("Has failed: %v", err)
	}
	if !has {
		t.Error("Key should exist")
	}

	// Test non-existent key
	_, err = kv.Get(ctx, []byte("nonexistent"))
	if err != kvstore.ErrNotFound {
		t.Errorf("Expected ErrNotFound, got %v", err)
	}

	has, err = kv.Has(ctx, []byte("nonexistent"))
	if err != nil {
		t.Fatalf("Has failed: %v", err)
	}
	if has {
		t.Error("Key should not exist")
	}

	// Test Delete
	err = kv.Delete(ctx, key)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	has, err = kv.Has(ctx, key)
	if err != nil {
		t.Fatalf("Has failed: %v", err)
	}
	if has {
		t.Error("Key should not exist after delete")
	}
}

func TestFSKVIterator(t *testing.T) {
	dir, err := os.MkdirTemp("", "malt-fs-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	kv, err := fs.New(dir)
	if err != nil {
		t.Fatalf("Failed to create fs kv: %v", err)
	}
	defer kv.Close()

	ctx := context.Background()

	// Put multiple keys
	keys := [][]byte{
		[]byte("key-a"),
		[]byte("key-b"),
		[]byte("key-c"),
	}

	for i, key := range keys {
		value := []byte{byte(i)}
		err = kv.Put(ctx, key, value)
		if err != nil {
			t.Fatalf("Put failed: %v", err)
		}
	}

	// Iterate over all keys
	iter := kv.NewIterator(ctx, nil, nil)
	defer iter.Close()

	count := 0
	for iter.Next() {
		count++
	}

	if iter.Err() != nil {
		t.Fatalf("Iterator error: %v", iter.Err())
	}

	if count != 3 {
		t.Errorf("Expected 3 keys, got %d", count)
	}
}

func TestFSKVBatch(t *testing.T) {
	dir, err := os.MkdirTemp("", "malt-fs-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	kv, err := fs.New(dir)
	if err != nil {
		t.Fatalf("Failed to create fs kv: %v", err)
	}
	defer kv.Close()

	ctx := context.Background()

	batch := kv.Batch()

	err = batch.Put([]byte("batch-key-1"), []byte("value-1"))
	if err != nil {
		t.Fatalf("Batch Put failed: %v", err)
	}

	err = batch.Put([]byte("batch-key-2"), []byte("value-2"))
	if err != nil {
		t.Fatalf("Batch Put failed: %v", err)
	}

	err = batch.Commit(ctx)
	if err != nil {
		t.Fatalf("Batch Commit failed: %v", err)
	}

	// Verify both keys exist
	has1, _ := kv.Has(ctx, []byte("batch-key-1"))
	has2, _ := kv.Has(ctx, []byte("batch-key-2"))

	if !has1 || !has2 {
		t.Error("Both batch keys should exist")
	}
}

func TestFSKVDirectoryStructure(t *testing.T) {
	dir, err := os.MkdirTemp("", "malt-fs-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(dir)

	kv, err := fs.New(dir)
	if err != nil {
		t.Fatalf("Failed to create fs kv: %v", err)
	}
	defer kv.Close()

	ctx := context.Background()

	// Put keys that should create different subdirectories
	keys := [][]byte{
		[]byte{0xab, 0xcd}, // hex: "abcd" -> subdir "ab"
		[]byte{0x12, 0x34}, // hex: "1234" -> subdir "12"
		[]byte{0xff, 0x00}, // hex: "ff00" -> subdir "ff"
	}

	for _, key := range keys {
		err = kv.Put(ctx, key, []byte("value"))
		if err != nil {
			t.Fatalf("Put failed: %v", err)
		}
	}

	// Check subdirectories exist
	expectedDirs := []string{"ab", "12", "ff"}
	for _, expectedDir := range expectedDirs {
		subdir := dir + "/" + expectedDir
		if _, err := os.Stat(subdir); os.IsNotExist(err) {
			t.Errorf("Subdirectory %s should exist", expectedDir)
		}
	}
}
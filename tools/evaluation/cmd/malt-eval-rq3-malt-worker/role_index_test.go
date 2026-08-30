package main

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestBlockRoleIndexPersistsExactCategoriesAndCleansUp(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	index, err := newBlockRoleIndex()
	if err != nil {
		t.Fatal(err)
	}
	directory := index.directory
	if err := index.checkAndRecord("bafy-role", "logical-changed-payload"); err != nil {
		t.Fatal(err)
	}
	if err := index.checkAndRecord("bafy-role", "logical-changed-payload"); err != nil {
		t.Fatalf("same category must be idempotent: %v", err)
	}
	if err := index.checkAndRecord("bafy-role", "cas-structural-metadata"); err == nil || !strings.Contains(err.Error(), "crosses mutually exclusive categories") {
		t.Fatalf("cross-category reuse error = %v", err)
	}
	if err := index.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(directory); !os.IsNotExist(err) {
		t.Fatalf("temporary role index remains after close: %v", err)
	}
	if err := index.close(); err != nil {
		t.Fatalf("close must be idempotent: %v", err)
	}
	if err := index.checkAndRecord("bafy-after-close", "logical-changed-payload"); err == nil {
		t.Fatal("closed role index accepted a write")
	}
}

func TestBlockRoleIndexLargeHistoryUsesBoundedHeapAndTemporaryDisk(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	index, err := newBlockRoleIndex()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = index.close() })
	directory := index.directory

	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	const entries = 400_000
	const batchSize = 1_024
	for start := 0; start < entries; start += batchSize {
		end := min(start+batchSize, entries)
		roles := make([]blockRole, end-start)
		for offset := range roles {
			var input [8]byte
			binary.LittleEndian.PutUint64(input[:], uint64(start+offset))
			digest := sha256.Sum256(input[:])
			roles[offset] = blockRole{key: hex.EncodeToString(digest[:]), category: "logical-changed-payload"}
		}
		if err := index.checkAndRecordBatch(roles); err != nil {
			t.Fatal(err)
		}
	}
	runtime.GC()
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	var heapGrowth uint64
	if after.HeapAlloc > before.HeapAlloc {
		heapGrowth = after.HeapAlloc - before.HeapAlloc
	}
	if heapGrowth > 32<<20 {
		t.Fatalf("role index retained %d heap bytes for %d unique CIDs", heapGrowth, entries)
	}

	var diskBytes int64
	if err := filepath.Walk(directory, func(_ string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.Mode().IsRegular() {
			diskBytes += info.Size()
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if diskBytes == 0 {
		t.Fatal("large role history did not use its declared process-temporary disk")
	}
	t.Logf("unique_cids=%d heap_growth_bytes=%d temporary_disk_bytes=%d", entries, heapGrowth, diskBytes)

	if err := index.close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(directory); !os.IsNotExist(err) {
		t.Fatalf("large temporary role index remains after close: %v", err)
	}
}

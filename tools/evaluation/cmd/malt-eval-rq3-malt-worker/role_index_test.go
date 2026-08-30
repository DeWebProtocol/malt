package main

import (
	"os"
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

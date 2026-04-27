package unixfs_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt/core/arctable/overwrite"
	casmock "github.com/dewebprotocol/malt/core/cas/mock"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/commitment/ipa"
	kvmemory "github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/layout/malt/unixfs"
	"github.com/dewebprotocol/malt/core/structure/list/tree"
	mappingradix "github.com/dewebprotocol/malt/core/structure/mapping/radix"
	cid "github.com/ipfs/go-cid"
)

func newLayout(t *testing.T, chunkSize int) *unixfs.Layout {
	t.Helper()

	kv := kvmemory.New()
	arcs, err := overwrite.NewArcTable(overwrite.WithKVStore(kv))
	if err != nil {
		t.Fatalf("overwrite.NewArcTable failed: %v", err)
	}
	scheme, err := ipa.NewScheme()
	if err != nil {
		t.Fatalf("ipa.NewScheme failed: %v", err)
	}
	maps, err := mappingradix.NewMap(scheme, arcs)
	if err != nil {
		t.Fatalf("radix.NewMap failed: %v", err)
	}
	lists, err := tree.NewList(scheme, arcs)
	if err != nil {
		t.Fatalf("tree.NewList failed: %v", err)
	}

	layout, err := unixfs.New(unixfs.Options{
		BucketID:  "unixfs-" + strings.ReplaceAll(t.Name(), "/", "-"),
		ChunkSize: chunkSize,
		Map:       maps,
		List:      lists,
		Blocks:    casmock.NewCAS(casmock.WithoutLatency()),
	})
	if err != nil {
		t.Fatalf("unixfs.New failed: %v", err)
	}
	return layout
}

func TestAddAndReadSmallFile(t *testing.T) {
	ctx := context.Background()
	layout := newLayout(t, 8)

	root, err := layout.AddFile(ctx, cid.Undef, "docs/hello.txt", []byte("hello"))
	if err != nil {
		t.Fatalf("AddFile failed: %v", err)
	}

	got, err := layout.ReadFile(ctx, root, "docs/hello.txt")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("ReadFile = %q, want hello", got)
	}

	resolution, err := layout.Resolve(ctx, root, "docs/hello.txt")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if len(resolution.Steps) != 3 {
		t.Fatalf("resolution step count = %d, want 3", len(resolution.Steps))
	}
	if codec.SemanticKindOf(resolution.Payload) != codec.SemanticKindUnknown {
		t.Fatalf("small file payload kind = %s, want unknown/raw", codec.SemanticKindOf(resolution.Payload))
	}
}

func TestAddAndReadLargeFileRange(t *testing.T) {
	ctx := context.Background()
	layout := newLayout(t, 4)
	data := []byte("abcdefghijklmnopqrstuvwxyz")

	root, err := layout.AddFile(ctx, cid.Undef, "blob.bin", data)
	if err != nil {
		t.Fatalf("AddFile failed: %v", err)
	}

	resolution, err := layout.Resolve(ctx, root, "blob.bin")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}
	if codec.SemanticKindOf(resolution.Payload) != codec.SemanticKindList {
		t.Fatalf("large file payload kind = %s, want list", codec.SemanticKindOf(resolution.Payload))
	}

	full, err := layout.ReadFile(ctx, root, "blob.bin")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if !bytes.Equal(full, data) {
		t.Fatalf("ReadFile mismatch: got %q want %q", full, data)
	}

	ranged, err := layout.ReadFileRange(ctx, root, "blob.bin", 3, 11)
	if err != nil {
		t.Fatalf("ReadFileRange failed: %v", err)
	}
	if !bytes.Equal(ranged, data[3:14]) {
		t.Fatalf("ReadFileRange mismatch: got %q want %q", ranged, data[3:14])
	}
}

func TestOverwritePreservesSibling(t *testing.T) {
	ctx := context.Background()
	layout := newLayout(t, 4)

	root, err := layout.AddFile(ctx, cid.Undef, "dir/a.txt", []byte("old-a"))
	if err != nil {
		t.Fatalf("AddFile(a) failed: %v", err)
	}
	root, err = layout.AddFile(ctx, root, "dir/b.txt", []byte("stable-b"))
	if err != nil {
		t.Fatalf("AddFile(b) failed: %v", err)
	}
	root, err = layout.AddFile(ctx, root, "dir/a.txt", []byte("new-a"))
	if err != nil {
		t.Fatalf("AddFile(overwrite a) failed: %v", err)
	}

	a, err := layout.ReadFile(ctx, root, "dir/a.txt")
	if err != nil {
		t.Fatalf("ReadFile(a) failed: %v", err)
	}
	if string(a) != "new-a" {
		t.Fatalf("a.txt = %q, want new-a", a)
	}

	b, err := layout.ReadFile(ctx, root, "dir/b.txt")
	if err != nil {
		t.Fatalf("ReadFile(b) failed: %v", err)
	}
	if string(b) != "stable-b" {
		t.Fatalf("b.txt = %q, want stable-b", b)
	}
}

func TestRejectsReservedPathSegment(t *testing.T) {
	ctx := context.Background()
	layout := newLayout(t, 8)

	if _, err := layout.AddFile(ctx, cid.Undef, "dir/@payload", []byte("bad")); err == nil {
		t.Fatal("expected AddFile to reject reserved path segment")
	}
}

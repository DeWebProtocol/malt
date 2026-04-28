package unixfs_test

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/dewebprotocol/malt/core/layout/malt/unixfs"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

func TestSmallFileMutationPlanIncludesMapPayload(t *testing.T) {
	ctx := context.Background()
	layout := newLayout(t, 8)

	root, err := layout.AddFile(ctx, cid.Undef, "hello.txt", []byte("hello"))
	if err != nil {
		t.Fatalf("AddFile failed: %v", err)
	}

	plan, err := layout.MutationPlanForPath(ctx, root, "hello.txt")
	if err != nil {
		t.Fatalf("MutationPlanForPath failed: %v", err)
	}
	if plan.BucketID == "" {
		t.Fatal("plan BucketID is empty")
	}
	if !plan.BaseRoot.Equals(root) {
		t.Fatalf("plan BaseRoot = %s, want %s", plan.BaseRoot, root)
	}
	if len(plan.Puts) != 1 {
		t.Fatalf("put count = %d, want 1", len(plan.Puts))
	}
	put := plan.Puts[0]
	if put.Kind != arcset.KindMap {
		t.Fatalf("put kind = %q, want map", put.Kind)
	}
	if put.ArcSet.Kind() != arcset.KindMap {
		t.Fatalf("arcset kind = %q, want map", put.ArcSet.Kind())
	}
	if !hasEntry(put.ArcSet, "@payload", arcset.TargetKindCAS) {
		t.Fatal("file map arcset missing CAS @payload binding")
	}
}

func TestLargeFileMutationPlanIncludesFileMapAndOrderedList(t *testing.T) {
	ctx := context.Background()
	layout := newLayout(t, 4)

	root, err := layout.AddFile(ctx, cid.Undef, "blob.bin", []byte("abcdefghijkl"))
	if err != nil {
		t.Fatalf("AddFile failed: %v", err)
	}

	plan, err := layout.MutationPlanForPath(ctx, root, "blob.bin")
	if err != nil {
		t.Fatalf("MutationPlanForPath failed: %v", err)
	}
	if len(plan.Puts) != 2 {
		t.Fatalf("put count = %d, want 2", len(plan.Puts))
	}
	if plan.Puts[0].Kind != arcset.KindMap {
		t.Fatalf("put 0 kind = %q, want map", plan.Puts[0].Kind)
	}
	if !hasEntry(plan.Puts[0].ArcSet, "@payload", arcset.TargetKindList) {
		t.Fatal("file map arcset missing list @payload binding")
	}
	if plan.Puts[1].Kind != arcset.KindList {
		t.Fatalf("put 1 kind = %q, want list", plan.Puts[1].Kind)
	}

	got := entryCoordinates(plan.Puts[1].ArcSet)
	want := []string{"0", "1", "2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("list coordinates = %#v, want %#v", got, want)
	}
	for _, entry := range plan.Puts[1].ArcSet.Entries() {
		if entry.Target.Kind() != arcset.TargetKindCAS {
			t.Fatalf("list entry %s target kind = %q, want cas", entry.Coordinate.String(), entry.Target.Kind())
		}
	}
}

func TestDirectoryMutationPlanSortsEntriesAndRejectsReservedPath(t *testing.T) {
	ctx := context.Background()
	layout := newLayout(t, 8)

	root, err := layout.AddFile(ctx, cid.Undef, "dir/b.txt", []byte("b"))
	if err != nil {
		t.Fatalf("AddFile(b) failed: %v", err)
	}
	root, err = layout.AddFile(ctx, root, "dir/a.txt", []byte("a"))
	if err != nil {
		t.Fatalf("AddFile(a) failed: %v", err)
	}

	if _, err := layout.MutationPlanForPath(ctx, root, "dir/@payload"); !errors.Is(err, unixfs.ErrReservedPath) {
		t.Fatalf("reserved path error = %v, want ErrReservedPath", err)
	}

	plan, err := layout.MutationPlanForPath(ctx, root, "dir")
	if err != nil {
		t.Fatalf("MutationPlanForPath(dir) failed: %v", err)
	}
	if len(plan.Puts) != 1 {
		t.Fatalf("put count = %d, want 1", len(plan.Puts))
	}
	got := entryCoordinates(plan.Puts[0].ArcSet)
	want := []string{"@payload", "@type", "a.txt", "b.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("directory map coordinates = %#v, want %#v", got, want)
	}
}

func hasEntry(set *arcset.CanonicalArcSet, coordinate string, targetKind arcset.TargetKind) bool {
	for _, entry := range set.Entries() {
		if entry.Coordinate.String() == coordinate && entry.Target.Kind() == targetKind {
			return true
		}
	}
	return false
}

func entryCoordinates(set *arcset.CanonicalArcSet) []string {
	entries := set.Entries()
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Coordinate.String())
	}
	return out
}

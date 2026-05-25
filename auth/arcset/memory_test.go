package arcset

import (
	"errors"
	"testing"

	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func testCID(t *testing.T, data string) cid.Cid {
	t.Helper()
	sum, err := mh.Sum([]byte(data), mh.SHA2_256, -1)
	if err != nil {
		t.Fatalf("mh.Sum failed: %v", err)
	}
	return cid.NewCidV1(cid.Raw, sum)
}

func TestNewArcSetCanonicalizesPaths(t *testing.T) {
	target := testCID(t, "target")
	arcs, err := NewArcSet(map[string]cid.Cid{
		"/a//b/": target,
	})
	if err != nil {
		t.Fatalf("NewArcSet failed: %v", err)
	}

	got, ok := arcs.Get(CanonicalizePath("a/b"))
	if !ok {
		t.Fatal("canonical path not found")
	}
	if !got.Equals(target) {
		t.Fatal("canonical path has wrong target")
	}
}

func TestNewArcSetRejectsEmptyCanonicalPath(t *testing.T) {
	_, err := NewArcSet(map[string]cid.Cid{
		"///": testCID(t, "target"),
	})
	if !errors.Is(err, ErrEmptyPath) {
		t.Fatalf("err = %v, want ErrEmptyPath", err)
	}
}

func TestNewArcSetRejectsConflictingDuplicatePath(t *testing.T) {
	_, err := NewArcSet(map[string]cid.Cid{
		"a/b":   testCID(t, "first"),
		"a//b/": testCID(t, "second"),
	})
	if !errors.Is(err, ErrDuplicatePath) {
		t.Fatalf("err = %v, want ErrDuplicatePath", err)
	}
}

func TestNewArcSetAllowsEquivalentDuplicatePath(t *testing.T) {
	target := testCID(t, "target")
	arcs, err := NewArcSet(map[string]cid.Cid{
		"a/b":   target,
		"a//b/": target,
	})
	if err != nil {
		t.Fatalf("NewArcSet failed: %v", err)
	}
	if arcs.Len() != 1 {
		t.Fatalf("Len = %d, want 1", arcs.Len())
	}
}

package memory

import (
	"testing"

	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// newTestCID creates a CID from data for testing.
func newTestCID(data []byte) cid.Cid {
	mhash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cid.Raw, mhash)
}

// === InMemoryArcSet Tests ===

func TestInMemoryArcSetSetAndGet(t *testing.T) {
	arcs := NewInMemoryArcSet()

	c1 := newTestCID([]byte("target1"))
	c2 := newTestCID([]byte("target2"))

	// Set and Get
	arcs.Set("a", c1)
	arcs.Set("b", c2)

	got, ok := arcs.Get("a")
	if !ok {
		t.Error("expected to find 'a'")
	}
	if !got.Equals(c1) {
		t.Error("wrong value for 'a'")
	}

	got, ok = arcs.Get("b")
	if !ok {
		t.Error("expected to find 'b'")
	}
	if !got.Equals(c2) {
		t.Error("wrong value for 'b'")
	}

	// Non-existent
	_, ok = arcs.Get("c")
	if ok {
		t.Error("expected not to find 'c'")
	}
}

func TestInMemoryArcSetUpdate(t *testing.T) {
	arcs := NewInMemoryArcSet()

	c1 := newTestCID([]byte("target1"))
	c2 := newTestCID([]byte("target2"))

	arcs.Set("key", c1)
	arcs.Set("key", c2) // Update

	got, ok := arcs.Get("key")
	if !ok {
		t.Error("expected to find 'key'")
	}
	if !got.Equals(c2) {
		t.Error("expected updated value")
	}
}

func TestInMemoryArcSetDelete(t *testing.T) {
	arcs := NewInMemoryArcSet()

	c1 := newTestCID([]byte("target1"))

	arcs.Set("a", c1)
	arcs.Delete("a")

	_, ok := arcs.Get("a")
	if ok {
		t.Error("expected 'a' to be deleted")
	}
}

func TestInMemoryArcSetLen(t *testing.T) {
	arcs := NewInMemoryArcSet()

	if arcs.Len() != 0 {
		t.Error("expected empty arc set")
	}

	arcs.Set("a", newTestCID([]byte("1")))
	arcs.Set("b", newTestCID([]byte("2")))
	arcs.Set("c", newTestCID([]byte("3")))

	if arcs.Len() != 3 {
		t.Errorf("expected 3, got %d", arcs.Len())
	}

	arcs.Delete("a")
	if arcs.Len() != 2 {
		t.Errorf("expected 2 after delete, got %d", arcs.Len())
	}
}

func TestInMemoryArcSetClear(t *testing.T) {
	arcs := NewInMemoryArcSet()

	arcs.Set("a", newTestCID([]byte("1")))
	arcs.Set("b", newTestCID([]byte("2")))

	arcs.Clear()

	if arcs.Len() != 0 {
		t.Error("expected empty after clear")
	}

	_, ok := arcs.Get("a")
	if ok {
		t.Error("expected 'a' to be gone after clear")
	}
}

func TestInMemoryArcSetIterate(t *testing.T) {
	arcs := NewInMemoryArcSet()

	c1 := newTestCID([]byte("target1"))
	c2 := newTestCID([]byte("target2"))
	c3 := newTestCID([]byte("target3"))

	arcs.Set("c", c3)
	arcs.Set("a", c1)
	arcs.Set("b", c2)

	// Iterate should return sorted paths
	it := arcs.Iterate()

	paths := []string{}
	targets := []cid.Cid{}
	for {
		path, target, ok := it.Next()
		if !ok {
			break
		}
		paths = append(paths, path)
		targets = append(targets, target)
	}

	if len(paths) != 3 {
		t.Errorf("expected 3 paths, got %d", len(paths))
	}

	// Check sorted order
	if paths[0] != "a" || paths[1] != "b" || paths[2] != "c" {
		t.Errorf("expected sorted order, got %v", paths)
	}

	// Verify targets
	if !targets[0].Equals(c1) || !targets[1].Equals(c2) || !targets[2].Equals(c3) {
		t.Error("targets don't match expected values")
	}

	// Check iterator error
	if it.Err() != nil {
		t.Errorf("unexpected iterator error: %v", it.Err())
	}
}

func TestInMemoryArcSetIterateEmpty(t *testing.T) {
	arcs := NewInMemoryArcSet()
	it := arcs.Iterate()

	_, _, ok := it.Next()
	if ok {
		t.Error("expected empty iterator to return false immediately")
	}
}

func TestInMemoryArcSetIterateSnapshot(t *testing.T) {
	arcs := NewInMemoryArcSet()

	arcs.Set("a", newTestCID([]byte("1")))
	arcs.Set("b", newTestCID([]byte("2")))

	it := arcs.Iterate()

	// Modify arcs while iterating
	arcs.Set("c", newTestCID([]byte("3")))
	arcs.Delete("a")

	// Iterator should see original snapshot
	count := 0
	for {
		_, _, ok := it.Next()
		if !ok {
			break
		}
		count++
	}

	if count != 2 {
		t.Errorf("expected 2 from snapshot, got %d", count)
	}

	// New iterator should see changes
	it2 := arcs.Iterate()
	count2 := 0
	for {
		_, _, ok := it2.Next()
		if !ok {
			break
		}
		count2++
	}

	if count2 != 2 {
		t.Errorf("expected 2 after changes (b and c), got %d", count2)
	}
}
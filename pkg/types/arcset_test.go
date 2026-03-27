package types

import (
	"testing"
)

func TestNewArcSet(t *testing.T) {
	as := NewArcSet()
	if as == nil {
		t.Fatal("NewArcSet returned nil")
	}
	if as.Size() != 0 {
		t.Errorf("New ArcSet size = %d, want 0", as.Size())
	}
	if !as.IsEmpty() {
		t.Error("New ArcSet should be empty")
	}
}

func TestArcSetAddGet(t *testing.T) {
	as := NewArcSet()
	cid, _ := NewCID([]byte("target"))

	// Add arc
	as.Add("link", cid)

	// Check size
	if as.Size() != 1 {
		t.Errorf("ArcSet size = %d, want 1", as.Size())
	}

	// Get arc
	got, ok := as.Get("link")
	if !ok {
		t.Error("Get failed for existing path")
	}
	if !got.Equals(cid) {
		t.Error("Get returned wrong CID")
	}

	// Get non-existent arc
	_, ok = as.Get("nonexistent")
	if ok {
		t.Error("Get should fail for non-existent path")
	}
}

func TestArcSetRemove(t *testing.T) {
	as := NewArcSet()
	cid, _ := NewCID([]byte("target"))

	as.Add("link", cid)
	if as.Size() != 1 {
		t.Errorf("ArcSet size = %d, want 1", as.Size())
	}

	// Remove arc
	removed := as.Remove("link")
	if !removed {
		t.Error("Remove should return true for existing path")
	}
	if as.Size() != 0 {
		t.Errorf("ArcSet size after remove = %d, want 0", as.Size())
	}

	// Remove non-existent arc
	removed = as.Remove("nonexistent")
	if removed {
		t.Error("Remove should return false for non-existent path")
	}
}

func TestArcSetHas(t *testing.T) {
	as := NewArcSet()
	cid, _ := NewCID([]byte("target"))

	as.Add("link", cid)

	if !as.Has("link") {
		t.Error("Has should return true for existing path")
	}

	if as.Has("nonexistent") {
		t.Error("Has should return false for non-existent path")
	}
}

func TestArcSetPairs(t *testing.T) {
	as := NewArcSet()
	cid1, _ := NewCID([]byte("target1"))
	cid2, _ := NewCID([]byte("target2"))

	as.Add("b", cid1)
	as.Add("a", cid2)

	pairs := as.Pairs()
	if len(pairs) != 2 {
		t.Errorf("Pairs length = %d, want 2", len(pairs))
	}

	// Check sorted order
	if pairs[0].Path != "a" {
		t.Errorf("First pair path = %s, want a", pairs[0].Path)
	}
	if pairs[1].Path != "b" {
		t.Errorf("Second pair path = %s, want b", pairs[1].Path)
	}
}

func TestArcSetClone(t *testing.T) {
	as := NewArcSet()
	cid, _ := NewCID([]byte("target"))
	as.Add("link", cid)

	clone := as.Clone()
	if !clone.Equals(as) {
		t.Error("Clone should equal original")
	}

	// Modify clone should not affect original
	cid2, _ := NewCID([]byte("target2"))
	clone.Add("link2", cid2)

	if as.Has("link2") {
		t.Error("Modifying clone should not affect original")
	}
}

func TestArcSetMerge(t *testing.T) {
	as1 := NewArcSet()
	as2 := NewArcSet()
	cid1, _ := NewCID([]byte("target1"))
	cid2, _ := NewCID([]byte("target2"))

	as1.Add("link1", cid1)
	as2.Add("link2", cid2)

	as1.Merge(as2)

	if as1.Size() != 2 {
		t.Errorf("Merged size = %d, want 2", as1.Size())
	}
	if !as1.Has("link2") {
		t.Error("Merged set should have link2")
	}
}

func TestArcSetUpdate(t *testing.T) {
	as := NewArcSet()
	cid1, _ := NewCID([]byte("target1"))
	cid2, _ := NewCID([]byte("target2"))

	as.Add("link", cid1)

	oldCID, ok := as.Update("link", cid2)
	if !ok {
		t.Error("Update should return true for existing path")
	}
	if !oldCID.Equals(cid1) {
		t.Error("Update should return old CID")
	}

	got, _ := as.Get("link")
	if !got.Equals(cid2) {
		t.Error("Update should change the CID")
	}
}

func TestArcSetDiff(t *testing.T) {
	as1 := NewArcSet()
	as2 := NewArcSet()
	cid1, _ := NewCID([]byte("target1"))
	cid2, _ := NewCID([]byte("target2"))
	cid3, _ := NewCID([]byte("target3"))

	as1.Add("a", cid1)
	as1.Add("b", cid2)

	as2.Add("b", cid3) // changed
	as2.Add("c", cid1) // added
	// "a" is removed

	added, removed, changed := as1.Diff(as2)

	if len(added) != 1 || added[0] != "c" {
		t.Errorf("Added = %v, want [c]", added)
	}
	if len(removed) != 1 || removed[0] != "a" {
		t.Errorf("Removed = %v, want [a]", removed)
	}
	if len(changed) != 1 || changed[0] != "b" {
		t.Errorf("Changed = %v, want [b]", changed)
	}
}

func TestArcSetJSON(t *testing.T) {
	as := NewArcSet()
	cid, _ := NewCID([]byte("target"))
	as.Add("link", cid)

	// Marshal
	jsonData, err := as.MarshalJSON()
	if err != nil {
		t.Fatalf("MarshalJSON failed: %v", err)
	}

	// Unmarshal
	parsed := NewArcSet()
	if err := parsed.UnmarshalJSON(jsonData); err != nil {
		t.Fatalf("UnmarshalJSON failed: %v", err)
	}

	if !parsed.Equals(as) {
		t.Error("Unmarshalled ArcSet should equal original")
	}
}

func TestNewArcSetFromPairs(t *testing.T) {
	cid1, _ := NewCID([]byte("target1"))
	cid2, _ := NewCID([]byte("target2"))

	as := NewArcSetFromPairs(
		NewArcPair("link1", cid1),
		NewArcPair("link2", cid2),
	)

	if as.Size() != 2 {
		t.Errorf("ArcSet size = %d, want 2", as.Size())
	}
	if !as.Has("link1") || !as.Has("link2") {
		t.Error("ArcSet should have both links")
	}
}
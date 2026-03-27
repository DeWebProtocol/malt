package memory

import (
	"testing"

	"github.com/dewebprotocol/malt/pkg/commitment"
	"github.com/dewebprotocol/malt/pkg/storage"
	"github.com/dewebprotocol/malt/pkg/types"
)

func TestMemoryStoragePutGet(t *testing.T) {
	s := New()
	defer s.Close()

	comm := commitment.Commitment("test-commitment")
	cid, _ := types.NewCID([]byte("target"))

	// Put
	err := s.Put(comm, "link", cid)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Get
	got, err := s.Get(comm, "link")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if !got.Equals(cid) {
		t.Error("Get returned wrong CID")
	}
}

func TestMemoryStorageGetNotFound(t *testing.T) {
	s := New()
	defer s.Close()

	comm := commitment.Commitment("test-commitment")

	_, err := s.Get(comm, "link")
	if err == nil {
		t.Error("Get should fail for non-existent entry")
	}
}

func TestMemoryStorageDelete(t *testing.T) {
	s := New()
	defer s.Close()

	comm := commitment.Commitment("test-commitment")
	cid, _ := types.NewCID([]byte("target"))

	s.Put(comm, "link", cid)

	// Delete
	err := s.Delete(comm, "link")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Get should fail after delete
	_, err = s.Get(comm, "link")
	if err == nil {
		t.Error("Get should fail after delete")
	}
}

func TestMemoryStorageHas(t *testing.T) {
	s := New()
	defer s.Close()

	comm := commitment.Commitment("test-commitment")
	cid, _ := types.NewCID([]byte("target"))

	has, _ := s.Has(comm, "link")
	if has {
		t.Error("Has should return false for non-existent entry")
	}

	s.Put(comm, "link", cid)

	has, _ = s.Has(comm, "link")
	if !has {
		t.Error("Has should return true for existing entry")
	}
}

func TestMemoryStorageBatch(t *testing.T) {
	s := New()
	defer s.Close()

	comm := commitment.Commitment("test-commitment")
	cid1, _ := types.NewCID([]byte("target1"))
	cid2, _ := types.NewCID([]byte("target2"))

	ops := []storage.Operation{
		{Type: storage.OpPut, Entry: storage.NewEATEntry(comm, "link1", cid1)},
		{Type: storage.OpPut, Entry: storage.NewEATEntry(comm, "link2", cid2)},
	}

	err := s.Batch(ops)
	if err != nil {
		t.Fatalf("Batch failed: %v", err)
	}

	// Verify entries exist
	has, _ := s.Has(comm, "link1")
	if !has {
		t.Error("link1 should exist")
	}
	has, _ = s.Has(comm, "link2")
	if !has {
		t.Error("link2 should exist")
	}
}

func TestMemoryStorageLineage(t *testing.T) {
	s := New()
	defer s.Close()

	parent := commitment.Commitment("parent")
	child := commitment.Commitment("child")

	// Set parent
	err := s.SetParent(child, parent)
	if err != nil {
		t.Fatalf("SetParent failed: %v", err)
	}

	// Get parent
	got, err := s.GetParent(child)
	if err != nil {
		t.Fatalf("GetParent failed: %v", err)
	}

	if !got.Equals(parent) {
		t.Error("GetParent returned wrong commitment")
	}

	// Get lineage
	lineage, err := s.GetLineage(child)
	if err != nil {
		t.Fatalf("GetLineage failed: %v", err)
	}

	if len(lineage) != 2 {
		t.Errorf("Lineage length = %d, want 2", len(lineage))
	}

	if !lineage[0].Equals(child) {
		t.Error("First lineage entry should be child")
	}

	if !lineage[1].Equals(parent) {
		t.Error("Second lineage entry should be parent")
	}
}

func TestMemoryStorageStats(t *testing.T) {
	s := New()
	defer s.Close()

	comm := commitment.Commitment("test-commitment")
	cid, _ := types.NewCID([]byte("target"))

	stats := s.Stats()
	if stats.TotalEntries != 0 {
		t.Errorf("Initial total entries = %d, want 0", stats.TotalEntries)
	}

	s.Put(comm, "link", cid)

	stats = s.Stats()
	if stats.TotalEntries != 1 {
		t.Errorf("Total entries after put = %d, want 1", stats.TotalEntries)
	}
}
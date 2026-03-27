// Package badger_test provides tests for the BadgerDB storage implementation.
package badger_test

import (
	"testing"

	"github.com/dewebprotocol/malt/pkg/commitment"
	"github.com/dewebprotocol/malt/pkg/storage"
	"github.com/dewebprotocol/malt/pkg/storage/badger"
	"github.com/dewebprotocol/malt/pkg/types"
)

func TestBadgerStorage_BasicOperations(t *testing.T) {
	s, err := badger.NewInMemory()
	if err != nil {
		t.Fatalf("Failed to create BadgerDB storage: %v", err)
	}
	defer s.Close()

	// Create test data
	comm := commitment.Commitment([]byte("test_commitment_123456789012345"))
	path := types.Path("test_path")
	cid, _ := types.NewCID([]byte("test_data"))

	// Test Put
	err = s.Put(comm, path, cid)
	if err != nil {
		t.Fatalf("Failed to put: %v", err)
	}

	// Test Get
	retrieved, err := s.Get(comm, path)
	if err != nil {
		t.Fatalf("Failed to get: %v", err)
	}

	if !retrieved.Equals(cid) {
		t.Errorf("Retrieved CID doesn't match: expected %s, got %s", cid, retrieved)
	}

	// Test Has
	has, err := s.Has(comm, path)
	if err != nil {
		t.Fatalf("Failed to check has: %v", err)
	}
	if !has {
		t.Error("Expected Has to return true")
	}

	// Test Delete
	err = s.Delete(comm, path)
	if err != nil {
		t.Fatalf("Failed to delete: %v", err)
	}

	// Verify deleted
	has, err = s.Has(comm, path)
	if err != nil {
		t.Fatalf("Failed to check has after delete: %v", err)
	}
	if has {
		t.Error("Expected Has to return false after delete")
	}
}

func TestBadgerStorage_Batch(t *testing.T) {
	s, err := badger.NewInMemory()
	if err != nil {
		t.Fatalf("Failed to create BadgerDB storage: %v", err)
	}
	defer s.Close()

	comm := commitment.Commitment([]byte("test_commitment_batch_12345678"))

	// Create batch operations
	ops := make([]storage.Operation, 3)
	for i := 0; i < 3; i++ {
		cid, _ := types.NewCID([]byte{byte(i)})
		ops[i] = storage.Operation{
			Type: storage.OpPut,
			Entry: storage.EATEntry{
				Commitment: comm,
				Path:       types.Path("batch_path_" + string(rune('a'+i))),
				Target:     cid,
			},
		}
	}

	// Execute batch
	err = s.Batch(ops)
	if err != nil {
		t.Fatalf("Failed to execute batch: %v", err)
	}

	// Verify all entries
	for i := 0; i < 3; i++ {
		path := types.Path("batch_path_" + string(rune('a'+i)))
		has, err := s.Has(comm, path)
		if err != nil {
			t.Fatalf("Failed to check has for path %s: %v", path, err)
		}
		if !has {
			t.Errorf("Expected entry for path %s to exist", path)
		}
	}
}

func TestBadgerStorage_Lineage(t *testing.T) {
	s, err := badger.NewInMemory()
	if err != nil {
		t.Fatalf("Failed to create BadgerDB storage: %v", err)
	}
	defer s.Close()

	parent := commitment.Commitment([]byte("parent_commitment_12345678901"))
	child := commitment.Commitment([]byte("child_commitment_123456789012"))
	grandchild := commitment.Commitment([]byte("grandchild_commitment_1234567"))

	// Set up lineage
	err = s.SetParent(child, parent)
	if err != nil {
		t.Fatalf("Failed to set parent: %v", err)
	}

	err = s.SetParent(grandchild, child)
	if err != nil {
		t.Fatalf("Failed to set parent for grandchild: %v", err)
	}

	// Test GetParent
	retrievedParent, err := s.GetParent(child)
	if err != nil {
		t.Fatalf("Failed to get parent: %v", err)
	}

	if string(retrievedParent) != string(parent) {
		t.Errorf("Parent mismatch: expected %s, got %s", parent, retrievedParent)
	}

	// Test GetLineage
	lineage, err := s.GetLineage(grandchild)
	if err != nil {
		t.Fatalf("Failed to get lineage: %v", err)
	}

	if len(lineage) != 3 {
		t.Errorf("Expected lineage length 3, got %d", len(lineage))
	}

	// Verify order: grandchild -> child -> parent
	if string(lineage[0]) != string(grandchild) {
		t.Error("First in lineage should be grandchild")
	}
	if string(lineage[1]) != string(child) {
		t.Error("Second in lineage should be child")
	}
	if string(lineage[2]) != string(parent) {
		t.Error("Third in lineage should be parent")
	}
}

func TestBadgerStorage_Iterate(t *testing.T) {
	s, err := badger.NewInMemory()
	if err != nil {
		t.Fatalf("Failed to create BadgerDB storage: %v", err)
	}
	defer s.Close()

	comm := commitment.Commitment([]byte("test_commitment_iter_1234567890"))

	// Add multiple entries
	for i := 0; i < 5; i++ {
		cid, _ := types.NewCID([]byte{byte(i)})
		err = s.Put(comm, types.Path("path_"+string(rune('a'+i))), cid)
		if err != nil {
			t.Fatalf("Failed to put entry %d: %v", i, err)
		}
	}

	// Iterate
	iter, err := s.Iterate(comm)
	if err != nil {
		t.Fatalf("Failed to iterate: %v", err)
	}
	defer iter.Close()

	count := 0
	for iter.Next() {
		entry := iter.Entry()
		if entry.Commitment == nil {
			t.Error("Entry commitment should not be nil")
		}
		count++
	}

	if iter.Err() != nil {
		t.Fatalf("Iterator error: %v", iter.Err())
	}

	if count != 5 {
		t.Errorf("Expected 5 entries, got %d", count)
	}
}

func TestBadgerStorage_Stats(t *testing.T) {
	s, err := badger.NewInMemory()
	if err != nil {
		t.Fatalf("Failed to create BadgerDB storage: %v", err)
	}
	defer s.Close()

	stats := s.Stats()
	// Stats should return something (could be 0 for in-memory)
	if stats.SizeBytes < 0 {
		t.Error("SizeBytes should not be negative")
	}
}

func TestBadgerStorage_NotFound(t *testing.T) {
	s, err := badger.NewInMemory()
	if err != nil {
		t.Fatalf("Failed to create BadgerDB storage: %v", err)
	}
	defer s.Close()

	comm := commitment.Commitment([]byte("nonexistent_commitment_123456789"))
	path := types.Path("nonexistent_path")

	_, err = s.Get(comm, path)
	if err != storage.ErrNotFound {
		t.Errorf("Expected ErrNotFound, got: %v", err)
	}
}
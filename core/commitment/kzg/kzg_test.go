package kzg_test

import (
	"testing"

	"github.com/dewebprotocol/malt/core/commitment"
	"github.com/dewebprotocol/malt/core/commitment/kzg"
)

func TestKZGProveIndexRestartSafe(t *testing.T) {
	first, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	values := []commitment.Cell{
		commitment.NewCell([]byte("slot0")),
		commitment.NewCell([]byte("slot1")),
	}
	root, err := first.CommitValues(values)
	if err != nil {
		t.Fatalf("CommitValues failed: %v", err)
	}

	second, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	value, proof, err := second.ProveIndex(root, values, 1)
	if err != nil {
		t.Fatalf("ProveIndex failed: %v", err)
	}
	if !value.Equal(values[1]) {
		t.Fatalf("unexpected value %x", value)
	}

	ok, err := second.VerifyIndex(root, 1, values[1], proof)
	if err != nil {
		t.Fatalf("VerifyIndex failed: %v", err)
	}
	if !ok {
		t.Fatal("expected proof to verify")
	}

	wrong := commitment.NewCell([]byte("wrong"))
	ok, err = second.VerifyIndex(root, 1, wrong, proof)
	if err != nil {
		t.Fatalf("VerifyIndex(wrong) failed: %v", err)
	}
	if ok {
		t.Fatal("expected wrong value verification to fail")
	}
}

func TestKZGBatchProveRestartSafe(t *testing.T) {
	first, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	values := []commitment.Cell{
		commitment.NewCell([]byte("slot0")),
		commitment.NewCell([]byte("slot1")),
		commitment.NewCell([]byte("slot2")),
	}
	root, err := first.CommitValues(values)
	if err != nil {
		t.Fatalf("CommitValues failed: %v", err)
	}

	second, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	indices := []uint64{1, 2}
	proved, proof, err := second.BatchProve(root, values, indices)
	if err != nil {
		t.Fatalf("BatchProve failed: %v", err)
	}
	if len(proved) != len(indices) {
		t.Fatalf("unexpected proved length: %d", len(proved))
	}
	if !proved[0].Equal(values[1]) || !proved[1].Equal(values[2]) {
		t.Fatalf("unexpected proved values: %x %x", proved[0], proved[1])
	}

	ok, err := second.BatchVerify(root, indices, []commitment.Cell{values[1], values[2]}, proof)
	if err != nil {
		t.Fatalf("BatchVerify failed: %v", err)
	}
	if !ok {
		t.Fatal("expected batch proof to verify")
	}

	ok, err = second.BatchVerify(root, indices, []commitment.Cell{values[1], commitment.NewCell([]byte("wrong"))}, proof)
	if err != nil {
		t.Fatalf("BatchVerify(wrong) failed: %v", err)
	}
	if ok {
		t.Fatal("expected wrong batch value verification to fail")
	}
}

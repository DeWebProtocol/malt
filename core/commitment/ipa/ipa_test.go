package ipa_test

import (
	"testing"

	"github.com/dewebprotocol/malt/core/commitment"
	"github.com/dewebprotocol/malt/core/commitment/ipa"
)

func TestIPAProveIndexRestartSafe(t *testing.T) {
	first, err := ipa.NewScheme()
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

	second, err := ipa.NewScheme()
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

	value0, proof0, err := second.ProveIndex(root, values, 0)
	if err != nil {
		t.Fatalf("ProveIndex(0) failed: %v", err)
	}
	if !value0.Equal(values[0]) {
		t.Fatalf("unexpected value at index 0: %x", value0)
	}

	ok, err = second.VerifyIndex(root, 0, values[0], proof0)
	if err != nil {
		t.Fatalf("VerifyIndex(0) failed: %v", err)
	}
	if !ok {
		t.Fatal("expected index 0 proof to verify")
	}
}

func TestIPAReplaceIndexRestartSafe(t *testing.T) {
	first, err := ipa.NewScheme()
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

	second, err := ipa.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	newValue := commitment.NewCell([]byte("slot1-new"))
	newRoot, err := second.ReplaceIndex(root, values, 1, values[1], newValue)
	if err != nil {
		t.Fatalf("ReplaceIndex failed after restart: %v", err)
	}

	updatedValues := []commitment.Cell{values[0], newValue}
	third, err := ipa.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	value, proof, err := third.ProveIndex(newRoot, updatedValues, 1)
	if err != nil {
		t.Fatalf("ProveIndex on updated root failed: %v", err)
	}
	if !value.Equal(newValue) {
		t.Fatalf("unexpected updated value %x", value)
	}

	ok, err := third.VerifyIndex(newRoot, 1, newValue, proof)
	if err != nil {
		t.Fatalf("VerifyIndex on updated root failed: %v", err)
	}
	if !ok {
		t.Fatal("expected updated proof to verify")
	}
}

func TestIPABatchProveRestartSafe(t *testing.T) {
	first, err := ipa.NewScheme()
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

	second, err := ipa.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	indices := []uint64{0, 2}
	proved, proof, err := second.BatchProve(root, values, indices)
	if err != nil {
		t.Fatalf("BatchProve failed: %v", err)
	}
	if len(proved) != len(indices) {
		t.Fatalf("unexpected proved length: %d", len(proved))
	}
	if !proved[0].Equal(values[0]) || !proved[1].Equal(values[2]) {
		t.Fatalf("unexpected proved values: %x %x", proved[0], proved[1])
	}

	ok, err := second.BatchVerify(root, indices, []commitment.Cell{values[0], values[2]}, proof)
	if err != nil {
		t.Fatalf("BatchVerify failed: %v", err)
	}
	if !ok {
		t.Fatal("expected batch proof to verify")
	}

	ok, err = second.BatchVerify(root, indices, []commitment.Cell{values[0], commitment.NewCell([]byte("wrong"))}, proof)
	if err != nil {
		t.Fatalf("BatchVerify(wrong) failed: %v", err)
	}
	if ok {
		t.Fatal("expected wrong batch value verification to fail")
	}
}

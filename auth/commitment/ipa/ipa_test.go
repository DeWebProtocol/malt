package ipa_test

import (
	"testing"

	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/auth/commitment/ipa"
)

func TestIPAProveIsStateless(t *testing.T) {
	scheme, err := ipa.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	values := []commitment.Cell{
		commitment.NewCell([]byte("slot0")),
		commitment.NewCell([]byte("slot1")),
		commitment.NewCell([]byte("slot2")),
	}
	root, err := scheme.Commit(values)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	provedRoot, value, proof, err := scheme.Prove(values, 2)
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}
	if !provedRoot.Equals(root) {
		t.Fatalf("unexpected recomputed root %s", provedRoot)
	}
	if !value.Equal(values[2]) {
		t.Fatalf("unexpected value %x", value)
	}

	ok, err := scheme.VerifyIndex(root, 2, values[2], proof)
	if err != nil {
		t.Fatalf("VerifyIndex failed: %v", err)
	}
	if !ok {
		t.Fatal("expected proof to verify")
	}

	wrong := commitment.NewCell([]byte("wrong"))
	ok, err = scheme.VerifyIndex(root, 2, wrong, proof)
	if err != nil {
		t.Fatalf("VerifyIndex(wrong) failed: %v", err)
	}
	if ok {
		t.Fatal("expected wrong value verification to fail")
	}

	_, value0, proof0, err := scheme.Prove(values, 0)
	if err != nil {
		t.Fatalf("Prove(0) failed: %v", err)
	}
	if !value0.Equal(values[0]) {
		t.Fatalf("unexpected value at index 0: %x", value0)
	}

	ok, err = scheme.VerifyIndex(root, 0, values[0], proof0)
	if err != nil {
		t.Fatalf("VerifyIndex(0) failed: %v", err)
	}
	if !ok {
		t.Fatal("expected index 0 proof to verify")
	}

	_, value1, proof1, err := scheme.Prove(values, 1)
	if err != nil {
		t.Fatalf("Prove(1) failed: %v", err)
	}
	if !value1.Equal(values[1]) {
		t.Fatalf("unexpected value at index 1: %x", value1)
	}

	ok, err = scheme.VerifyIndex(root, 1, values[1], proof1)
	if err != nil {
		t.Fatalf("VerifyIndex(1) failed: %v", err)
	}
	if !ok {
		t.Fatal("expected index 1 proof to verify")
	}

	ok, err = scheme.VerifyProof(root, values[2], proof)
	if err != nil {
		t.Fatalf("VerifyProof failed: %v", err)
	}
	if !ok {
		t.Fatal("expected VerifyProof to verify")
	}
}

func TestIPAReplaceIsStateless(t *testing.T) {
	scheme, err := ipa.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	values := []commitment.Cell{
		commitment.NewCell([]byte("slot0")),
		commitment.NewCell([]byte("slot1")),
	}
	root, err := scheme.Commit(values)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	newValue := commitment.NewCell([]byte("slot1-new"))
	newRoot, err := scheme.Replace(values, 1, values[1], newValue)
	if err != nil {
		t.Fatalf("Replace failed: %v", err)
	}
	if newRoot.Equals(root) {
		t.Fatal("expected replacement to change root")
	}

	updatedValues := []commitment.Cell{values[0], newValue}
	recomputedRoot, err := scheme.Commit(updatedValues)
	if err != nil {
		t.Fatalf("Commit(updated) failed: %v", err)
	}
	if !recomputedRoot.Equals(newRoot) {
		t.Fatalf("updated root mismatch: %s != %s", recomputedRoot, newRoot)
	}

	provedRoot, value, proof, err := scheme.Prove(updatedValues, 1)
	if err != nil {
		t.Fatalf("Prove on updated values failed: %v", err)
	}
	if !provedRoot.Equals(newRoot) {
		t.Fatalf("unexpected updated root %s", provedRoot)
	}
	if !value.Equal(newValue) {
		t.Fatalf("unexpected updated value %x", value)
	}

	ok, err := scheme.VerifyIndex(newRoot, 1, newValue, proof)
	if err != nil {
		t.Fatalf("VerifyIndex on updated root failed: %v", err)
	}
	if !ok {
		t.Fatal("expected updated proof to verify")
	}
}

func TestIPABatchProveIsStateless(t *testing.T) {
	scheme, err := ipa.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	values := []commitment.Cell{
		commitment.NewCell([]byte("slot0")),
		commitment.NewCell([]byte("slot1")),
		commitment.NewCell([]byte("slot2")),
	}
	root, err := scheme.Commit(values)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	indices := []uint64{0, 2}
	provedRoot, proved, proof, err := scheme.BatchProve(values, indices)
	if err != nil {
		t.Fatalf("BatchProve failed: %v", err)
	}
	if !provedRoot.Equals(root) {
		t.Fatalf("unexpected batch root %s", provedRoot)
	}
	if len(proved) != len(indices) {
		t.Fatalf("unexpected proved length: %d", len(proved))
	}
	if !proved[0].Equal(values[0]) || !proved[1].Equal(values[2]) {
		t.Fatalf("unexpected proved values: %x %x", proved[0], proved[1])
	}

	ok, err := scheme.BatchVerify(root, indices, []commitment.Cell{values[0], values[2]}, proof)
	if err != nil {
		t.Fatalf("BatchVerify failed: %v", err)
	}
	if !ok {
		t.Fatal("expected batch proof to verify")
	}

	ok, err = scheme.BatchVerify(root, indices, []commitment.Cell{values[0], commitment.NewCell([]byte("wrong"))}, proof)
	if err != nil {
		t.Fatalf("BatchVerify(wrong) failed: %v", err)
	}
	if ok {
		t.Fatal("expected wrong batch value verification to fail")
	}
}

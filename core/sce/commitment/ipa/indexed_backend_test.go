package ipa_test

import (
	"testing"

	"github.com/dewebprotocol/malt/core/sce/commitment"
	"github.com/dewebprotocol/malt/core/sce/commitment/ipa"
)

func TestIPAIndexedBackendRestartSafe(t *testing.T) {
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

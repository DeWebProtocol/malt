package kzg_test

import (
	"testing"

	"github.com/dewebprotocol/malt/core/sce/commitment"
	"github.com/dewebprotocol/malt/core/sce/commitment/kzg"
)

func TestKZGIndexedBackendRestartSafe(t *testing.T) {
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

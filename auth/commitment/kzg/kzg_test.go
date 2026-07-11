package kzg_test

import (
	"encoding/binary"
	"testing"

	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/auth/commitment/kzg"
)

func TestKZGProveIsStateless(t *testing.T) {
	scheme, err := kzg.NewScheme()
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

	provedRoot, value, proof, err := scheme.Prove(values, 1)
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}
	if !provedRoot.Equals(root) {
		t.Fatalf("unexpected recomputed root %s", provedRoot)
	}
	if !value.Equal(values[1]) {
		t.Fatalf("unexpected value %x", value)
	}

	ok, err := scheme.VerifyIndex(root, 1, values[1], proof)
	if err != nil {
		t.Fatalf("VerifyIndex failed: %v", err)
	}
	if !ok {
		t.Fatal("expected proof to verify")
	}

	wrong := commitment.NewCell([]byte("wrong"))
	ok, err = scheme.VerifyIndex(root, 1, wrong, proof)
	if err != nil {
		t.Fatalf("VerifyIndex(wrong) failed: %v", err)
	}
	if ok {
		t.Fatal("expected wrong value verification to fail")
	}
}

func TestKZGBatchProveIsStateless(t *testing.T) {
	scheme, err := kzg.NewScheme()
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

	indices := []uint64{1, 2}
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
	if !proved[0].Equal(values[1]) || !proved[1].Equal(values[2]) {
		t.Fatalf("unexpected proved values: %x %x", proved[0], proved[1])
	}

	ok, err := scheme.BatchVerify(root, indices, []commitment.Cell{values[1], values[2]}, proof)
	if err != nil {
		t.Fatalf("BatchVerify failed: %v", err)
	}
	if !ok {
		t.Fatal("expected batch proof to verify")
	}

	ok, err = scheme.BatchVerify(root, indices, []commitment.Cell{values[1], commitment.NewCell([]byte("wrong"))}, proof)
	if err != nil {
		t.Fatalf("BatchVerify(wrong) failed: %v", err)
	}
	if ok {
		t.Fatal("expected wrong batch value verification to fail")
	}
}

func TestKZGVerifyRejectsMalformedProofMetadata(t *testing.T) {
	scheme, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}
	values := []commitment.Cell{commitment.NewCell([]byte("slot0"))}
	root, value, proof, err := scheme.Prove(values, 0)
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}

	outOfRange := append([]byte(nil), proof...)
	binary.BigEndian.PutUint32(outOfRange[80:84], uint32(kzg.MaxValues))
	if ok, err := scheme.VerifyProof(root, value, outOfRange); err == nil || ok {
		t.Fatalf("VerifyProof(out-of-range index) = %v, %v; want false, error", ok, err)
	}
	if ok, err := scheme.VerifyIndex(root, 0, value, outOfRange); err == nil || ok {
		t.Fatalf("VerifyIndex(proof index out of range) = %v, %v; want false, error", ok, err)
	}

	if ok, err := scheme.VerifyIndex(root, kzg.MaxValues, value, proof); err == nil || ok {
		t.Fatalf("VerifyIndex(out-of-range index) = %v, %v; want false, error", ok, err)
	}

	withTrailingData := append(append([]byte(nil), proof...), 0)
	if ok, err := scheme.VerifyProof(root, value, withTrailingData); err == nil || ok {
		t.Fatalf("VerifyProof(trailing data) = %v, %v; want false, error", ok, err)
	}
}

func FuzzKZGVerifyProofDoesNotPanic(f *testing.F) {
	scheme, err := kzg.NewScheme()
	if err != nil {
		f.Fatalf("NewScheme failed: %v", err)
	}
	values := []commitment.Cell{commitment.NewCell([]byte("slot0"))}
	root, value, proof, err := scheme.Prove(values, 0)
	if err != nil {
		f.Fatalf("Prove failed: %v", err)
	}

	outOfRange := append([]byte(nil), proof...)
	binary.BigEndian.PutUint32(outOfRange[80:84], uint32(kzg.MaxValues))
	f.Add(proof)
	f.Add(outOfRange)
	f.Add(append(append([]byte(nil), proof...), 0))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, candidate []byte) {
		_, _ = scheme.VerifyProof(root, value, candidate)
	})
}

package ipa_test

import (
	"testing"

	"github.com/dewebprotocol/malt/core/sce/commitment/ipa"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestIPAIndexedBackendRestartSafe(t *testing.T) {
	first, err := ipa.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	values := []cid.Cid{
		newIndexedPayloadCID([]byte("slot0")),
		newIndexedPayloadCID([]byte("slot1")),
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
	if !value.Equals(values[1]) {
		t.Fatalf("unexpected value %s", value)
	}

	ok, err := second.VerifyIndex(root, 1, values[1], proof)
	if err != nil {
		t.Fatalf("VerifyIndex failed: %v", err)
	}
	if !ok {
		t.Fatal("expected proof to verify")
	}

	wrong := newIndexedPayloadCID([]byte("wrong"))
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
	if !value0.Equals(values[0]) {
		t.Fatalf("unexpected value at index 0: %s", value0)
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

	values := []cid.Cid{
		newIndexedPayloadCID([]byte("slot0")),
		newIndexedPayloadCID([]byte("slot1")),
	}
	root, err := first.CommitValues(values)
	if err != nil {
		t.Fatalf("CommitValues failed: %v", err)
	}

	second, err := ipa.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	newValue := newIndexedPayloadCID([]byte("slot1-new"))
	newRoot, err := second.ReplaceIndex(root, values, 1, values[1], newValue)
	if err != nil {
		t.Fatalf("ReplaceIndex failed after restart: %v", err)
	}

	updatedValues := []cid.Cid{values[0], newValue}
	third, err := ipa.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	value, proof, err := third.ProveIndex(newRoot, updatedValues, 1)
	if err != nil {
		t.Fatalf("ProveIndex on updated root failed: %v", err)
	}
	if !value.Equals(newValue) {
		t.Fatalf("unexpected updated value %s", value)
	}

	ok, err := third.VerifyIndex(newRoot, 1, newValue, proof)
	if err != nil {
		t.Fatalf("VerifyIndex on updated root failed: %v", err)
	}
	if !ok {
		t.Fatal("expected updated proof to verify")
	}
}

func newIndexedPayloadCID(data []byte) cid.Cid {
	sum, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cid.Raw, sum)
}

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
}

func newIndexedPayloadCID(data []byte) cid.Cid {
	sum, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cid.Raw, sum)
}

package radix_test

import (
	"testing"

	"github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/sce/commitment/radix"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestRadixMappingBackendNonMembership(t *testing.T) {
	s, err := radix.NewScheme(radix.WithKVStore(memory.New()))
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	value := newMappingPayloadCID([]byte("value"))
	bindings := arcset.NewSetFrom(map[string]cid.Cid{"a": value})
	root, err := s.CommitBindings(bindings)
	if err != nil {
		t.Fatalf("CommitBindings failed: %v", err)
	}

	target, present, proof, err := s.ProveBinding(root, bindings, arcset.CanonicalizePath("missing"))
	if err != nil {
		t.Fatalf("ProveBinding failed: %v", err)
	}
	if present || target.Defined() {
		t.Fatalf("expected absent binding, got present=%v target=%s", present, target)
	}

	ok, err := s.VerifyBinding(root, arcset.CanonicalizePath("missing"), cid.Undef, false, proof)
	if err != nil {
		t.Fatalf("VerifyBinding failed: %v", err)
	}
	if !ok {
		t.Fatal("expected non-membership proof to verify")
	}
}

func newMappingPayloadCID(data []byte) cid.Cid {
	sum, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cid.Raw, sum)
}

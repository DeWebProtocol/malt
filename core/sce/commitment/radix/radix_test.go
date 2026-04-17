package radix_test

import (
	"testing"

	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/sce/commitment/radix"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func newPayloadCID(data []byte) (cid.Cid, error) {
	mhash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, mhash), nil
}

func TestRadixCommitProveVerify(t *testing.T) {
	s, err := radix.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{
		"a":    k1,
		"a/b":  k2,
		"root": k1,
	})

	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	if !root.Defined() {
		t.Fatal("root should be defined")
	}
	if root.Prefix().Codec != codec.CodecMaltRadix {
		t.Fatalf("expected radix root codec, got %x", root.Prefix().Codec)
	}

	target, proof, err := s.Prove(root, arcs, "a/b")
	if err != nil {
		t.Fatalf("Prove failed: %v", err)
	}
	if !target.Equals(k2) {
		t.Fatalf("unexpected target: %s", target)
	}

	valid, err := s.Verify(root, "a/b", k2, proof)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !valid {
		t.Fatal("proof should be valid")
	}
}

func TestRadixRestartSafeProve(t *testing.T) {
	kv := memory.New()

	s1, err := radix.NewScheme(radix.WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{
		"aaa": k1,
		"bbb": k2,
	})

	root, err := s1.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	// Simulate restart: new scheme instance, same kvstore, no in-memory cache.
	s2, err := radix.NewScheme(radix.WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	target, proof, err := s2.Prove(root, arcs, "aaa")
	if err != nil {
		t.Fatalf("Prove after restart failed: %v", err)
	}
	if !target.Equals(k1) {
		t.Fatalf("unexpected target after restart: %s", target)
	}

	valid, err := s2.Verify(root, "aaa", k1, proof)
	if err != nil {
		t.Fatalf("Verify after restart failed: %v", err)
	}
	if !valid {
		t.Fatal("proof should remain valid after restart")
	}
}

func TestRadixUpdateReplace(t *testing.T) {
	kv := memory.New()

	s, err := radix.NewScheme(radix.WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	oldTarget, _ := newPayloadCID([]byte("old"))
	newTarget, _ := newPayloadCID([]byte("new"))
	otherTarget, _ := newPayloadCID([]byte("other"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{
		"aaa": oldTarget,
		"bbb": otherTarget,
	})

	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	newRoot, err := s.Update(root, arcs, "aaa", oldTarget, newTarget)
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}
	if newRoot.Equals(root) {
		t.Fatal("new root should differ after update")
	}

	updatedSnapshot := arcset.NewMapFrom(map[string]cid.Cid{
		"aaa": newTarget,
		"bbb": otherTarget,
	})

	target, proof, err := s.Prove(newRoot, updatedSnapshot, "aaa")
	if err != nil {
		t.Fatalf("Prove on new root failed: %v", err)
	}
	if !target.Equals(newTarget) {
		t.Fatalf("unexpected updated target: %s", target)
	}

	valid, err := s.Verify(newRoot, "aaa", newTarget, proof)
	if err != nil {
		t.Fatalf("Verify on new root failed: %v", err)
	}
	if !valid {
		t.Fatal("updated proof should be valid")
	}
}

func TestRadixAggregateProof(t *testing.T) {
	s, err := radix.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	k1, _ := newPayloadCID([]byte("target1"))
	k2, _ := newPayloadCID([]byte("target2"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{
		"a":   k1,
		"a/b": k2,
	})

	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	agg, err := s.AggregateProve(root, arcs, []string{"a", "a/b"})
	if err != nil {
		t.Fatalf("AggregateProve failed: %v", err)
	}

	ok, err := s.AggregateVerify(root, agg)
	if err != nil {
		t.Fatalf("AggregateVerify failed: %v", err)
	}
	if !ok {
		t.Fatal("aggregate proof should be valid")
	}
}

func TestRadixBatchUpdateRejectsOldValueMismatch(t *testing.T) {
	s, err := radix.NewScheme()
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	oldTarget, _ := newPayloadCID([]byte("old"))
	newTarget, _ := newPayloadCID([]byte("new"))
	wrongOld, _ := newPayloadCID([]byte("wrong"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{
		"aaa": oldTarget,
	})

	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	_, err = s.BatchUpdate(root, arcs, map[string]struct {
		Old cid.Cid
		New cid.Cid
	}{
		"aaa": {Old: wrongOld, New: newTarget},
	})
	if err == nil {
		t.Fatal("expected BatchUpdate to reject mismatched old value")
	}
}

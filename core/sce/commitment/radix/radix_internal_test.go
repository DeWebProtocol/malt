package radix

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func newInternalPayloadCID(data []byte) (cid.Cid, error) {
	mhash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, mhash), nil
}

func TestRadixProveFallsBackWhenHotIndexIsCorrupted(t *testing.T) {
	ctx := context.Background()
	kv := memory.New()

	s, err := NewScheme(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	k1, _ := newInternalPayloadCID([]byte("target1"))
	k2, _ := newInternalPayloadCID([]byte("target2"))
	arcs := arcset.NewMapFrom(map[string]cid.Cid{
		"aaa": k1,
		"bbb": k2,
	})

	root, err := s.Commit(arcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	bogus, _ := newInternalPayloadCID([]byte("bogus"))
	if err := putHotIndex(ctx, kv, root, nil, bogus); err != nil {
		t.Fatalf("corrupting hot index failed: %v", err)
	}

	target, proof, err := s.Prove(root, arcs, "aaa")
	if err != nil {
		t.Fatalf("Prove should fall back to canonical store, got: %v", err)
	}
	if !target.Equals(k1) {
		t.Fatalf("unexpected target: %s", target)
	}

	ok, err := s.Verify(root, "aaa", k1, proof)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !ok {
		t.Fatal("proof should remain valid after hot-index fallback")
	}
}

func TestRadixWalkProofPathRejectsTooDeepInternalNode(t *testing.T) {
	ctx := context.Background()
	kv := memory.New()

	s, err := NewScheme(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	digest := digestPath("aaa")
	var next cid.Cid
	for depth := len(digest); depth >= 1; depth-- {
		node := newInternalNode(digest[:depth])
		if next.Defined() {
			node.Children[digest[depth]] = next
		}
		nodeCID, _, err := putNode(ctx, kv, node)
		if err != nil {
			t.Fatalf("putNode failed: %v", err)
		}
		next = nodeCID
	}

	root := newInternalNode(nil)
	root.Children[digest[0]] = next
	rootNodeCID, _, err := putNode(ctx, kv, root)
	if err != nil {
		t.Fatalf("putNode root failed: %v", err)
	}
	rootCID, err := rootCIDFromNodeCID(rootNodeCID)
	if err != nil {
		t.Fatalf("rootCIDFromNodeCID failed: %v", err)
	}
	if rootCID.Prefix().Codec != codec.CodecMaltRadix {
		t.Fatalf("expected radix root codec, got %x", rootCID.Prefix().Codec)
	}

	_, _, err = s.walkProofPath(ctx, rootCID, rootNodeCID, "aaa", digest)
	if err == nil {
		t.Fatal("expected malformed deep tree to return an error")
	}
}

func TestRadixEnsureRootNodeFailedRecoveryDoesNotPersistGarbage(t *testing.T) {
	ctx := context.Background()
	kv := memory.New()

	s, err := NewScheme(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewScheme failed: %v", err)
	}

	k1, _ := newInternalPayloadCID([]byte("target1"))
	correctArcs := arcset.NewMapFrom(map[string]cid.Cid{
		"aaa": k1,
	})
	root, err := s.Commit(correctArcs)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	rootNodeCID, err := rootNodeCIDFromCommitment(root)
	if err != nil {
		t.Fatalf("rootNodeCIDFromCommitment failed: %v", err)
	}
	if err := kv.Delete(ctx, nodeStoreKey(rootNodeCID)); err != nil {
		t.Fatalf("Delete root node failed: %v", err)
	}

	k2, _ := newInternalPayloadCID([]byte("other"))
	wrongArcs := arcset.NewMapFrom(map[string]cid.Cid{
		"bbb": k2,
	})

	temp, err := NewScheme()
	if err != nil {
		t.Fatalf("temp NewScheme failed: %v", err)
	}
	wrongRoot, err := temp.Commit(wrongArcs)
	if err != nil {
		t.Fatalf("temp Commit failed: %v", err)
	}
	wrongRootNodeCID, err := rootNodeCIDFromCommitment(wrongRoot)
	if err != nil {
		t.Fatalf("wrong rootNodeCIDFromCommitment failed: %v", err)
	}

	_, _, err = s.Prove(root, wrongArcs, "aaa")
	if err == nil {
		t.Fatal("expected Prove to fail when recovery uses the wrong snapshot")
	}

	hasWrongNode, err := kv.Has(ctx, nodeStoreKey(wrongRootNodeCID))
	if err != nil {
		t.Fatalf("Has wrong root node failed: %v", err)
	}
	if hasWrongNode {
		t.Fatal("failed recovery persisted wrong rebuilt root node")
	}

	_, err = getHotIndex(ctx, kv, wrongRoot, nil)
	if err == nil {
		t.Fatal("failed recovery persisted wrong hot-index root entry")
	}
}

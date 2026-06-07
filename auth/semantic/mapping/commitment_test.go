package mapping

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/auth/commitment"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestCommitmentCommitAuthenticatesKeys(t *testing.T) {
	handler, err := NewCommitment(testScheme{})
	if err != nil {
		t.Fatalf("NewCommitment failed: %v", err)
	}

	valueA := testCID(t, "value-a")
	valueB := testCID(t, "value-b")
	first, err := handler.Commit(context.Background(), NewViewFrom(map[string]cid.Cid{
		"a": valueA,
		"b": valueB,
	}))
	if err != nil {
		t.Fatalf("Commit(first) failed: %v", err)
	}
	second, err := handler.Commit(context.Background(), NewViewFrom(map[string]cid.Cid{
		"c": valueA,
		"d": valueB,
	}))
	if err != nil {
		t.Fatalf("Commit(second) failed: %v", err)
	}
	if first.Equals(second) {
		t.Fatal("different keys with the same ordered values produced the same root")
	}
}

func TestCommitmentCommitRejectsInvalidBindings(t *testing.T) {
	handler, err := NewCommitment(testScheme{})
	if err != nil {
		t.Fatalf("NewCommitment failed: %v", err)
	}

	if _, err := handler.Commit(context.Background(), NewViewFromPaths(map[arcset.Path]cid.Cid{
		"": testCID(t, "value"),
	})); err == nil {
		t.Fatal("Commit should reject empty keys")
	}
	if _, err := handler.Commit(context.Background(), NewViewFromPaths(map[arcset.Path]cid.Cid{
		arcset.CanonicalizePath("a"): cid.Undef,
	})); err == nil {
		t.Fatal("Commit should reject undefined values")
	}
}

func TestCommitmentCommitRejectsNonCanonicalIterationOrder(t *testing.T) {
	handler, err := NewCommitment(testScheme{})
	if err != nil {
		t.Fatalf("NewCommitment failed: %v", err)
	}

	if _, err := handler.Commit(context.Background(), orderedView{
		{key: arcset.CanonicalizePath("b"), value: testCID(t, "value-b")},
		{key: arcset.CanonicalizePath("a"), value: testCID(t, "value-a")},
	}); err == nil {
		t.Fatal("Commit should reject non-canonical iteration order")
	}
}

type orderedView []orderedBinding

type orderedBinding struct {
	key   arcset.Path
	value cid.Cid
}

func (v orderedView) Len() int {
	return len(v)
}

func (v orderedView) Get(key arcset.Path) (cid.Cid, bool) {
	for _, binding := range v {
		if binding.key == key {
			return binding.value, true
		}
	}
	return cid.Undef, false
}

func (v orderedView) Iterate() Iterator {
	return &orderedIterator{bindings: v}
}

type orderedIterator struct {
	bindings orderedView
	index    int
}

func (it *orderedIterator) Next() (arcset.Path, cid.Cid, bool) {
	if it.index >= len(it.bindings) {
		return "", cid.Undef, false
	}
	binding := it.bindings[it.index]
	it.index++
	return binding.key, binding.value, true
}

func (it *orderedIterator) Err() error {
	return nil
}

type testScheme struct{}

func (testScheme) MaxValues() int { return 1024 }

func (testScheme) Commit(values []commitment.Cell) (cid.Cid, error) {
	h := sha256.New()
	var lenBuf [8]byte
	for _, value := range values {
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(value)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write(value)
	}
	sum, err := mh.Sum(h.Sum(nil), mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, sum), nil
}

func (testScheme) Prove([]commitment.Cell, uint64) (cid.Cid, commitment.Cell, []byte, error) {
	return cid.Undef, nil, nil, fmt.Errorf("not implemented")
}

func (testScheme) BatchProve([]commitment.Cell, []uint64) (cid.Cid, []commitment.Cell, []byte, error) {
	return cid.Undef, nil, nil, fmt.Errorf("not implemented")
}

func (testScheme) VerifyIndex(cid.Cid, uint64, commitment.Cell, []byte) (bool, error) {
	return false, fmt.Errorf("not implemented")
}

func (testScheme) BatchVerify(cid.Cid, []uint64, []commitment.Cell, []byte) (bool, error) {
	return false, fmt.Errorf("not implemented")
}

func (testScheme) VerifyProof(cid.Cid, commitment.Cell, []byte) (bool, error) {
	return false, fmt.Errorf("not implemented")
}

func (testScheme) Replace([]commitment.Cell, uint64, commitment.Cell, commitment.Cell) (cid.Cid, error) {
	return cid.Undef, fmt.Errorf("not implemented")
}

func testCID(t *testing.T, payload string) cid.Cid {
	t.Helper()
	sum, err := mh.Sum([]byte(payload), mh.SHA2_256, -1)
	if err != nil {
		t.Fatalf("mh.Sum failed: %v", err)
	}
	return cid.NewCidV1(cid.Raw, sum)
}

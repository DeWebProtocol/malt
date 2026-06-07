package list

import (
	"context"
	"crypto/sha512"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestCommitmentCommitReturnsListTypedRoot(t *testing.T) {
	handler, err := NewCommitment(testScheme{})
	if err != nil {
		t.Fatalf("NewCommitment failed: %v", err)
	}

	root, err := handler.Commit(context.Background(), NewViewFromSlice([]cid.Cid{
		testCID(t, "value-a"),
		testCID(t, "value-b"),
	}))
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	if got := maltcid.SemanticKindOf(root); got != maltcid.SemanticKindList {
		t.Fatalf("root semantic kind = %s, want %s", got, maltcid.SemanticKindList)
	}
}

func TestCommitmentCommitRejectsUndefinedValues(t *testing.T) {
	handler, err := NewCommitment(testScheme{})
	if err != nil {
		t.Fatalf("NewCommitment failed: %v", err)
	}

	if _, err := handler.Commit(context.Background(), NewViewFromSlice([]cid.Cid{
		testCID(t, "value"),
		cid.Undef,
	})); err == nil {
		t.Fatal("Commit should reject undefined values")
	}
}

type testScheme struct{}

func (testScheme) MaxValues() int { return 1024 }

func (testScheme) Commit(values []commitment.Cell) (cid.Cid, error) {
	h := sha512.New()
	var lenBuf [8]byte
	for _, value := range values {
		binary.BigEndian.PutUint64(lenBuf[:], uint64(len(value)))
		_, _ = h.Write(lenBuf[:])
		_, _ = h.Write(value)
	}
	sum := h.Sum(nil)
	return maltcid.NewMapKZGCid(sum[:maltcid.KZGCommitmentSize])
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

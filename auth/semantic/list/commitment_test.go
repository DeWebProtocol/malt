package list

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"

	"github.com/dewebprotocol/malt/auth/commitment"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

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

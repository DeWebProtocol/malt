package commitment_test

import (
	"testing"

	"github.com/dewebprotocol/malt/core/sce/commitment"
)

func TestUnwrapPathProofRejectsRawPrimitiveProof(t *testing.T) {
	raw := []byte{1, 2, 3, 4, 5}
	_, err := commitment.UnwrapPathProof("a", raw)
	if err == nil {
		t.Fatal("expected raw primitive proof to be rejected")
	}
}

func TestUnwrapPathProofRejectsShortProof(t *testing.T) {
	short := []byte{1, 2, 3}
	_, err := commitment.UnwrapPathProof("a", short)
	if err == nil {
		t.Fatal("expected short proof to be rejected")
	}
}

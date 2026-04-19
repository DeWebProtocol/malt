package commitment_test

import (
	"testing"

	"github.com/dewebprotocol/malt/core/sce/commitment"
)

func TestUnwrapLegacyPathProofRejectsRawPrimitiveProof(t *testing.T) {
	raw := []byte{1, 2, 3, 4, 5}
	_, err := commitment.UnwrapLegacyPathProof("a", raw)
	if err == nil {
		t.Fatal("expected raw primitive proof to be rejected")
	}
}

func TestUnwrapLegacyPathProofRejectsShortProof(t *testing.T) {
	short := []byte{1, 2, 3}
	_, err := commitment.UnwrapLegacyPathProof("a", short)
	if err == nil {
		t.Fatal("expected short proof to be rejected")
	}
}

package commitment

import (
	"bytes"
	"crypto/sha256"
	"fmt"
)

var legacyPathProofMagic = [4]byte{'M', 'P', 'T', 'H'}

const legacyPathProofVersion byte = 1
const legacyPathProofOverhead = 4 + 1 + sha256.Size

// WrapLegacyPathProof binds a primitive proof to the legacy path-oriented API.
// Primitive proofs remain index-based; this wrapper only preserves the legacy
// Scheme.Verify(path, ...) contract.
func WrapLegacyPathProof(path string, primitiveProof []byte) []byte {
	pathHash := sha256.Sum256([]byte(path))
	out := make([]byte, 0, legacyPathProofOverhead+len(primitiveProof))
	out = append(out, legacyPathProofMagic[:]...)
	out = append(out, legacyPathProofVersion)
	out = append(out, pathHash[:]...)
	out = append(out, primitiveProof...)
	return out
}

// UnwrapLegacyPathProof validates and unwraps a path-bound legacy proof.
// Proofs must use the wrapped format; raw primitive proofs are rejected.
func UnwrapLegacyPathProof(path string, proof []byte) ([]byte, error) {
	if len(proof) < legacyPathProofOverhead {
		return nil, fmt.Errorf("legacy path proof too short: %d", len(proof))
	}
	if !bytes.Equal(proof[:4], legacyPathProofMagic[:]) {
		return nil, fmt.Errorf("missing legacy path proof magic")
	}
	if proof[4] != legacyPathProofVersion {
		return nil, fmt.Errorf("unsupported legacy path proof version %d", proof[4])
	}

	expected := sha256.Sum256([]byte(path))
	if !bytes.Equal(proof[5:5+sha256.Size], expected[:]) {
		return nil, fmt.Errorf("legacy path proof does not match requested path")
	}
	return proof[legacyPathProofOverhead:], nil
}

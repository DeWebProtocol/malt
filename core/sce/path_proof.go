package sce

import (
	"bytes"
	"crypto/sha256"
	"fmt"
)

var pathProofMagic = [4]byte{'M', 'P', 'T', 'H'}

const pathProofVersion byte = 1
const pathProofOverhead = 4 + 1 + sha256.Size

func wrapPathProof(path string, primitiveProof []byte) []byte {
	pathHash := sha256.Sum256([]byte(path))
	out := make([]byte, 0, pathProofOverhead+len(primitiveProof))
	out = append(out, pathProofMagic[:]...)
	out = append(out, pathProofVersion)
	out = append(out, pathHash[:]...)
	out = append(out, primitiveProof...)
	return out
}

func unwrapPathProof(path string, proof []byte) ([]byte, error) {
	if len(proof) < pathProofOverhead {
		return nil, fmt.Errorf("path-bound proof too short: %d", len(proof))
	}
	if !bytes.Equal(proof[:4], pathProofMagic[:]) {
		return nil, fmt.Errorf("invalid path-bound proof magic")
	}
	if proof[4] != pathProofVersion {
		return nil, fmt.Errorf("unsupported path-bound proof version %d", proof[4])
	}

	expected := sha256.Sum256([]byte(path))
	if !bytes.Equal(proof[5:5+sha256.Size], expected[:]) {
		return nil, fmt.Errorf("path-bound proof does not match requested path")
	}
	return proof[pathProofOverhead:], nil
}

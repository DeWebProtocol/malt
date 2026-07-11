package verifier

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/auth/commitment"
	structure "github.com/dewebprotocol/malt/auth/semantic"
	"github.com/dewebprotocol/malt/auth/semantic/mapping"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

const (
	portableNodeWidth = 256
	radixLeafPrefix   = "malt:map:radix:leaf:v1:"
	radixBucketPrefix = "malt:map:radix:bucket:v1:"
)

// radixMapVerifier is the storage-free half of the runtime radix-map
// semantic. Its proof envelope and marker encodings intentionally mirror the
// locked runtime wire format, but verification requires only the primitive
// commitment scheme.
type radixMapVerifier struct {
	scheme commitment.IndexCommitment
}

type radixProofEnvelope struct {
	Steps  []radixProofStep    `json:"steps"`
	Bucket *radixBucketWitness `json:"bucket,omitempty"`
}

type radixProofStep struct {
	Slot  []byte `json:"slot,omitempty"`
	Proof []byte `json:"proof"`
}

type radixBucketWitness struct {
	Proof []byte `json:"proof"`
}

func newRadixMapVerifier(scheme commitment.IndexCommitment) MapVerifier {
	return &radixMapVerifier{scheme: scheme}
}

func (v *radixMapVerifier) Verify(root cid.Cid, key arcset.Path, expected mapping.Binding, proof structure.Proof) (bool, error) {
	if v == nil || v.scheme == nil {
		return false, fmt.Errorf("radix verifier commitment scheme is nil")
	}
	if !expected.Present {
		return false, fmt.Errorf("non-membership verification is not implemented")
	}
	if !root.Defined() {
		return false, fmt.Errorf("root is undefined")
	}
	if key.IsEmpty() {
		return false, fmt.Errorf("key is empty")
	}
	if !expected.Value.Defined() {
		return false, fmt.Errorf("expected value is undefined")
	}

	var envelope radixProofEnvelope
	if err := json.Unmarshal(proof, &envelope); err != nil {
		return false, err
	}
	if len(envelope.Steps) == 0 {
		return false, fmt.Errorf("missing proof steps")
	}

	digest := sha256.Sum256([]byte(key.String()))
	if len(envelope.Steps) > len(digest) {
		return false, fmt.Errorf("proof has too many radix steps")
	}
	currentRoot := root
	expectedLeaf, err := encodeRadixLeafMarker(key, expected.Value)
	if err != nil {
		return false, err
	}

	for depth, step := range envelope.Steps {
		var slotCID cid.Cid
		if len(step.Slot) > 0 {
			slotCID, err = cid.Cast(step.Slot)
			if err != nil {
				return false, err
			}
		}

		ok, err := v.scheme.VerifyIndex(currentRoot, uint64(digest[depth]), commitment.CellFromCID(slotCID), cloneProofBytes(step.Proof))
		if err != nil || !ok {
			return ok, err
		}
		if !slotCID.Defined() {
			return false, nil
		}

		if leafPath, leafValue, isLeaf, err := tryDecodeRadixLeafMarker(slotCID); err != nil {
			return false, err
		} else if isLeaf {
			if depth != len(envelope.Steps)-1 || envelope.Bucket != nil {
				return false, nil
			}
			return leafPath == key && leafValue.Equals(expected.Value), nil
		}

		if bucketRoot, isBucket, err := tryDecodeRadixBucketRef(slotCID); err != nil {
			return false, err
		} else if isBucket {
			if depth != len(envelope.Steps)-1 || envelope.Bucket == nil {
				return false, nil
			}
			return v.scheme.VerifyProof(bucketRoot, commitment.CellFromCID(expectedLeaf), cloneProofBytes(envelope.Bucket.Proof))
		}

		if depth == len(envelope.Steps)-1 {
			return false, nil
		}
		currentRoot = slotCID
	}

	return false, nil
}

func encodeRadixLeafMarker(path arcset.Path, value cid.Cid) (cid.Cid, error) {
	if path.IsEmpty() {
		return cid.Undef, fmt.Errorf("path is empty")
	}
	if !value.Defined() {
		return cid.Undef, fmt.Errorf("value is undefined")
	}
	pathBytes := []byte(path.String())
	if len(pathBytes) > 0xffff {
		return cid.Undef, fmt.Errorf("path %q is too long", path.String())
	}
	payload := make([]byte, 0, len(radixLeafPrefix)+2+len(pathBytes)+len(value.Bytes()))
	payload = append(payload, []byte(radixLeafPrefix)...)
	payload = binary.BigEndian.AppendUint16(payload, uint16(len(pathBytes)))
	payload = append(payload, pathBytes...)
	payload = append(payload, value.Bytes()...)
	return identityCID(payload)
}

func decodeRadixLeafMarker(marker cid.Cid) (arcset.Path, cid.Cid, error) {
	payload, err := decodeIdentityPayload(marker)
	if err != nil {
		return "", cid.Undef, err
	}
	if len(payload) < len(radixLeafPrefix)+2 || string(payload[:len(radixLeafPrefix)]) != radixLeafPrefix {
		return "", cid.Undef, fmt.Errorf("leaf marker prefix mismatch")
	}
	pathLen := int(binary.BigEndian.Uint16(payload[len(radixLeafPrefix) : len(radixLeafPrefix)+2]))
	offset := len(radixLeafPrefix) + 2
	if len(payload) < offset+pathLen {
		return "", cid.Undef, fmt.Errorf("leaf marker truncated")
	}
	path := arcset.CanonicalizePath(string(payload[offset : offset+pathLen]))
	if path.IsEmpty() {
		return "", cid.Undef, fmt.Errorf("leaf marker path is empty")
	}
	value, err := cid.Cast(payload[offset+pathLen:])
	if err != nil {
		return "", cid.Undef, err
	}
	return path, value, nil
}

func tryDecodeRadixLeafMarker(marker cid.Cid) (arcset.Path, cid.Cid, bool, error) {
	payload, err := decodeIdentityPayload(marker)
	if err != nil {
		return "", cid.Undef, false, nil
	}
	if len(payload) < len(radixLeafPrefix) || string(payload[:len(radixLeafPrefix)]) != radixLeafPrefix {
		return "", cid.Undef, false, nil
	}
	path, value, err := decodeRadixLeafMarker(marker)
	return path, value, err == nil, err
}

func tryDecodeRadixBucketRef(marker cid.Cid) (cid.Cid, bool, error) {
	payload, err := decodeIdentityPayload(marker)
	if err != nil {
		return cid.Undef, false, nil
	}
	if len(payload) < len(radixBucketPrefix) || string(payload[:len(radixBucketPrefix)]) != radixBucketPrefix {
		return cid.Undef, false, nil
	}
	root, err := cid.Cast(payload[len(radixBucketPrefix):])
	return root, err == nil, err
}

func identityCID(payload []byte) (cid.Cid, error) {
	sum, err := mh.Sum(payload, mh.IDENTITY, len(payload))
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, sum), nil
}

func decodeIdentityPayload(value cid.Cid) ([]byte, error) {
	if !value.Defined() {
		return nil, fmt.Errorf("marker is undefined")
	}
	decoded, err := mh.Decode(value.Hash())
	if err != nil {
		return nil, err
	}
	if decoded.Code != mh.IDENTITY {
		return nil, fmt.Errorf("marker is not identity-encoded")
	}
	return decoded.Digest, nil
}

func cloneProofBytes(proof []byte) []byte {
	return append([]byte(nil), proof...)
}

var _ MapVerifier = (*radixMapVerifier)(nil)

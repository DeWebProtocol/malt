package unixfs

import (
	"bytes"
	"encoding/json"
	"fmt"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

const ChunkMetadataProfile = "malt.unixfs.chunks/1"

// ChunkMetadata is an application document, never a Core structural field.
// Count is supplied by the authenticated Positional root and is not duplicated.
type ChunkMetadata struct {
	Profile   string `json:"profile"`
	ChunkSize uint64 `json:"chunk_size,string"`
	TotalSize uint64 `json:"total_size,string"`
}

func (m ChunkMetadata) Validate(count uint64) error {
	if m.Profile != ChunkMetadataProfile || m.ChunkSize == 0 {
		return fmt.Errorf("invalid chunk metadata profile or width")
	}
	expected := uint64(0)
	if m.TotalSize > 0 {
		expected = (m.TotalSize-1)/m.ChunkSize + 1
	}
	if expected != count {
		return fmt.Errorf("chunk metadata does not match authenticated count")
	}
	return nil
}

func EncodeChunkMetadata(total, chunk, count uint64) ([]byte, error) {
	m := ChunkMetadata{Profile: ChunkMetadataProfile, ChunkSize: chunk, TotalSize: total}
	if err := m.Validate(count); err != nil {
		return nil, err
	}
	return json.Marshal(m)
}

func VerifyPayloadCID(key cid.Cid, data []byte) error {
	if !key.Defined() {
		return fmt.Errorf("missing metadata payload CID")
	}
	actual, err := key.Prefix().Sum(data)
	if err != nil || !actual.Equals(key) {
		return fmt.Errorf("metadata bytes do not match payload CID")
	}
	return nil
}

func ParseChunkMetadata(key cid.Cid, data []byte, count uint64) (ChunkMetadata, error) {
	var m ChunkMetadata
	if err := VerifyPayloadCID(key, data); err != nil {
		return m, err
	}
	if key.Type() != cid.Raw {
		return m, fmt.Errorf("chunk metadata requires raw JSON content")
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, err
	}
	if err := m.Validate(count); err != nil {
		return m, err
	}
	canonical, err := json.Marshal(m)
	if err != nil || !bytes.Equal(canonical, data) {
		return m, fmt.Errorf("noncanonical chunk metadata JSON")
	}
	return m, nil
}

// InlineChunkMetadata binds the small document directly in an identity CID.
// Evaluators can retain this application data without an extra content store.
func InlineChunkMetadata(total, chunk, count uint64) (cid.Cid, error) {
	data, err := EncodeChunkMetadata(total, chunk, count)
	if err != nil {
		return cid.Undef, err
	}
	hash, err := mh.Encode(data, mh.IDENTITY)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, hash), nil
}

func ParseInlineChunkMetadata(key cid.Cid, count uint64) (ChunkMetadata, error) {
	decoded, err := mh.Decode(key.Hash())
	if err != nil || decoded.Code != mh.IDENTITY {
		return ChunkMetadata{}, fmt.Errorf("inline chunk metadata required")
	}
	return ParseChunkMetadata(key, decoded.Digest, count)
}

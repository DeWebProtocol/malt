package nodeapi

import (
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// CIDForBlock computes the CIDv1 SHA-256 identity of immutable block bytes.
// Codec zero selects raw bytes. This helper does not write or accept a block;
// callers still validate the CID returned by the selected node service.
func CIDForBlock(block Block) (cid.Cid, error) {
	codec := block.Codec
	if codec == 0 {
		codec = cid.Raw
	}
	hash, err := mh.Sum(block.Data, mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(codec, hash), nil
}

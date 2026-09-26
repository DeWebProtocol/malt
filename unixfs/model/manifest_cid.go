package unixfs

import (
	"fmt"

	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// NewManifestCID creates a V2 CID for canonical typed manifest bytes.
func newManifestCID(payload []byte) (cid.Cid, error) {
	digest, err := mh.Sum(payload, mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, fmt.Errorf("create manifest multihash: %w", err)
	}
	return cid.NewCidV1(DirectoryManifestCodec, digest), nil
}

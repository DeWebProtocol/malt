// Package format defines UnixFS application-model format constants.
package format

import (
	"fmt"

	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// UnixFS application manifest CID codecs are kept outside MALT Core's reserved
// 0x30VSBB typed-root namespace.
const (
	CodecMaltManifestV2 = 0x310002 // malt-unixfs-directory-manifest-json-v2
)

// NewManifestCID creates a V2 CID for canonical typed manifest bytes.
func NewManifestCID(payload []byte) (cid.Cid, error) {
	digest, err := mh.Sum(payload, mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, fmt.Errorf("create manifest multihash: %w", err)
	}
	return cid.NewCidV1(CodecMaltManifestV2, digest), nil
}

// IsManifestCID reports whether c is a UnixFS model manifest CID.
func IsManifestCID(c cid.Cid) bool {
	if !c.Defined() {
		return false
	}
	codec := c.Prefix().Codec
	return codec == CodecMaltManifestV2
}

// CodecName returns the locked wire name for UnixFS codecs.
func CodecName(codec uint64) string {
	switch codec {
	case CodecMaltManifestV2:
		return "malt-unixfs-directory-manifest-json-v2"
	default:
		return fmt.Sprintf("unknown-%x", codec)
	}
}

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
	CodecMaltManifestV1 = 0x310001 // malt-unixfs-directory-manifest-json-v1
	CodecMaltManifestV2 = 0x310002 // malt-unixfs-directory-manifest-json-v2
)

// NewManifestCID creates a V2 CID for canonical typed manifest bytes.
func NewManifestCID(payload []byte) (cid.Cid, error) {
	return NewManifestCIDWithCodec(payload, CodecMaltManifestV2)
}

// NewManifestCIDWithCodec creates a CID for a supported manifest encoding.
func NewManifestCIDWithCodec(payload []byte, codec uint64) (cid.Cid, error) {
	if codec != CodecMaltManifestV1 && codec != CodecMaltManifestV2 {
		return cid.Undef, fmt.Errorf("unsupported manifest codec 0x%x", codec)
	}
	mhash, err := mh.Sum(payload, mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, fmt.Errorf("failed to create manifest multihash: %w", err)
	}
	return cid.NewCidV1(codec, mhash), nil
}

// IsManifestCID reports whether c is a UnixFS model manifest CID.
func IsManifestCID(c cid.Cid) bool {
	if !c.Defined() {
		return false
	}
	codec := c.Prefix().Codec
	return codec == CodecMaltManifestV1 || codec == CodecMaltManifestV2
}

// CodecName returns the locked wire name for UnixFS codecs.
func CodecName(codec uint64) string {
	switch codec {
	case CodecMaltManifestV1:
		return "malt-unixfs-directory-manifest-json-v1"
	case CodecMaltManifestV2:
		return "malt-unixfs-directory-manifest-json-v2"
	default:
		return fmt.Sprintf("unknown-%x", codec)
	}
}

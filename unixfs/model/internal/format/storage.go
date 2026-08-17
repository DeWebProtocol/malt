package format

import (
	maltcid "github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

// StorageKindFromCID projects a MALT or raw payload CID into the UnixFS model's
// storage-kind vocabulary.
func StorageKindFromCID(c cid.Cid) string {
	if !c.Defined() {
		return ""
	}
	switch c.Prefix().Codec {
	case cid.Raw, CodecMaltManifestV1, CodecMaltManifestV2:
		return "raw"
	}
	switch maltcid.SemanticKindOf(c) {
	case maltcid.SemanticKindList:
		return "list"
	case maltcid.SemanticKindMap:
		return "map"
	default:
		return ""
	}
}

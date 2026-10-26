package unixfs

import (
	maltcid "github.com/dewebprotocol/malt-core/maltcid"
	cid "github.com/ipfs/go-cid"
)

// StorageKindFromCID reports the typed layout or raw application payload.
func StorageKindFromCID(c cid.Cid) string {
	if !c.Defined() {
		return ""
	}
	switch c.Prefix().Codec {
	case cid.Raw, DirectoryManifestCodec:
		return "raw"
	}
	descriptor, _, err := maltcid.ParseRoot(c)
	if err != nil {
		return ""
	}
	switch descriptor.Layout {
	case maltcid.Map:
		return "prefix"
	case maltcid.List:
		return "positional"
	default:
		return ""
	}
}

package format

import cid "github.com/ipfs/go-cid"

// StorageKindFromCID projects a MALT or raw payload CID into the HTTP/storage
// kind vocabulary used by UnixFS-facing routes and command output.
func StorageKindFromCID(c cid.Cid) string {
	if !c.Defined() {
		return ""
	}
	codec := c.Prefix().Codec
	switch codec {
	case 0x55:
		return "raw"
	case 0x300002, 0x300004:
		return "list"
	case 0x300001, 0x300003:
		return "map"
	default:
		return ""
	}
}

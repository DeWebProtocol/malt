package unixfs

import (
	"fmt"

	unixfsformat "github.com/dewebprotocol/malt-client/unixfs/model/internal/format"
	"github.com/dewebprotocol/malt-client/unixfs/model/internal/manifest"
	cid "github.com/ipfs/go-cid"
)

const (
	DirectoryManifestVersionV1 = manifest.VersionV1
	DirectoryManifestVersionV2 = manifest.VersionV2

	DirectoryManifestCodecV1 = unixfsformat.CodecMaltManifestV1
	DirectoryManifestCodecV2 = unixfsformat.CodecMaltManifestV2

	// DirectoryManifestCodec is the current UnixFS directory manifest codec.
	DirectoryManifestCodec = DirectoryManifestCodecV2
)

// DirectoryEntryType is the parent-declared UnixFS projection for one child.
type DirectoryEntryType string

const (
	DirectoryEntryTypeUnknown DirectoryEntryType = ""
	DirectoryEntryTypeDir     DirectoryEntryType = "dir"
	DirectoryEntryTypeFile    DirectoryEntryType = "file"
)

// DirectoryEntry is one immediate child named by a directory manifest.
type DirectoryEntry struct {
	Name string             `json:"name"`
	Type DirectoryEntryType `json:"type"`
}

// DirectoryManifest is a decoded V1 or V2 manifest. V1 entries have an
// unknown Type and are interpreted only through the historical compatibility
// rule in the UnixFS reader.
type DirectoryManifest struct {
	Version int              `json:"version"`
	Entries []DirectoryEntry `json:"entries"`
}

// DirectoryManifestBlock carries canonical payload bytes together with the
// application codec that must be used when computing or storing its CID.
type DirectoryManifestBlock struct {
	Codec uint64
	Data  []byte
}

// NewDirectoryManifestCID creates a CID for current V2 manifest bytes.
func NewDirectoryManifestCID(payload []byte) (cid.Cid, error) {
	return unixfsformat.NewManifestCID(payload)
}

// IsDirectoryManifestCID reports whether a CID uses a supported UnixFS
// directory manifest codec.
func IsDirectoryManifestCID(value cid.Cid) bool {
	return unixfsformat.IsManifestCID(value)
}

// EncodeDirectoryManifest serializes typed entries using canonical V2 JSON.
func EncodeDirectoryManifest(entries []DirectoryEntry) (DirectoryManifestBlock, error) {
	internalEntries := make([]manifest.DirectoryEntry, len(entries))
	for index, entry := range entries {
		internalEntries[index] = manifest.DirectoryEntry{
			Name: entry.Name,
			Type: manifest.EntryType(entry.Type),
		}
	}
	data, err := manifest.MarshalDirectoryEntries(internalEntries)
	if err != nil {
		return DirectoryManifestBlock{}, err
	}
	return DirectoryManifestBlock{Codec: DirectoryManifestCodecV2, Data: data}, nil
}

// DirectoryManifestPayload serializes typed entries using canonical V2 JSON.
// Callers that store the result must use DirectoryManifestCodec.
func DirectoryManifestPayload(entries []DirectoryEntry) ([]byte, error) {
	block, err := EncodeDirectoryManifest(entries)
	if err != nil {
		return nil, err
	}
	return block.Data, nil
}

// ParseDirectoryManifest parses already-fetched bytes according to their CID
// codec. Historical raw-CID manifests are accepted as V1 because early native
// runtime writers stored the locked V1 JSON through a codec-less CAS port.
func ParseDirectoryManifest(key cid.Cid, data []byte) (*DirectoryManifest, error) {
	var (
		value *manifest.DirectoryManifest
		err   error
	)
	switch key.Prefix().Codec {
	case cid.Raw, DirectoryManifestCodecV1:
		value, err = manifest.ParseV1DirectoryJSON(data)
	case DirectoryManifestCodecV2:
		value, err = manifest.ParseV2DirectoryJSON(data)
	default:
		return nil, fmt.Errorf("unsupported UnixFS directory manifest codec 0x%x", key.Prefix().Codec)
	}
	if err != nil {
		return nil, err
	}
	entries := make([]DirectoryEntry, len(value.Entries))
	for index, entry := range value.Entries {
		entries[index] = DirectoryEntry{
			Name: entry.Name,
			Type: DirectoryEntryType(entry.Type),
		}
	}
	return &DirectoryManifest{Version: value.Version, Entries: entries}, nil
}

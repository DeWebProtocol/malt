package unixfs

import (
	"context"
	"fmt"
	"slices"

	unixfsformat "github.com/dewebprotocol/malt/layout/unixfs/internal/format"
	"github.com/dewebprotocol/malt/layout/unixfs/internal/manifest"
	cid "github.com/ipfs/go-cid"
)

type blockGetter interface {
	Get(ctx context.Context, c cid.Cid) ([]byte, error)
}

// DirectoryManifestCodec is the UnixFS manifest CID codec.
const DirectoryManifestCodec = unixfsformat.CodecMaltManifest

// NewDirectoryManifestCID creates a CID for a directory manifest payload.
func NewDirectoryManifestCID(payload []byte) (cid.Cid, error) {
	return unixfsformat.NewManifestCID(payload)
}

// ManifestDirectoryEntries reads a UnixFS manifest-codec directory payload.
// The boolean result reports whether the CID is a UnixFS manifest CID; callers
// can use it to keep non-manifest fallback behavior outside the layout package.
func ManifestDirectoryEntries(ctx context.Context, blocks blockGetter, manifestCID cid.Cid) ([]string, bool, error) {
	if !unixfsformat.IsManifestCID(manifestCID) {
		return nil, false, nil
	}
	entries, err := DirectoryManifestPayloadEntries(ctx, blocks, manifestCID)
	if err != nil {
		return nil, true, err
	}
	return entries, true, nil
}

// DirectoryManifestPayloadEntries reads a directory manifest payload CID. It
// intentionally does not require the typed manifest codec because historical
// UnixFS layout payloads may be stored as raw CAS blocks.
func DirectoryManifestPayloadEntries(ctx context.Context, blocks blockGetter, payload cid.Cid) ([]string, error) {
	return readDirectoryManifestEntries(ctx, blocks, payload)
}

// DirectoryManifestPayload serializes directory entries using the UnixFS
// manifest payload format.
func DirectoryManifestPayload(entries []string) ([]byte, error) {
	return manifest.MarshalDirectoryEntries(entries)
}

func readDirectoryManifestEntries(ctx context.Context, blocks blockGetter, manifestCID cid.Cid) ([]string, error) {
	if blocks == nil {
		return nil, fmt.Errorf("CAS reader is nil")
	}
	data, err := blocks.Get(ctx, manifestCID)
	if err != nil {
		return nil, err
	}
	m, err := manifest.ParseDirectoryJSON(data)
	if err != nil {
		return nil, err
	}
	return slices.Clone(m.Entries), nil
}

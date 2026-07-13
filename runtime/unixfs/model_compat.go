package unixfs

import (
	"context"

	unixfsmodel "github.com/dewebprotocol/malt/model/unixfs"
	cid "github.com/ipfs/go-cid"
)

const (
	DefaultChunkSize       = unixfsmodel.DefaultChunkSize
	DirectoryManifestCodec = unixfsmodel.DirectoryManifestCodec
)

// Deprecated: use model/unixfs.NewDirectoryManifestCID.
func NewDirectoryManifestCID(payload []byte) (cid.Cid, error) {
	return unixfsmodel.NewDirectoryManifestCID(payload)
}

func ManifestDirectoryEntries(ctx context.Context, blocks interface {
	Get(context.Context, cid.Cid) ([]byte, error)
}, manifestCID cid.Cid) ([]string, bool, error) {
	if !unixfsmodel.IsDirectoryManifestCID(manifestCID) {
		return nil, false, nil
	}
	entries, err := DirectoryManifestPayloadEntries(ctx, blocks, manifestCID)
	return entries, true, err
}

func DirectoryManifestPayloadEntries(ctx context.Context, blocks interface {
	Get(context.Context, cid.Cid) ([]byte, error)
}, payload cid.Cid) ([]string, error) {
	data, err := blocks.Get(ctx, payload)
	if err != nil {
		return nil, err
	}
	return unixfsmodel.ParseDirectoryManifest(data)
}

func DirectoryManifestPayload(entries []string) ([]byte, error) {
	return unixfsmodel.DirectoryManifestPayload(entries)
}

func StorageKindFromCID(c cid.Cid) string { return unixfsmodel.StorageKindFromCID(c) }

func PayloadChunks(data []byte, chunkSize int) ([][]byte, error) {
	return unixfsmodel.PayloadChunks(data, chunkSize)
}

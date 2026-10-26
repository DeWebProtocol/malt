package unixfs

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt-client/nodeapi"
	model "github.com/dewebprotocol/malt-client/unixfs/model"
	"github.com/dewebprotocol/malt-core/auth/coordinate"
	"github.com/dewebprotocol/malt-core/derivation"
	"github.com/dewebprotocol/malt-core/engine"
	"github.com/dewebprotocol/malt-core/maltcid"
	cid "github.com/ipfs/go-cid"
)

// FileBlockStore distinguishes body chunks from the metadata document without
// requiring observers to recognize or reimplement application serialization.
// Both kinds use raw CIDs. The boolean is true only for the metadata document.
type FileBlockStore interface {
	PutFileBlock(context.Context, []byte, bool) (cid.Cid, error)
}

// WriteChunkedFile stores fixed-size raw chunks and authenticated file metadata.
// The caller supplies CAS and node capabilities; UnixFS owns file serialization.
func WriteChunkedFile(ctx context.Context, blocks FileBlockStore, roots nodeapi.AuthenticationBuilder, data []byte, mode uint32, chunkSize int, profile maltcid.ProfileID) (cid.Cid, error) {
	if err := ctx.Err(); err != nil {
		return cid.Undef, err
	}
	if chunkSize <= 0 {
		return cid.Undef, fmt.Errorf("chunk size must be positive")
	}
	entries := []engine.Entry{}
	for start := 0; start < len(data); {
		end := start + min(chunkSize, len(data)-start)
		key, err := blocks.PutFileBlock(ctx, data[start:end], false)
		if err != nil {
			return cid.Undef, err
		}
		if expected, err := nodeapi.CIDForBlock(nodeapi.Block{Data: data[start:end], Codec: cid.Raw}); err != nil || !expected.Equals(key) {
			return cid.Undef, nodeapi.ErrCorruptedBlock
		}
		entries = append(entries, engine.Entry{Label: coordinate.EncodeIndex(uint64(len(entries))), Target: key})
		start = end
	}
	metadata, err := model.EncodeFileMetadata(uint64(len(data)), uint64(chunkSize), uint64(len(entries)), mode)
	if err != nil {
		return cid.Undef, err
	}
	payload, err := blocks.PutFileBlock(ctx, metadata, true)
	if err != nil {
		return cid.Undef, err
	}
	if expected, err := nodeapi.CIDForBlock(nodeapi.Block{Data: metadata, Codec: cid.Raw}); err != nil || !expected.Equals(payload) {
		return cid.Undef, nodeapi.ErrCorruptedBlock
	}
	return roots.BuildAuthentication(ctx, engine.State{Descriptor: maltcid.RootDescriptor{Layout: maltcid.List, DerivationProfile: uint8(derivation.Direct), Profile: profile}, PayloadCID: payload, Entries: entries})
}

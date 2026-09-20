package encrypted

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt-client/unixfs"
	cid "github.com/ipfs/go-cid"
)

type geometryReader struct {
	unixfs.Reader
	part *unixfs.ReadResult
}

func (r geometryReader) ReadPositionalPayloadRange(context.Context, cid.Cid, uint64, uint64) (*unixfs.ReadResult, error) {
	return r.part, nil
}

func TestReadChunkBindsAuthenticatedWidthToEncryptedManifest(t *testing.T) {
	plaintext := []byte("data")
	bucketKey := [32]byte{1, 2, 3}
	file := FileView{DatasetID: "dataset", Branch: "main", BindingID: "binding", RelativePath: "file", Epoch: 1}
	file.Manifest = FileManifest{Profile: ProfileID, Version: ProfileVersion, Kind: EntryFile, Storage: StorageList, Size: 8, PlaintextChunkSize: 4, CiphertextChunkSize: 4 + envelopeOverhead, CiphertextSize: 2 * (4 + envelopeOverhead), ChunkCount: 2}
	key := fileContentKey(bindingKey(bucketKey, file.DatasetID, file.Branch, file.BindingID), file.RelativePath)
	sealed, err := sealEnvelope(kindFileChunk, file.Epoch, key, plaintext, chunkContext(file.DatasetID, file.Branch, file.BindingID, file.RelativePath, 0)...)
	if err != nil {
		t.Fatal(err)
	}
	part := &unixfs.ReadResult{Body: sealed, Offset: 0, TotalSize: file.Manifest.CiphertextSize, ChunkSize: file.Manifest.CiphertextChunkSize}
	reader := &Reader{lists: geometryReader{part: part}}
	keys := func(uint32) ([32]byte, error) { return bucketKey, nil }
	got, err := reader.readChunk(t.Context(), file, 0, keys)
	if err != nil || !bytes.Equal(got, plaintext) {
		t.Fatalf("valid chunk: %q %v", got, err)
	}
	// All bytes, the AEAD, offset and total size remain valid. The lower-level
	// authenticated sequence width alone no longer matches the file manifest.
	for _, width := range []uint64{0, file.Manifest.CiphertextChunkSize / 2, file.Manifest.CiphertextChunkSize + 1} {
		part.ChunkSize = width
		if _, err := reader.readChunk(t.Context(), file, 0, keys); err == nil || !strings.Contains(err.Error(), "geometry") {
			t.Fatalf("width %d: %v", width, err)
		}
	}
}

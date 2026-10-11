package unixfs

import (
	"bytes"
	"strings"
	"testing"

	unixfsmodel "github.com/dewebprotocol/malt-client/unixfs/model"
	"github.com/dewebprotocol/malt-core/engine"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestReadChunkMetadataRevalidatesRetainedBytes(t *testing.T) {
	body, err := unixfsmodel.EncodeChunkMetadata(128, 16, 8)
	if err != nil {
		t.Fatal(err)
	}
	key, err := (cid.Prefix{Version: 1, Codec: cid.Raw, MhType: mh.SHA2_256, MhLength: -1}).Sum(body)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name    string
		body    []byte
		count   uint64
		wantErr string
	}{
		{name: "valid", body: body, count: 8},
		{name: "changed-bytes", body: bytes.Replace(body, []byte(`"128"`), []byte(`"127"`), 1), count: 8, wantErr: "payload CID"},
		{name: "changed-count", body: body, count: 9, wantErr: "authenticated count"},
		{name: "empty-bytes", body: []byte{}, count: 8, wantErr: "payload CID"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Reuse must work without any CAS getter, including when retained
			// evidence is invalid: reject it instead of fetching a replacement.
			reader := &verifiedReader{}
			geometry, retained, err := reader.readChunkMetadata(t.Context(), engine.Metadata{PayloadCID: key, Count: tc.count}, tc.body)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("retained metadata error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if geometry.TotalSize != 128 || geometry.ChunkSize != 16 || !bytes.Equal(retained, body) {
				t.Fatalf("retained metadata changed: %#v, %q", geometry, retained)
			}
		})
	}
}

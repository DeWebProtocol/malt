package nodeapi_test

import (
	"encoding/hex"
	"testing"

	"github.com/dewebprotocol/malt-client/nodeapi"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestCIDForBlockPreservesCodecAndSHA256Identity(t *testing.T) {
	for _, codec := range []uint64{0, cid.Raw, cid.DagProtobuf, cid.DagCBOR} {
		id, err := nodeapi.CIDForBlock(nodeapi.Block{Codec: codec, Data: []byte("abc")})
		if err != nil {
			t.Fatal(err)
		}
		wantCodec := codec
		if wantCodec == 0 {
			wantCodec = cid.Raw
		}
		hash, err := mh.Decode(id.Hash())
		if err != nil {
			t.Fatal(err)
		}
		if id.Version() != 1 || id.Type() != wantCodec || hash.Code != mh.SHA2_256 ||
			hex.EncodeToString(hash.Digest) != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
			t.Fatalf("codec %d: unexpected block identity %s", codec, id)
		}
	}
}

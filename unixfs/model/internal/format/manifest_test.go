package format_test

import (
	"testing"

	unixfsformat "github.com/dewebprotocol/malt-client/unixfs/model/internal/format"
	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

func TestNewManifestCIDUsesV2Codec(t *testing.T) {
	value, err := unixfsformat.NewManifestCID([]byte(`{"entries":[]}`))
	if err != nil {
		t.Fatalf("NewManifestCID: %v", err)
	}
	if value.Prefix().Codec != unixfsformat.CodecMaltManifestV2 {
		t.Fatalf("codec %x, want %x", value.Prefix().Codec, unixfsformat.CodecMaltManifestV2)
	}
	if !unixfsformat.IsManifestCID(value) {
		t.Fatal("V2 manifest CID should be recognized")
	}
}

func TestManifestCodecVersionsAreDistinctAndOutsideCore(t *testing.T) {
	payload := []byte(`{"entries":[]}`)
	v1, err := unixfsformat.NewManifestCIDWithCodec(payload, unixfsformat.CodecMaltManifestV1)
	if err != nil {
		t.Fatal(err)
	}
	v2, err := unixfsformat.NewManifestCIDWithCodec(payload, unixfsformat.CodecMaltManifestV2)
	if err != nil {
		t.Fatal(err)
	}
	if v1.Equals(v2) {
		t.Fatal("manifest versions must have distinct CIDs")
	}
	for _, value := range []cid.Cid{v1, v2} {
		if codec := value.Prefix().Codec; codec >= 0x300000 && codec <= 0x30ffff {
			t.Fatalf("manifest codec %x occupies the MALT typed-root namespace", codec)
		}
		if maltcid.IsMaltCid(value) {
			t.Fatal("manifest should not be recognized as a MALT root")
		}
	}
	if got := unixfsformat.CodecName(unixfsformat.CodecMaltManifestV1); got != "malt-unixfs-directory-manifest-json-v1" {
		t.Fatalf("V1 name = %q", got)
	}
	if got := unixfsformat.CodecName(unixfsformat.CodecMaltManifestV2); got != "malt-unixfs-directory-manifest-json-v2" {
		t.Fatalf("V2 name = %q", got)
	}
}

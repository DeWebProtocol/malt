package format_test

import (
	"testing"

	unixfsformat "github.com/dewebprotocol/malt-client/unixfs/model/internal/format"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
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

func TestCurrentManifestCodecIsOutsideCore(t *testing.T) {
	value, err := unixfsformat.NewManifestCID([]byte(`{"entries":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if codec := value.Prefix().Codec; codec >= 0x300000 && codec <= 0x30ffff {
		t.Fatalf("manifest codec %x occupies Core namespace", codec)
	}
	if _, _, err := maltcid.ParseRoot(value); err == nil {
		t.Fatal("manifest parsed as MALT Root")
	}
	if got := unixfsformat.CodecName(unixfsformat.CodecMaltManifestV2); got != "malt-unixfs-directory-manifest-json-v2" {
		t.Fatal(got)
	}
}

package unixfs

import (
	"testing"

	"github.com/dewebprotocol/malt-core/maltcid"
)

func TestNewDirectoryManifestCIDUsesV2Codec(t *testing.T) {
	value, err := NewDirectoryManifestCID([]byte(`{"entries":[]}`))
	if err != nil {
		t.Fatalf("NewDirectoryManifestCID: %v", err)
	}
	if value.Prefix().Codec != DirectoryManifestCodec {
		t.Fatalf("codec %x, want %x", value.Prefix().Codec, DirectoryManifestCodec)
	}
	if !IsDirectoryManifestCID(value) {
		t.Fatal("V2 manifest CID should be recognized")
	}
}

func TestCurrentManifestCodecIsOutsideCore(t *testing.T) {
	value, err := NewDirectoryManifestCID([]byte(`{"entries":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if codec := value.Prefix().Codec; codec >= 0x300000 && codec <= 0x30ffff {
		t.Fatalf("manifest codec %x occupies Core namespace", codec)
	}
	if _, _, err := maltcid.ParseRoot(value); err == nil {
		t.Fatal("manifest parsed as MALT Root")
	}
}

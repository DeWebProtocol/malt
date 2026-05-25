package wire_test

import (
	"testing"

	unixfswire "github.com/dewebprotocol/malt/layout/unixfs/wire"
)

func TestNewManifestCID(t *testing.T) {
	payload := []byte(`{"entries":["docs","readme.md"]}`)
	c, err := unixfswire.NewManifestCID(payload)
	if err != nil {
		t.Fatalf("NewManifestCID: %v", err)
	}
	if c.Prefix().Codec != unixfswire.CodecMaltManifest {
		t.Fatalf("codec %x, want %x", c.Prefix().Codec, unixfswire.CodecMaltManifest)
	}
	if !unixfswire.IsManifestCID(c) {
		t.Fatal("manifest CID should be recognized")
	}
}

func TestCodecName(t *testing.T) {
	if got := unixfswire.CodecName(unixfswire.CodecMaltManifest); got != "malt-manifest" {
		t.Fatalf("CodecName = %q, want malt-manifest", got)
	}
}

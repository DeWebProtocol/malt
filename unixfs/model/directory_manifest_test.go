package unixfs_test

import (
	"testing"

	unixfs "github.com/dewebprotocol/malt-client/unixfs/model"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestDirectoryManifestV2GoldenVector(t *testing.T) {
	block, err := unixfs.EncodeDirectoryManifest([]unixfs.DirectoryEntry{
		{Name: "report.docx", Type: unixfs.DirectoryEntryTypeFile},
		{Name: "docs", Type: unixfs.DirectoryEntryTypeDir},
	})
	if err != nil {
		t.Fatal(err)
	}
	const wantPayload = `{"entries":[{"name":"docs","type":"dir"},{"name":"report.docx","type":"file"}]}`
	if string(block.Data) != wantPayload {
		t.Fatalf("payload = %q", block.Data)
	}
	if block.Codec != unixfs.DirectoryManifestCodecV2 {
		t.Fatalf("codec = 0x%x", block.Codec)
	}
	value, err := unixfs.NewDirectoryManifestCID(block.Data)
	if err != nil {
		t.Fatal(err)
	}
	const wantCID = "bagbibrabciqkfloqxwbi2arag4vedouzjjh4tiwninbyrjp7n5reg5wup7f4fla"
	if value.String() != wantCID {
		t.Fatalf("CID = %s, want %s", value, wantCID)
	}
	parsed, err := unixfs.ParseDirectoryManifest(value, block.Data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Version != unixfs.DirectoryManifestVersionV2 || len(parsed.Entries) != 2 {
		t.Fatalf("manifest = %#v", parsed)
	}
}

func TestDirectoryManifestV1AndRawCompatibility(t *testing.T) {
	payload := []byte(`{"entries":["docs"]}`)
	for _, codec := range []uint64{unixfs.DirectoryManifestCodecV1, 0x55} {
		value, err := cidForManifestTest(payload, codec)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := unixfs.ParseDirectoryManifest(value, payload)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Version != unixfs.DirectoryManifestVersionV1 ||
			len(parsed.Entries) != 1 ||
			parsed.Entries[0].Type != unixfs.DirectoryEntryTypeUnknown {
			t.Fatalf("manifest = %#v", parsed)
		}
	}
}

func cidForManifestTest(payload []byte, codec uint64) (cid.Cid, error) {
	digest, err := mh.Sum(payload, mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(codec, digest), nil
}

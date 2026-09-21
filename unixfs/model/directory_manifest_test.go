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

func TestDirectoryManifestRejectsRetiredV1AndRawCodecs(t *testing.T) {
	for _, payload := range [][]byte{[]byte(`{"entries":["docs"]}`), []byte(`{"entries":[]}`), []byte(`{"entries":[{"name":"docs","type":"dir"}]}`)} {
		for _, codec := range []uint64{0x310001, cid.Raw} {
			key, err := cidForManifestTest(payload, codec)
			if err != nil {
				t.Fatal(err)
			}
			if unixfs.IsDirectoryManifestCID(key) {
				t.Fatal("recognized retired manifest codec")
			}
			if _, err := unixfs.ParseDirectoryManifest(key, payload); err == nil {
				t.Fatalf("accepted retired manifest codec %x", codec)
			}
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

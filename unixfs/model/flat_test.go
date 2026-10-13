package unixfs

import (
	"testing"

	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestFlatManifestAndPathLabelsPreserveRepositoryNames(t *testing.T) {
	seen := map[string]bool{}
	for _, path := range []string{"", "@payload", "@scope/package", "src/@scope/file", " file", "file\n"} {
		label, err := FlatPathLabel(path)
		if err != nil {
			t.Fatal(err)
		}
		if seen[string(label)] {
			t.Fatalf("colliding label: %q", path)
		}
		seen[string(label)] = true
	}
	for _, path := range []string{"/absolute", "a//b", "a/../b", "a\\b"} {
		if _, err := FlatPathLabel(path); err == nil {
			t.Fatalf("invalid path accepted %q", path)
		}
	}
	block, err := EncodeDirectoryManifest([]DirectoryEntry{{Name: "@scope", Type: DirectoryEntryTypeDir}, {Name: "file\n", Type: DirectoryEntryTypeFile}})
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"entries\":[{\"name\":\"@scope\",\"type\":\"dir\"},{\"name\":\"file\\n\",\"type\":\"file\"}]}"
	if string(block.Data) != want {
		t.Fatalf("manifest changed %s", block.Data)
	}
	key, err := NewDirectoryManifestCID(block.Data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseDirectoryManifest(key, block.Data); err != nil {
		t.Fatal(err)
	}
}

func TestFileMetadataBindsModeAndEmptyGeometry(t *testing.T) {
	for _, total := range []uint64{0, 262145} {
		count := (total + 262143) / 262144
		data, err := EncodeFileMetadata(total, 262144, count, 0o755)
		if err != nil {
			t.Fatal(err)
		}
		key, err := (cid.Prefix{Version: 1, Codec: cid.Raw, MhType: mh.SHA2_256, MhLength: -1}).Sum(data)
		if err != nil {
			t.Fatal(err)
		}
		m, err := ParseChunkMetadata(key, data, count)
		if err != nil || m.Mode == nil || *m.Mode != 0o755 {
			t.Fatal("mode lost", err)
		}
		other, err := EncodeFileMetadata(total, 262144, count, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		if string(other) == string(data) {
			t.Fatal("mode-only update is not authenticated")
		}
	}
}

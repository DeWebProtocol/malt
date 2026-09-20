package manifest_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/dewebprotocol/malt-client/unixfs/model/internal/manifest"
)

func TestV2CanonicalRoundTrip(t *testing.T) {
	entries := []manifest.DirectoryEntry{
		{Name: "readme.md", Type: manifest.EntryTypeFile},
		{Name: "docs", Type: manifest.EntryTypeDir},
		{Name: "a<&\u2028.txt", Type: manifest.EntryTypeFile},
	}
	data, err := manifest.MarshalDirectoryEntries(entries)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"entries\":[{\"name\":\"a<&\u2028.txt\",\"type\":\"file\"},{\"name\":\"docs\",\"type\":\"dir\"},{\"name\":\"readme.md\",\"type\":\"file\"}]}"
	if string(data) != want {
		t.Fatalf("payload = %q, want %q", data, want)
	}
	parsed, err := manifest.ParseDirectoryJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Version != manifest.VersionV2 || len(parsed.Entries) != 3 {
		t.Fatalf("manifest = %#v", parsed)
	}
}

func TestV2EmptyIsCanonical(t *testing.T) {
	data, err := manifest.MarshalDirectoryEntries(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"entries":[]}` {
		t.Fatalf("payload = %q", data)
	}
	value, err := manifest.ParseDirectoryJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if value.Entries == nil || len(value.Entries) != 0 {
		t.Fatalf("entries = %#v", value.Entries)
	}
}

func TestV2RejectsNonCanonicalAndInvalidEntries(t *testing.T) {
	tests := []string{
		`{"entries":["docs"]}`,
		`{"entries":[{"name":"docs"}]}`,
		`{"entries":[{"name":"docs","type":""}]}`,
		`{"entries": [ ]}`,
		`{"entries":[{"type":"dir","name":"docs"}]}`,
		`{"entries":[{"name":"docs","type":"directory"}]}`,
		`{"entries":[{"name":"b","type":"dir"},{"name":"a","type":"file"}]}`,
		`{"entries":[{"name":"a","type":"dir"},{"name":"a","type":"file"}]}`,
		`{"entries":[{"name":"a/b","type":"file"}]}`,
		`{"entries":[{"name":"docs","type":"dir","extra":true}]}`,
		`{"entries":[],"extra":true}`,
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			_, err := manifest.ParseDirectoryJSON([]byte(raw))
			if !errors.Is(err, manifest.ErrInvalidManifest) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestV2WriterRejectsDuplicateNames(t *testing.T) {
	_, err := manifest.MarshalDirectoryEntries([]manifest.DirectoryEntry{
		{Name: "same", Type: manifest.EntryTypeDir},
		{Name: "same", Type: manifest.EntryTypeFile},
	})
	if !errors.Is(err, manifest.ErrInvalidManifest) {
		t.Fatalf("error = %v", err)
	}
}

func TestManifestReaderAndWriterRejectUnsupportedChildNames(t *testing.T) {
	for _, name := range []string{".", "..", "@payload", "\x00", " file", "file ", "\tfile", "file\n", "\u0085file", "file\ufeff"} {
		t.Run(name, func(t *testing.T) {
			encoded, err := json.Marshal(name)
			if err != nil {
				t.Fatal(err)
			}
			raw := []byte(`{"entries":[{"name":` + string(encoded) + `,"type":"file"}]}`)
			if _, err := manifest.ParseDirectoryJSON(raw); !errors.Is(err, manifest.ErrInvalidManifest) {
				t.Fatalf("reader accepted %q: %v", name, err)
			}
			if _, err := manifest.MarshalDirectoryEntries([]manifest.DirectoryEntry{{Name: name, Type: manifest.EntryTypeFile}}); !errors.Is(err, manifest.ErrInvalidManifest) {
				t.Fatalf("writer accepted %q: %v", name, err)
			}
		})
	}
}

func TestManifestReadersRejectInvalidUTF8(t *testing.T) {
	invalid := []byte{'{', '"', 'e', 'n', 't', 'r', 'i', 'e', 's', '"', ':', '[', '"', 0xff, '"', ']', '}'}
	for _, parse := range []func([]byte) (*manifest.DirectoryManifest, error){
		manifest.ParseDirectoryJSON,
	} {
		if _, err := parse(invalid); !errors.Is(err, manifest.ErrInvalidManifest) {
			t.Fatalf("error = %v, want invalid manifest", err)
		}
	}
}

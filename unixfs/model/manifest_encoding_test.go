package unixfs

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestV2CanonicalRoundTrip(t *testing.T) {
	entries := []DirectoryEntry{
		{Name: "readme.md", Type: DirectoryEntryTypeFile},
		{Name: "docs", Type: DirectoryEntryTypeDir},
		{Name: "a<&\u2028.txt", Type: DirectoryEntryTypeFile},
	}
	data, err := marshalDirectoryEntries(entries)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"entries\":[{\"name\":\"a<&\u2028.txt\",\"type\":\"file\"},{\"name\":\"docs\",\"type\":\"dir\"},{\"name\":\"readme.md\",\"type\":\"file\"}]}"
	if string(data) != want {
		t.Fatalf("payload = %q, want %q", data, want)
	}
	parsed, err := parseDirectoryJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Version != DirectoryManifestVersionV2 || len(parsed.Entries) != 3 {
		t.Fatalf("manifest = %#v", parsed)
	}
}

func TestV2EmptyIsCanonical(t *testing.T) {
	data, err := marshalDirectoryEntries(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"entries":[]}` {
		t.Fatalf("payload = %q", data)
	}
	value, err := parseDirectoryJSON(data)
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
			_, err := parseDirectoryJSON([]byte(raw))
			if !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestV2WriterRejectsDuplicateNames(t *testing.T) {
	_, err := marshalDirectoryEntries([]DirectoryEntry{
		{Name: "same", Type: DirectoryEntryTypeDir},
		{Name: "same", Type: DirectoryEntryTypeFile},
	})
	if !errors.Is(err, ErrInvalidManifest) {
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
			if _, err := parseDirectoryJSON(raw); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("reader accepted %q: %v", name, err)
			}
			if _, err := marshalDirectoryEntries([]DirectoryEntry{{Name: name, Type: DirectoryEntryTypeFile}}); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("writer accepted %q: %v", name, err)
			}
		})
	}
}

func TestManifestReadersRejectInvalidUTF8(t *testing.T) {
	invalid := []byte{'{', '"', 'e', 'n', 't', 'r', 'i', 'e', 's', '"', ':', '[', '"', 0xff, '"', ']', '}'}
	for _, parse := range []func([]byte) (*DirectoryManifest, error){
		parseDirectoryJSON,
	} {
		if _, err := parse(invalid); !errors.Is(err, ErrInvalidManifest) {
			t.Fatalf("error = %v, want invalid manifest", err)
		}
	}
}

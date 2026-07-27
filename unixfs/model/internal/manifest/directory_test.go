package manifest_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/dewebprotocol/malt-client/unixfs/model/internal/manifest"
)

func TestParseV1DirectoryJSON(t *testing.T) {
	value, err := manifest.ParseV1DirectoryJSON([]byte(`{"entries":["a","b","docs"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if value.Version != manifest.VersionV1 || len(value.Entries) != 3 {
		t.Fatalf("manifest = %#v", value)
	}
	if value.Entries[0].Name != "a" || value.Entries[0].Type != manifest.EntryTypeUnknown {
		t.Fatalf("entry = %#v", value.Entries[0])
	}
}

func TestV2CanonicalRoundTrip(t *testing.T) {
	entries := []manifest.DirectoryEntry{
		{Name: "readme.md", Type: manifest.EntryTypeFile},
		{Name: "docs", Type: manifest.EntryTypeDir},
		{Name: "a<&\u2028.txt", Type: manifest.EntryTypeFile},
	}
	data, err := manifest.MarshalV2DirectoryEntries(entries)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"entries\":[{\"name\":\"a<&\u2028.txt\",\"type\":\"file\"},{\"name\":\"docs\",\"type\":\"dir\"},{\"name\":\"readme.md\",\"type\":\"file\"}]}"
	if string(data) != want {
		t.Fatalf("payload = %q, want %q", data, want)
	}
	parsed, err := manifest.ParseV2DirectoryJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Version != manifest.VersionV2 || len(parsed.Entries) != 3 {
		t.Fatalf("manifest = %#v", parsed)
	}
}

func TestV2EmptyIsCanonical(t *testing.T) {
	data, err := manifest.MarshalV2DirectoryEntries(nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"entries":[]}` {
		t.Fatalf("payload = %q", data)
	}
	value, err := manifest.ParseV2DirectoryJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if value.Entries == nil || len(value.Entries) != 0 {
		t.Fatalf("entries = %#v", value.Entries)
	}
}

func TestV2RejectsNonCanonicalAndInvalidEntries(t *testing.T) {
	tests := []string{
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
			_, err := manifest.ParseV2DirectoryJSON([]byte(raw))
			if !errors.Is(err, manifest.ErrInvalidManifest) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestV2WriterRejectsDuplicateNames(t *testing.T) {
	_, err := manifest.MarshalV2DirectoryEntries([]manifest.DirectoryEntry{
		{Name: "same", Type: manifest.EntryTypeDir},
		{Name: "same", Type: manifest.EntryTypeFile},
	})
	if !errors.Is(err, manifest.ErrInvalidManifest) {
		t.Fatalf("error = %v", err)
	}
}

func TestManifestReadersAndV2WriterRejectUnsupportedChildNames(t *testing.T) {
	for _, name := range []string{
		".", "..", "@payload", "\x00", " file", "file ", "\tfile", "file\n",
		"\u0085file", "file\ufeff",
	} {
		t.Run(name, func(t *testing.T) {
			encodedName, err := json.Marshal(name)
			if err != nil {
				t.Fatal(err)
			}
			for version, raw := range map[string][]byte{
				"V1": []byte(`{"entries":[` + string(encodedName) + `]}`),
				"V2": []byte(`{"entries":[{"name":` + string(encodedName) + `,"type":"file"}]}`),
			} {
				t.Run(version, func(t *testing.T) {
					parse := manifest.ParseV1DirectoryJSON
					if version == "V2" {
						parse = manifest.ParseV2DirectoryJSON
					}
					if _, err := parse(raw); !errors.Is(err, manifest.ErrInvalidManifest) {
						t.Fatalf("reader accepted child name %q: %v", name, err)
					}
				})
			}
			if _, err := manifest.MarshalV2DirectoryEntries([]manifest.DirectoryEntry{{
				Name: name, Type: manifest.EntryTypeFile,
			}}); !errors.Is(err, manifest.ErrInvalidManifest) {
				t.Fatalf("writer accepted child name %q: %v", name, err)
			}
		})
	}
}

func TestManifestReadersRejectInvalidUTF8(t *testing.T) {
	invalid := []byte{'{', '"', 'e', 'n', 't', 'r', 'i', 'e', 's', '"', ':', '[', '"', 0xff, '"', ']', '}'}
	for _, parse := range []func([]byte) (*manifest.DirectoryManifest, error){
		manifest.ParseV1DirectoryJSON,
		manifest.ParseV2DirectoryJSON,
	} {
		if _, err := parse(invalid); !errors.Is(err, manifest.ErrInvalidManifest) {
			t.Fatalf("error = %v, want invalid manifest", err)
		}
	}
}

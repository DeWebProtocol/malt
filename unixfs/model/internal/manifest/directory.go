// Package manifest implements the locked directory manifest encodings used by
// the UnixFS application model.
//
// V1 wire shape:
//
//	{"entries":["docs","readme.md"]}
//
// V2 wire shape:
//
//	{"entries":[{"name":"docs","type":"dir"},{"name":"readme.md","type":"file"}]}
//
// V2 bytes are canonical: entries are ordered by UTF-8 bytes, object fields
// have the order shown above, insignificant whitespace is forbidden, and JSON
// strings use the locked encoder below. The CID codec selects V1 or V2, which
// also makes an empty manifest unambiguous.
package manifest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	VersionV1 = 1
	VersionV2 = 2
)

// EntryType is the UnixFS projection assigned to one immediate child by its
// parent directory manifest. It is independent of the target's MALT semantic
// kind.
type EntryType string

const (
	EntryTypeUnknown EntryType = ""
	EntryTypeDir     EntryType = "dir"
	EntryTypeFile    EntryType = "file"
)

// DirectoryEntry is one immediate child projection.
type DirectoryEntry struct {
	Name string    `json:"name"`
	Type EntryType `json:"type"`
}

// DirectoryManifest is the decoded application manifest. Version is selected
// by the payload CID codec and is not duplicated in the JSON body.
type DirectoryManifest struct {
	Version int
	Entries []DirectoryEntry
}

var (
	// ErrInvalidManifest indicates JSON that is not a valid directory manifest.
	ErrInvalidManifest = errors.New("invalid directory manifest")
)

// ParseV1DirectoryJSON decodes the historical name-only manifest. Entries have
// an unknown type so the caller can apply the locked V1 compatibility rule.
func ParseV1DirectoryJSON(data []byte) (*DirectoryManifest, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: manifest is not UTF-8", ErrInvalidManifest)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidManifest, err)
	}
	for key := range raw {
		if key != "entries" {
			return nil, fmt.Errorf("%w: unknown field %q", ErrInvalidManifest, key)
		}
	}
	encodedEntries, ok := raw["entries"]
	if !ok {
		return nil, fmt.Errorf("%w: missing required field \"entries\"", ErrInvalidManifest)
	}
	var names []string
	if err := json.Unmarshal(encodedEntries, &names); err != nil {
		return nil, fmt.Errorf("%w: invalid \"entries\" array: %w", ErrInvalidManifest, err)
	}
	entries := make([]DirectoryEntry, len(names))
	for index, name := range names {
		entries[index] = DirectoryEntry{Name: name, Type: EntryTypeUnknown}
	}
	manifest := &DirectoryManifest{Version: VersionV1, Entries: entries}
	if err := Validate(manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

// ParseV2DirectoryJSON decodes canonical typed manifest bytes. Non-canonical
// encodings fail closed so every implementation computes the same CID for the
// same normalized manifest.
func ParseV2DirectoryJSON(data []byte) (*DirectoryManifest, error) {
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("%w: manifest is not UTF-8", ErrInvalidManifest)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidManifest, err)
	}
	for key := range raw {
		if key != "entries" {
			return nil, fmt.Errorf("%w: unknown field %q", ErrInvalidManifest, key)
		}
	}
	encodedEntries, ok := raw["entries"]
	if !ok {
		return nil, fmt.Errorf("%w: missing required field \"entries\"", ErrInvalidManifest)
	}
	var entryObjects []map[string]json.RawMessage
	if err := json.Unmarshal(encodedEntries, &entryObjects); err != nil {
		return nil, fmt.Errorf("%w: invalid \"entries\" array: %w", ErrInvalidManifest, err)
	}
	entries := make([]DirectoryEntry, len(entryObjects))
	for index, object := range entryObjects {
		for key := range object {
			if key != "name" && key != "type" {
				return nil, fmt.Errorf("%w: entries[%d]: unknown field %q", ErrInvalidManifest, index, key)
			}
		}
		nameRaw, hasName := object["name"]
		typeRaw, hasType := object["type"]
		if !hasName || !hasType {
			return nil, fmt.Errorf("%w: entries[%d]: name and type are required", ErrInvalidManifest, index)
		}
		if err := json.Unmarshal(nameRaw, &entries[index].Name); err != nil {
			return nil, fmt.Errorf("%w: entries[%d].name: %w", ErrInvalidManifest, index, err)
		}
		if err := json.Unmarshal(typeRaw, &entries[index].Type); err != nil {
			return nil, fmt.Errorf("%w: entries[%d].type: %w", ErrInvalidManifest, index, err)
		}
	}
	manifest := &DirectoryManifest{Version: VersionV2, Entries: entries}
	if err := Validate(manifest); err != nil {
		return nil, err
	}
	canonical, err := MarshalV2DirectoryEntries(entries)
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(data, canonical) {
		return nil, fmt.Errorf("%w: V2 manifest bytes are not canonical", ErrInvalidManifest)
	}
	return manifest, nil
}

// Validate checks version, type, and sorted unique immediate-child invariants.
func Validate(manifest *DirectoryManifest) error {
	if manifest == nil {
		return fmt.Errorf("%w: nil manifest", ErrInvalidManifest)
	}
	if manifest.Version != VersionV1 && manifest.Version != VersionV2 {
		return fmt.Errorf("%w: unsupported version %d", ErrInvalidManifest, manifest.Version)
	}
	previous := ""
	for index, entry := range manifest.Entries {
		if err := validateImmediateChildName(entry.Name); err != nil {
			return fmt.Errorf("%w: entries[%d]: %w", ErrInvalidManifest, index, err)
		}
		switch manifest.Version {
		case VersionV1:
			if entry.Type != EntryTypeUnknown {
				return fmt.Errorf("%w: entries[%d]: V1 entry type must be absent", ErrInvalidManifest, index)
			}
		case VersionV2:
			if entry.Type != EntryTypeDir && entry.Type != EntryTypeFile {
				return fmt.Errorf("%w: entries[%d]: unsupported type %q", ErrInvalidManifest, index, entry.Type)
			}
		}
		if index > 0 && entry.Name <= previous {
			if entry.Name == previous {
				return fmt.Errorf("%w: duplicate entry %q", ErrInvalidManifest, entry.Name)
			}
			return fmt.Errorf("%w: entries must be sorted by UTF-8 bytes", ErrInvalidManifest)
		}
		previous = entry.Name
	}
	return nil
}

func validateImmediateChildName(name string) error {
	if name == "" {
		return fmt.Errorf("empty child name")
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("child name is not valid UTF-8")
	}
	if strings.Contains(name, "/") {
		return fmt.Errorf("immediate child name must not contain '/': %q", name)
	}
	if strings.Contains(name, "\\") {
		return fmt.Errorf("immediate child name must not contain '\\': %q", name)
	}
	return nil
}

// NormalizeV2 returns a sorted copy of entries. Duplicate names are rejected
// rather than resolved by implementation-specific first/last-wins behavior.
func NormalizeV2(entries []DirectoryEntry) ([]DirectoryEntry, error) {
	normalized := slices.Clone(entries)
	slices.SortFunc(normalized, func(left, right DirectoryEntry) int {
		return strings.Compare(left.Name, right.Name)
	})
	manifest := &DirectoryManifest{Version: VersionV2, Entries: normalized}
	if err := Validate(manifest); err != nil {
		return nil, err
	}
	return normalized, nil
}

// MarshalV2DirectoryEntries emits the locked canonical JSON bytes for V2.
func MarshalV2DirectoryEntries(entries []DirectoryEntry) ([]byte, error) {
	normalized, err := NormalizeV2(entries)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	out.WriteString(`{"entries":[`)
	for index, entry := range normalized {
		if index > 0 {
			out.WriteByte(',')
		}
		out.WriteString(`{"name":`)
		appendJSONString(&out, entry.Name)
		out.WriteString(`,"type":`)
		appendJSONString(&out, string(entry.Type))
		out.WriteByte('}')
	}
	out.WriteString(`]}`)
	return out.Bytes(), nil
}

// appendJSONString implements the RFC 8259 string subset used by the locked
// V2 encoder. Printable Unicode is emitted as UTF-8; control characters use
// the shortest JSON escape, with lower-case hex for the remaining controls.
func appendJSONString(out *bytes.Buffer, value string) {
	const hex = "0123456789abcdef"
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\f':
			out.WriteString(`\f`)
		case '\n':
			out.WriteString(`\n`)
		case '\r':
			out.WriteString(`\r`)
		case '\t':
			out.WriteString(`\t`)
		default:
			if r < 0x20 {
				out.WriteString(`\u00`)
				out.WriteByte(hex[byte(r)>>4])
				out.WriteByte(hex[byte(r)&0x0f])
				continue
			}
			out.WriteRune(r)
		}
	}
	out.WriteByte('"')
}

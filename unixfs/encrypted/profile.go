// Package encrypted implements the runtime-owned encrypted UnixFS application
// profile. MALT authenticates opaque lookup tokens and ciphertext CIDs while
// authorized consumers decrypt directory and file manifests locally.
package encrypted

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/chacha20poly1305"
)

const (
	ProfileID      = "malt.encrypted-unixfs/v1"
	ProfileVersion = 1
	// NamespaceKeyEpoch pins the key epoch used to derive opaque lookup
	// tokens. Content-encryption epochs may rotate without renaming every Map
	// key in the dataset.
	NamespaceKeyEpoch = uint32(1)

	defaultPlaintextChunkSize = 256 * 1024

	envelopeVersion  = byte(1)
	envelopeHeader   = 8 + 1 + 1 + 4 + chacha20poly1305.NonceSizeX
	envelopeOverhead = envelopeHeader + chacha20poly1305.Overhead
)

var envelopeMagic = [8]byte{'M', 'A', 'L', 'T', 'E', 'F', 'S', '1'}

type envelopeKind byte

const (
	kindDatasetManifest   envelopeKind = 1
	kindDirectoryManifest envelopeKind = 2
	kindFileManifest      envelopeKind = 3
	kindFileChunk         envelopeKind = 4
)

const (
	EntryDirectory = "directory"
	EntryFile      = "file"
	EntrySymlink   = "symlink"

	StorageRaw  = "raw"
	StorageList = "list"
)

// DatasetManifest is the encrypted root directory description. Targets are
// deliberately absent: each opaque token -> target relation is authenticated
// independently by the dataset MALT Map.
type DatasetManifest struct {
	Profile     string            `json:"profile"`
	Version     int               `json:"version"`
	DatasetID   string            `json:"dataset_id"`
	PlanID      string            `json:"plan_id"`
	DatasetName string            `json:"dataset_name"`
	Branch      string            `json:"branch"`
	Bindings    []BindingManifest `json:"bindings"`
}

// BindingManifest describes one user-visible top-level directory without
// exposing its name or stable identity to storage. Token is the only Map key
// used by an untrusted executor.
type BindingManifest struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	PathName string `json:"path_name"`
	Token    string `json:"token"`
}

// DirectoryManifest is the encrypted readdir source for one directory. Child
// targets remain in the surrounding MALT Map so content-only updates do not
// rewrite this payload and independent token changes remain mergeable.
type DirectoryManifest struct {
	Profile          string           `json:"profile"`
	Version          int              `json:"version"`
	Kind             string           `json:"kind"`
	Mode             uint32           `json:"mode"`
	ModifiedUnixNano int64            `json:"modified_unix_nano"`
	Entries          []DirectoryEntry `json:"entries"`
}

type DirectoryEntry struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Token string `json:"token"`
}

// FileManifest is encrypted independently from file content. A regular file
// root authenticates both this manifest and its raw/List ciphertext target.
// A symlink has no content binding; its safe relative target is inside this
// encrypted manifest.
type FileManifest struct {
	Profile             string `json:"profile"`
	Version             int    `json:"version"`
	Kind                string `json:"kind"`
	Mode                uint32 `json:"mode"`
	ModifiedUnixNano    int64  `json:"modified_unix_nano"`
	Size                uint64 `json:"size"`
	Storage             string `json:"storage,omitempty"`
	PlaintextChunkSize  uint64 `json:"plaintext_chunk_size,omitempty"`
	CiphertextChunkSize uint64 `json:"ciphertext_chunk_size,omitempty"`
	CiphertextSize      uint64 `json:"ciphertext_size,omitempty"`
	ChunkCount          uint64 `json:"chunk_count,omitempty"`
	LinkTarget          string `json:"link_target,omitempty"`
}

type envelope struct {
	Kind       envelopeKind
	Epoch      uint32
	Nonce      []byte
	Ciphertext []byte
}

func deriveKey(master [32]byte, domain string, parts ...string) [32]byte {
	mac := hmac.New(sha256.New, master[:])
	writeKDFField(mac, ProfileID)
	writeKDFField(mac, domain)
	for _, part := range parts {
		writeKDFField(mac, part)
	}
	var result [32]byte
	copy(result[:], mac.Sum(nil))
	return result
}

func writeKDFField(writer io.Writer, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = writer.Write(size[:])
	_, _ = writer.Write([]byte(value))
}

func datasetManifestKey(bucketKey [32]byte, datasetID, branch string) [32]byte {
	return deriveKey(bucketKey, "dataset-manifest", datasetID, branch)
}

func bindingKey(bucketKey [32]byte, datasetID, branch, bindingID string) [32]byte {
	return deriveKey(bucketKey, "binding", datasetID, branch, bindingID)
}

func directoryManifestKey(key [32]byte, relativePath string) [32]byte {
	return deriveKey(key, "directory-manifest", relativePath)
}

func fileManifestKey(key [32]byte, relativePath string) [32]byte {
	return deriveKey(key, "file-manifest", relativePath)
}

func fileContentKey(key [32]byte, relativePath string) [32]byte {
	return deriveKey(key, "file-content", relativePath)
}

func bindingToken(bucketKey [32]byte, datasetID, branch, bindingID string) string {
	key := deriveKey(bucketKey, "binding-token", datasetID, branch)
	return opaqueToken(key, bindingID)
}

func entryToken(key [32]byte, parentPath, name string) string {
	tokenKey := deriveKey(key, "entry-token", parentPath)
	return opaqueToken(tokenKey, name)
}

func opaqueToken(key [32]byte, value string) string {
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte(value))
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(mac.Sum(nil))
	return "e1-" + strings.ToLower(encoded)
}

func envelopeAAD(kind envelopeKind, epoch uint32, contextParts ...string) []byte {
	var out bytes.Buffer
	out.WriteString(ProfileID)
	out.WriteByte(0)
	out.WriteByte(byte(kind))
	var number [4]byte
	binary.BigEndian.PutUint32(number[:], epoch)
	out.Write(number[:])
	for _, part := range contextParts {
		binary.BigEndian.PutUint32(number[:], uint32(len(part)))
		out.Write(number[:])
		out.WriteString(part)
	}
	return out.Bytes()
}

func sealEnvelope(kind envelopeKind, epoch uint32, key [32]byte, plaintext []byte, contextParts ...string) ([]byte, error) {
	if epoch == 0 {
		return nil, fmt.Errorf("encrypted UnixFS key epoch must be positive")
	}
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generate encrypted UnixFS nonce: %w", err)
	}
	header := make([]byte, envelopeHeader)
	copy(header, envelopeMagic[:])
	header[8] = envelopeVersion
	header[9] = byte(kind)
	binary.BigEndian.PutUint32(header[10:14], epoch)
	copy(header[14:], nonce)
	ciphertext := aead.Seal(nil, nonce, plaintext, envelopeAAD(kind, epoch, contextParts...))
	return append(header, ciphertext...), nil
}

func parseEnvelope(data []byte) (envelope, error) {
	if len(data) < envelopeOverhead || !bytes.Equal(data[:8], envelopeMagic[:]) || data[8] != envelopeVersion {
		return envelope{}, fmt.Errorf("unsupported encrypted UnixFS envelope")
	}
	kind := envelopeKind(data[9])
	switch kind {
	case kindDatasetManifest, kindDirectoryManifest, kindFileManifest, kindFileChunk:
	default:
		return envelope{}, fmt.Errorf("unsupported encrypted UnixFS envelope kind %d", kind)
	}
	epoch := binary.BigEndian.Uint32(data[10:14])
	if epoch == 0 {
		return envelope{}, fmt.Errorf("encrypted UnixFS envelope has invalid key epoch")
	}
	return envelope{
		Kind: kind, Epoch: epoch,
		Nonce:      append([]byte(nil), data[14:envelopeHeader]...),
		Ciphertext: append([]byte(nil), data[envelopeHeader:]...),
	}, nil
}

func openEnvelope(data []byte, expected envelopeKind, key [32]byte, contextParts ...string) ([]byte, uint32, error) {
	parsed, err := parseEnvelope(data)
	if err != nil {
		return nil, 0, err
	}
	if parsed.Kind != expected {
		return nil, 0, fmt.Errorf("encrypted UnixFS envelope kind %d, want %d", parsed.Kind, expected)
	}
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, 0, err
	}
	plaintext, err := aead.Open(nil, parsed.Nonce, parsed.Ciphertext, envelopeAAD(expected, parsed.Epoch, contextParts...))
	if err != nil {
		return nil, 0, fmt.Errorf("decrypt encrypted UnixFS envelope: key, context, or ciphertext is invalid")
	}
	return plaintext, parsed.Epoch, nil
}

func encodeCanonical(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func decodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("encrypted UnixFS manifest contains trailing JSON")
		}
		return err
	}
	return nil
}

func validateDatasetManifest(manifest DatasetManifest) error {
	if manifest.Profile != ProfileID || manifest.Version != ProfileVersion ||
		strings.TrimSpace(manifest.DatasetID) == "" || strings.TrimSpace(manifest.PlanID) == "" ||
		strings.TrimSpace(manifest.DatasetName) == "" ||
		strings.TrimSpace(manifest.Branch) == "" ||
		!utf8.ValidString(manifest.DatasetID) || !utf8.ValidString(manifest.PlanID) ||
		!utf8.ValidString(manifest.DatasetName) || !utf8.ValidString(manifest.Branch) {
		return fmt.Errorf("encrypted UnixFS dataset manifest is incomplete")
	}
	previous := ""
	ids := make(map[string]struct{}, len(manifest.Bindings))
	tokens := make(map[string]struct{}, len(manifest.Bindings))
	paths := make(map[string]struct{}, len(manifest.Bindings))
	for _, binding := range manifest.Bindings {
		if strings.TrimSpace(binding.ID) == "" || strings.TrimSpace(binding.Name) == "" ||
			binding.PathName == "" || !utf8.ValidString(binding.ID) ||
			!utf8.ValidString(binding.Name) || !utf8.ValidString(binding.PathName) || !validToken(binding.Token) {
			return fmt.Errorf("encrypted UnixFS binding manifest is incomplete")
		}
		if err := validateEntryName(binding.PathName); err != nil {
			return fmt.Errorf("encrypted UnixFS binding path name: %w", err)
		}
		if binding.ID <= previous {
			return fmt.Errorf("encrypted UnixFS bindings must be uniquely sorted by ID")
		}
		previous = binding.ID
		if _, ok := ids[binding.ID]; ok {
			return fmt.Errorf("duplicate encrypted UnixFS binding ID %q", binding.ID)
		}
		if _, ok := tokens[binding.Token]; ok {
			return fmt.Errorf("duplicate encrypted UnixFS binding token")
		}
		if _, ok := paths[binding.PathName]; ok {
			return fmt.Errorf("duplicate encrypted UnixFS binding path name %q", binding.PathName)
		}
		ids[binding.ID] = struct{}{}
		tokens[binding.Token] = struct{}{}
		paths[binding.PathName] = struct{}{}
	}
	return nil
}

func validateDirectoryManifest(manifest DirectoryManifest) error {
	if manifest.Profile != ProfileID || manifest.Version != ProfileVersion || manifest.Kind != EntryDirectory {
		return fmt.Errorf("encrypted UnixFS directory manifest is incomplete")
	}
	previous := ""
	tokens := make(map[string]struct{}, len(manifest.Entries))
	for _, entry := range manifest.Entries {
		if err := validateEntryName(entry.Name); err != nil {
			return err
		}
		if entry.Type != EntryDirectory && entry.Type != EntryFile && entry.Type != EntrySymlink {
			return fmt.Errorf("unsupported encrypted UnixFS entry type %q", entry.Type)
		}
		if !validToken(entry.Token) {
			return fmt.Errorf("invalid encrypted UnixFS entry token")
		}
		if entry.Name <= previous {
			return fmt.Errorf("encrypted UnixFS directory entries must be uniquely sorted by name")
		}
		previous = entry.Name
		if _, ok := tokens[entry.Token]; ok {
			return fmt.Errorf("duplicate encrypted UnixFS entry token")
		}
		tokens[entry.Token] = struct{}{}
	}
	return nil
}

func validateFileManifest(manifest FileManifest) error {
	if manifest.Profile != ProfileID || manifest.Version != ProfileVersion {
		return fmt.Errorf("encrypted UnixFS file manifest is incomplete")
	}
	switch manifest.Kind {
	case EntrySymlink:
		if manifest.LinkTarget == "" || !utf8.ValidString(manifest.LinkTarget) || manifest.Storage != "" || manifest.ChunkCount != 0 || manifest.CiphertextSize != 0 {
			return fmt.Errorf("encrypted UnixFS symlink manifest is invalid")
		}
	case EntryFile:
		if manifest.LinkTarget != "" || (manifest.Storage != StorageRaw && manifest.Storage != StorageList) ||
			manifest.PlaintextChunkSize == 0 || manifest.CiphertextChunkSize == 0 || manifest.CiphertextSize == 0 || manifest.ChunkCount == 0 {
			return fmt.Errorf("encrypted UnixFS file manifest is invalid")
		}
		wantChunks := uint64(1)
		if manifest.Size > 0 {
			wantChunks = (manifest.Size + manifest.PlaintextChunkSize - 1) / manifest.PlaintextChunkSize
		}
		if manifest.ChunkCount != wantChunks || manifest.CiphertextChunkSize != manifest.PlaintextChunkSize+envelopeOverhead {
			return fmt.Errorf("encrypted UnixFS file chunk geometry is invalid")
		}
		lastPlain := manifest.Size % manifest.PlaintextChunkSize
		if manifest.Size == 0 {
			lastPlain = 0
		} else if lastPlain == 0 {
			lastPlain = manifest.PlaintextChunkSize
		}
		wantCipher := (manifest.ChunkCount-1)*manifest.CiphertextChunkSize + lastPlain + envelopeOverhead
		if manifest.CiphertextSize != wantCipher || (manifest.Storage == StorageRaw) != (manifest.ChunkCount == 1) {
			return fmt.Errorf("encrypted UnixFS file ciphertext geometry is invalid")
		}
	default:
		return fmt.Errorf("unsupported encrypted UnixFS file kind %q", manifest.Kind)
	}
	return nil
}

func validToken(token string) bool {
	if !strings.HasPrefix(token, "e1-") || len(token) != 55 {
		return false
	}
	for _, r := range token[3:] {
		if (r < 'a' || r > 'z') && (r < '2' || r > '7') {
			return false
		}
	}
	return true
}

func validateEntryName(name string) error {
	if !utf8.ValidString(name) || name == "" || name == "." || name == ".." || strings.HasPrefix(name, "@") ||
		strings.ContainsAny(name, "/\\\x00") {
		return fmt.Errorf("invalid encrypted UnixFS entry name %q", name)
	}
	return nil
}

func canonicalDatasetBindings(bindings []BindingManifest) []BindingManifest {
	result := slices.Clone(bindings)
	slices.SortFunc(result, func(a, b BindingManifest) int { return strings.Compare(a.ID, b.ID) })
	return result
}

func canonicalDirectoryEntries(entries []DirectoryEntry) []DirectoryEntry {
	result := slices.Clone(entries)
	slices.SortFunc(result, func(a, b DirectoryEntry) int { return strings.Compare(a.Name, b.Name) })
	return result
}

func chunkContext(datasetID, branch, bindingID, relativePath string, index uint64) []string {
	return []string{datasetID, branch, bindingID, relativePath, strconv.FormatUint(index, 10)}
}

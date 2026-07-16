package maltcid_test

import (
	"testing"

	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestCodecLayout(t *testing.T) {
	tests := []struct {
		name     string
		codec    uint64
		version  uint8
		semantic maltcid.SemanticKind
		backend  maltcid.BackendKind
	}{
		{name: "map_kzg", codec: maltcid.CodecMaltMapKZG, version: 1, semantic: maltcid.SemanticKindMap, backend: maltcid.BackendKindKZG},
		{name: "list_kzg", codec: maltcid.CodecMaltListKZG, version: 1, semantic: maltcid.SemanticKindList, backend: maltcid.BackendKindKZG},
		{name: "map_ipa", codec: maltcid.CodecMaltMapIPA, version: 1, semantic: maltcid.SemanticKindMap, backend: maltcid.BackendKindIPA},
		{name: "list_ipa", codec: maltcid.CodecMaltListIPA, version: 1, semantic: maltcid.SemanticKindList, backend: maltcid.BackendKindIPA},
	}
	wantCodecs := map[string]uint64{
		"map_kzg":  0x301101,
		"list_kzg": 0x301201,
		"map_ipa":  0x301102,
		"list_ipa": 0x301202,
	}
	if maltcid.MALTVersionID != 1 {
		t.Fatalf("MALTVersionID = %d, want 1", maltcid.MALTVersionID)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.codec != wantCodecs[test.name] {
				t.Fatalf("codec = %#x, want %#x", test.codec, wantCodecs[test.name])
			}
			root := cidWithIdentityDigest(t, test.codec, commitmentSize(test.backend))
			if !maltcid.IsMaltCid(root) {
				t.Fatal("IsMaltCid rejected a registered codec")
			}
			if got := maltcid.VersionIDOf(root); got != test.version {
				t.Fatalf("VersionIDOf = %d, want %d", got, test.version)
			}
			if got := maltcid.SemanticKindOf(root); got != test.semantic {
				t.Fatalf("SemanticKindOf = %q, want %q", got, test.semantic)
			}
			if got := maltcid.BackendKindOf(root); got != test.backend {
				t.Fatalf("BackendKindOf = %q, want %q", got, test.backend)
			}
		})
	}
}

func TestCodecClassificationRejectsUnsupportedFields(t *testing.T) {
	tests := []struct {
		name        string
		codec       uint64
		versionHint uint8
	}{
		{name: "old_flat_map_kzg", codec: 0x300001},
		{name: "old_flat_list_kzg", codec: 0x300002},
		{name: "old_flat_map_ipa", codec: 0x300003},
		{name: "old_flat_list_ipa", codec: 0x300004},
		{name: "version_zero", codec: 0x300101},
		{name: "version_two", codec: 0x302101, versionHint: 2},
		{name: "version_fifteen", codec: 0x30F101, versionHint: 15},
		{name: "semantic_zero", codec: 0x301001, versionHint: 1},
		{name: "semantic_unknown", codec: 0x301301, versionHint: 1},
		{name: "semantic_fifteen", codec: 0x301F01, versionHint: 1},
		{name: "backend_zero", codec: 0x301100, versionHint: 1},
		{name: "backend_unknown", codec: 0x301103, versionHint: 1},
		{name: "backend_255", codec: 0x3011FF, versionHint: 1},
		{name: "outside_root_subrange", codec: 0x311101},
		{name: "high_bit_alias", codec: 0x100301101},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := cidWithIdentityDigest(t, test.codec, maltcid.KZGCommitmentSize)
			if maltcid.IsMaltCid(root) {
				t.Fatalf("IsMaltCid accepted unsupported codec %#x", test.codec)
			}
			if got := maltcid.VersionIDOf(root); got != test.versionHint {
				t.Fatalf("VersionIDOf = %d, want %d", got, test.versionHint)
			}
			if got := maltcid.SemanticKindOf(root); got != maltcid.SemanticKindUnknown {
				t.Fatalf("SemanticKindOf = %q, want unknown", got)
			}
			if got := maltcid.BackendKindOf(root); got != maltcid.BackendKindUnknown {
				t.Fatalf("BackendKindOf = %q, want unknown", got)
			}
			if got := maltcid.GetMaltCodec(root); got != 0 {
				t.Fatalf("GetMaltCodec = %#x, want 0", got)
			}
			if _, err := maltcid.ExtractCommitment(root); err == nil {
				t.Fatal("ExtractCommitment accepted unsupported codec")
			}
		})
	}
}

func TestNewKZGCid(t *testing.T) {
	// Create a valid KZG commitment (48 bytes)
	commitment := make([]byte, maltcid.KZGCommitmentSize)
	for i := range commitment {
		commitment[i] = byte(i)
	}

	c, err := maltcid.NewKZGCid(commitment)
	if err != nil {
		t.Fatalf("NewKZGCid failed: %v", err)
	}

	// Check version is CIDv1
	if c.Version() != 1 {
		t.Errorf("Expected CIDv1, got version %d", c.Version())
	}

	// Check codec
	if c.Prefix().Codec != maltcid.CodecMaltMapKZG {
		t.Errorf("Expected codec %x, got %x", maltcid.CodecMaltMapKZG, c.Prefix().Codec)
	}
	if maltcid.CodecMaltKZG != maltcid.CodecMaltMapKZG {
		t.Error("CodecMaltKZG alias must equal CodecMaltMapKZG")
	}

	// Check it's a MALT CID
	if !maltcid.IsMaltCid(c) {
		t.Error("Expected IsMaltCid to return true")
	}
	if version := maltcid.VersionIDOf(c); version != maltcid.MALTVersionID {
		t.Errorf("VersionIDOf = %d, want %d", version, maltcid.MALTVersionID)
	}

	// Extract commitment
	extracted, err := maltcid.ExtractCommitment(c)
	if err != nil {
		t.Fatalf("ExtractCommitment failed: %v", err)
	}

	// Verify extracted matches original
	if len(extracted) != len(commitment) {
		t.Errorf("Extracted size %d != original size %d", len(extracted), len(commitment))
	}
	for i := range commitment {
		if extracted[i] != commitment[i] {
			t.Errorf("Extracted[%d] = %d, expected %d", i, extracted[i], commitment[i])
		}
	}
}

func TestNewKZGCidInvalidSize(t *testing.T) {
	// Create an invalid commitment (wrong size)
	commitment := make([]byte, 32) // wrong size

	_, err := maltcid.NewKZGCid(commitment)
	if err == nil {
		t.Error("Expected error for invalid size")
	}
}

func TestNewTypedCIDRejectsUnknownKinds(t *testing.T) {
	tests := []struct {
		name     string
		semantic maltcid.SemanticKind
		backend  maltcid.BackendKind
	}{
		{name: "unknown_semantic", semantic: maltcid.SemanticKindUnknown, backend: maltcid.BackendKindKZG},
		{name: "unknown_backend", semantic: maltcid.SemanticKindMap, backend: maltcid.BackendKindUnknown},
		{name: "unregistered_semantic", semantic: maltcid.SemanticKind("tree"), backend: maltcid.BackendKindKZG},
		{name: "unregistered_backend", semantic: maltcid.SemanticKindMap, backend: maltcid.BackendKind("future")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := maltcid.NewTypedCID(test.semantic, test.backend, make([]byte, maltcid.KZGCommitmentSize)); err == nil {
				t.Fatal("NewTypedCID accepted an unregistered kind")
			}
		})
	}
}

func TestNewIPACid(t *testing.T) {
	// Create a valid IPA commitment (32 bytes)
	commitment := make([]byte, maltcid.IPACommitmentSize)
	for i := range commitment {
		commitment[i] = byte(i)
	}

	c, err := maltcid.NewIPACid(commitment)
	if err != nil {
		t.Fatalf("NewIPACid failed: %v", err)
	}

	// Check codec
	if c.Prefix().Codec != maltcid.CodecMaltMapIPA {
		t.Errorf("Expected codec %x, got %x", maltcid.CodecMaltMapIPA, c.Prefix().Codec)
	}

	// Check it's a MALT CID
	if !maltcid.IsMaltCid(c) {
		t.Error("Expected IsMaltCid to return true")
	}
}

func TestIsMaltCidFalse(t *testing.T) {
	// Create a regular CID (not MALT)
	c := cid.NewCidV1(cid.Raw, nil)
	if maltcid.IsMaltCid(c) {
		t.Error("Expected IsMaltCid to return false for raw CID")
	}
}

func TestGetMaltCodec(t *testing.T) {
	commitment := make([]byte, maltcid.KZGCommitmentSize)
	c, _ := maltcid.NewKZGCid(commitment)

	codecType := maltcid.GetMaltCodec(c)
	if codecType != maltcid.CodecMaltMapKZG {
		t.Errorf("Expected codec %x, got %x", maltcid.CodecMaltMapKZG, codecType)
	}

	// Test non-MALT CID
	rawCid := cid.NewCidV1(cid.Raw, nil)
	codecType = maltcid.GetMaltCodec(rawCid)
	if codecType != 0 {
		t.Errorf("Expected 0 for non-MALT CID, got %x", codecType)
	}
}

func TestNewListKZGCid(t *testing.T) {
	commitment := make([]byte, maltcid.KZGCommitmentSize)
	c, err := maltcid.NewListKZGCid(commitment)
	if err != nil {
		t.Fatalf("NewListKZGCid: %v", err)
	}
	if c.Prefix().Codec != maltcid.CodecMaltListKZG {
		t.Errorf("codec %x, want %x", c.Prefix().Codec, maltcid.CodecMaltListKZG)
	}
	if !maltcid.IsMaltCid(c) {
		t.Error("IsMaltCid should be true for list kzg")
	}
}

func TestEqualCommitmentAcrossSemanticCodecs(t *testing.T) {
	commitment := make([]byte, maltcid.KZGCommitmentSize)
	for i := range commitment {
		commitment[i] = byte(i)
	}
	mapCID, err := maltcid.NewMapKZGCid(commitment)
	if err != nil {
		t.Fatal(err)
	}
	listCID, err := maltcid.NewListKZGCid(commitment)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := maltcid.EqualCommitment(mapCID, listCID)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected EqualCommitment(map,list) to be true")
	}
}

func TestCodecName(t *testing.T) {
	tests := []struct {
		codec    uint64
		expected string
	}{
		{maltcid.CodecMaltMapKZG, "malt-map-kzg"},
		{maltcid.CodecMaltListKZG, "malt-list-kzg"},
		{maltcid.CodecMaltMapIPA, "malt-map-ipa"},
		{maltcid.CodecMaltListIPA, "malt-list-ipa"},
		{0x999999, "unknown-999999"},
	}

	for _, tt := range tests {
		name := maltcid.CodecName(tt.codec)
		if name != tt.expected {
			t.Errorf("CodecName(%x) = %s, expected %s", tt.codec, name, tt.expected)
		}
	}
}

func TestExtractCommitmentNonMalt(t *testing.T) {
	// Create a raw CID
	c := cid.NewCidV1(cid.Raw, nil)

	_, err := maltcid.ExtractCommitment(c)
	if err == nil {
		t.Error("Expected error for non-MALT CID")
	}
}

func TestExtractCommitmentRejectsInvalidDigestSize(t *testing.T) {
	tests := []struct {
		codec uint64
		size  int
	}{
		{codec: maltcid.CodecMaltMapKZG, size: maltcid.KZGCommitmentSize},
		{codec: maltcid.CodecMaltListKZG, size: maltcid.KZGCommitmentSize},
		{codec: maltcid.CodecMaltMapIPA, size: maltcid.IPACommitmentSize},
		{codec: maltcid.CodecMaltListIPA, size: maltcid.IPACommitmentSize},
	}

	for _, test := range tests {
		invalidSizes := []struct {
			name string
			size int
		}{
			{name: "short", size: test.size - 1},
			{name: "long", size: test.size + 1},
		}
		for _, invalid := range invalidSizes {
			name := maltcid.CodecName(test.codec) + "/" + invalid.name
			t.Run(name, func(t *testing.T) {
				invalidSize := invalid.size
				digest := make([]byte, invalidSize)
				hash, err := mh.Encode(digest, mh.IDENTITY)
				if err != nil {
					t.Fatalf("encode identity multihash: %v", err)
				}
				malformed := cid.NewCidV1(test.codec, hash)
				if maltcid.IsMaltCid(malformed) {
					t.Fatalf("IsMaltCid accepted %d-byte digest, want %d", invalidSize, test.size)
				}
				if got := maltcid.SemanticKindOf(malformed); got != maltcid.SemanticKindUnknown {
					t.Fatalf("SemanticKindOf = %q, want unknown", got)
				}
				if got := maltcid.BackendKindOf(malformed); got != maltcid.BackendKindUnknown {
					t.Fatalf("BackendKindOf = %q, want unknown", got)
				}
				if _, err := maltcid.ExtractCommitment(malformed); err == nil {
					t.Fatalf("ExtractCommitment accepted %d-byte digest, want %d", invalidSize, test.size)
				}
			})
		}
	}
}

func TestExtractCommitmentRejectsNonIdentityMultihash(t *testing.T) {
	hash, err := mh.Sum([]byte("not a raw commitment"), mh.SHA2_256, -1)
	if err != nil {
		t.Fatalf("create sha2-256 multihash: %v", err)
	}
	root := cid.NewCidV1(maltcid.CodecMaltMapKZG, hash)
	if maltcid.IsMaltCid(root) {
		t.Fatal("IsMaltCid accepted non-identity multihash")
	}
	if got := maltcid.SemanticKindOf(root); got != maltcid.SemanticKindUnknown {
		t.Fatalf("SemanticKindOf = %q, want unknown", got)
	}
	if got := maltcid.BackendKindOf(root); got != maltcid.BackendKindUnknown {
		t.Fatalf("BackendKindOf = %q, want unknown", got)
	}
	if _, err := maltcid.ExtractCommitment(root); err == nil {
		t.Fatal("ExtractCommitment accepted non-identity multihash")
	}
}

func cidWithIdentityDigest(t *testing.T, codec uint64, size int) cid.Cid {
	t.Helper()
	hash, err := mh.Encode(make([]byte, size), mh.IDENTITY)
	if err != nil {
		t.Fatalf("encode identity multihash: %v", err)
	}
	return cid.NewCidV1(codec, hash)
}

func commitmentSize(backend maltcid.BackendKind) int {
	if backend == maltcid.BackendKindKZG {
		return maltcid.KZGCommitmentSize
	}
	return maltcid.IPACommitmentSize
}

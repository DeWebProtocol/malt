package maltcid_test

import (
	"testing"

	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

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

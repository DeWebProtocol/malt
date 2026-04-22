package codec_test

import (
	"testing"

	"github.com/dewebprotocol/malt/core/codec"
	cid "github.com/ipfs/go-cid"
)

func TestNewKZGCid(t *testing.T) {
	// Create a valid KZG commitment (48 bytes)
	commitment := make([]byte, codec.KZGCommitmentSize)
	for i := range commitment {
		commitment[i] = byte(i)
	}

	c, err := codec.NewKZGCid(commitment)
	if err != nil {
		t.Fatalf("NewKZGCid failed: %v", err)
	}

	// Check version is CIDv1
	if c.Version() != 1 {
		t.Errorf("Expected CIDv1, got version %d", c.Version())
	}

	// Check codec
	if c.Prefix().Codec != codec.CodecMaltMapKZG {
		t.Errorf("Expected codec %x, got %x", codec.CodecMaltMapKZG, c.Prefix().Codec)
	}
	if codec.CodecMaltKZG != codec.CodecMaltMapKZG {
		t.Error("CodecMaltKZG alias must equal CodecMaltMapKZG")
	}

	// Check it's a MALT CID
	if !codec.IsMaltCid(c) {
		t.Error("Expected IsMaltCid to return true")
	}

	// Extract commitment
	extracted, err := codec.ExtractCommitment(c)
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

	_, err := codec.NewKZGCid(commitment)
	if err == nil {
		t.Error("Expected error for invalid size")
	}
}

func TestNewIPACid(t *testing.T) {
	// Create a valid IPA commitment (32 bytes)
	commitment := make([]byte, codec.IPACommitmentSize)
	for i := range commitment {
		commitment[i] = byte(i)
	}

	c, err := codec.NewIPACid(commitment)
	if err != nil {
		t.Fatalf("NewIPACid failed: %v", err)
	}

	// Check codec
	if c.Prefix().Codec != codec.CodecMaltMapIPA {
		t.Errorf("Expected codec %x, got %x", codec.CodecMaltMapIPA, c.Prefix().Codec)
	}

	// Check it's a MALT CID
	if !codec.IsMaltCid(c) {
		t.Error("Expected IsMaltCid to return true")
	}
}

func TestIsMaltCidFalse(t *testing.T) {
	// Create a regular CID (not MALT)
	c := cid.NewCidV1(cid.Raw, nil)
	if codec.IsMaltCid(c) {
		t.Error("Expected IsMaltCid to return false for raw CID")
	}
}

func TestGetMaltCodec(t *testing.T) {
	commitment := make([]byte, codec.KZGCommitmentSize)
	c, _ := codec.NewKZGCid(commitment)

	codecType := codec.GetMaltCodec(c)
	if codecType != codec.CodecMaltMapKZG {
		t.Errorf("Expected codec %x, got %x", codec.CodecMaltMapKZG, codecType)
	}

	// Test non-MALT CID
	rawCid := cid.NewCidV1(cid.Raw, nil)
	codecType = codec.GetMaltCodec(rawCid)
	if codecType != 0 {
		t.Errorf("Expected 0 for non-MALT CID, got %x", codecType)
	}
}

func TestNewListKZGCid(t *testing.T) {
	commitment := make([]byte, codec.KZGCommitmentSize)
	c, err := codec.NewListKZGCid(commitment)
	if err != nil {
		t.Fatalf("NewListKZGCid: %v", err)
	}
	if c.Prefix().Codec != codec.CodecMaltListKZG {
		t.Errorf("codec %x, want %x", c.Prefix().Codec, codec.CodecMaltListKZG)
	}
	if !codec.IsMaltCid(c) {
		t.Error("IsMaltCid should be true for list kzg")
	}
}

func TestEqualCommitmentAcrossSemanticCodecs(t *testing.T) {
	commitment := make([]byte, codec.KZGCommitmentSize)
	for i := range commitment {
		commitment[i] = byte(i)
	}
	mapCID, err := codec.NewMapKZGCid(commitment)
	if err != nil {
		t.Fatal(err)
	}
	listCID, err := codec.NewListKZGCid(commitment)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := codec.EqualCommitment(mapCID, listCID)
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
		{codec.CodecMaltMapKZG, "malt-map-kzg"},
		{codec.CodecMaltListKZG, "malt-list-kzg"},
		{codec.CodecMaltMapIPA, "malt-map-ipa"},
		{codec.CodecMaltListIPA, "malt-list-ipa"},
		{0x999999, "unknown-999999"},
	}

	for _, tt := range tests {
		name := codec.CodecName(tt.codec)
		if name != tt.expected {
			t.Errorf("CodecName(%x) = %s, expected %s", tt.codec, name, tt.expected)
		}
	}
}

func TestExtractCommitmentNonMalt(t *testing.T) {
	// Create a raw CID
	c := cid.NewCidV1(cid.Raw, nil)

	_, err := codec.ExtractCommitment(c)
	if err == nil {
		t.Error("Expected error for non-MALT CID")
	}
}

package implicit_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/auth/proof/evidence"
	"github.com/dewebprotocol/malt/cmd/eval/internal/compat/hamt"
	"github.com/dewebprotocol/malt/cmd/eval/internal/compat/implicit"
	"github.com/dewebprotocol/malt/cmd/eval/internal/compat/implicit/codec"
	"github.com/dewebprotocol/malt/storage/cas/mock"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// helperCID creates a CID from raw data using sha2-256 and the raw codec.
func helperCID(data []byte) cid.Cid {
	mhash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cid.Raw, mhash)
}

// helperCIDWithCodec creates a CID from data using the specified codec.
func helperCIDWithCodec(data []byte, codec uint64) cid.Cid {
	mhash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(codec, mhash)
}

// ---------------------------------------------------------------------------
// TestResolve_EmptyPath
// With a valid root and empty path, should return the block content as evidence.
// ---------------------------------------------------------------------------
func TestResolve_EmptyPath(t *testing.T) {
	mockCAS := mock.NewCAS()
	resolver := implicit.NewResolver(mockCAS)

	blockData := []byte("hello world")
	c := helperCID(blockData)
	mockCAS.AddBlock(c, blockData)

	matchedPath, target, ev, err := resolver.Resolve(context.Background(), c, "")
	if err != nil {
		t.Fatalf("Resolve with empty path should not error: %v", err)
	}
	if matchedPath != "" {
		t.Errorf("expected empty matchedPath, got %q", matchedPath)
	}
	if target.Defined() {
		t.Errorf("expected zero CID for target with empty path, got %s", target)
	}
	implEv, ok := ev.(*evidence.ImplicitEvidence)
	if !ok {
		t.Fatalf("expected ImplicitEvidence, got %T", ev)
	}
	if string(implEv.Bytes()) != string(blockData) {
		t.Errorf("evidence bytes mismatch: expected %q, got %q", blockData, implEv.Bytes())
	}
}

// ---------------------------------------------------------------------------
// TestResolve_UndefinedRoot
// Should error with "root is not defined".
// ---------------------------------------------------------------------------
func TestResolve_UndefinedRoot(t *testing.T) {
	mockCAS := mock.NewCAS()
	resolver := implicit.NewResolver(mockCAS)

	_, _, _, err := resolver.Resolve(context.Background(), cid.Cid{}, "some/path")
	if err == nil {
		t.Fatal("expected error for undefined root, got nil")
	}
	if err.Error() != "root is not defined" {
		t.Errorf("expected error %q, got %q", "root is not defined", err.Error())
	}
}

// ---------------------------------------------------------------------------
// TestResolve_NilCAS
// Resolver with nil CAS should error.
// ---------------------------------------------------------------------------
func TestResolve_NilCAS(t *testing.T) {
	resolver := implicit.NewResolver(nil)

	c := helperCID([]byte("data"))
	_, _, _, err := resolver.Resolve(context.Background(), c, "")
	if err == nil {
		t.Fatal("expected error for nil CAS, got nil")
	}
	if err.Error() != "CAS client is nil" {
		t.Errorf("expected error %q, got %q", "CAS client is nil", err.Error())
	}
}

// ---------------------------------------------------------------------------
// TestResolve_BlockNotFound
// CAS returns error for missing block, should propagate error.
// ---------------------------------------------------------------------------
func TestResolve_BlockNotFound(t *testing.T) {
	mockCAS := mock.NewCAS()
	resolver := implicit.NewResolver(mockCAS)

	c := helperCID([]byte("missing block"))

	_, _, _, err := resolver.Resolve(context.Background(), c, "")
	if err == nil {
		t.Fatal("expected error for missing block, got nil")
	}
	// The error should contain "failed to fetch block" from the resolver
	// and the underlying mock error "block not found"
	if err.Error() == "" {
		t.Error("expected non-empty error message")
	}
}

// ---------------------------------------------------------------------------
// TestResolve_RawBlock
// Raw block with empty path should return block content as evidence.
// ---------------------------------------------------------------------------
func TestResolve_RawBlock(t *testing.T) {
	mockCAS := mock.NewCAS()
	resolver := implicit.NewResolver(mockCAS)

	blockData := []byte("raw block content")
	c := helperCID(blockData)
	mockCAS.AddBlock(c, blockData)

	matchedPath, target, ev, err := resolver.Resolve(context.Background(), c, "")
	if err != nil {
		t.Fatalf("Resolve raw block should not error: %v", err)
	}
	if matchedPath != "" {
		t.Errorf("expected empty matchedPath, got %q", matchedPath)
	}
	if target.Defined() {
		t.Errorf("expected zero CID for target, got %s", target)
	}
	implEv, ok := ev.(*evidence.ImplicitEvidence)
	if !ok {
		t.Fatalf("expected ImplicitEvidence, got %T", ev)
	}
	if string(implEv.Bytes()) != string(blockData) {
		t.Errorf("evidence bytes mismatch: expected %q, got %q", blockData, implEv.Bytes())
	}
}

// ---------------------------------------------------------------------------
// TestVerify_NilEvidence
// Should error.
// ---------------------------------------------------------------------------
func TestVerify_NilEvidence(t *testing.T) {
	mockCAS := mock.NewCAS()
	resolver := implicit.NewResolver(mockCAS)

	c := helperCID([]byte("data"))
	target := helperCID([]byte("target"))

	ok, err := resolver.Verify(context.Background(), c, "", target, nil)
	if err == nil {
		t.Fatal("expected error for nil evidence, got nil")
	}
	if ok {
		t.Error("expected false for nil evidence")
	}
}

// ---------------------------------------------------------------------------
// TestVerify_WrongEvidenceType
// Pass ExplicitEvidence instead of ImplicitEvidence, should error.
// ---------------------------------------------------------------------------
func TestVerify_WrongEvidenceType(t *testing.T) {
	mockCAS := mock.NewCAS()
	resolver := implicit.NewResolver(mockCAS)

	c := helperCID([]byte("data"))
	target := helperCID([]byte("target"))

	wrongEv := evidence.NewExplicitEvidence([]byte("proof bytes"))

	ok, err := resolver.Verify(context.Background(), c, "", target, wrongEv)
	if err == nil {
		t.Fatal("expected error for wrong evidence type, got nil")
	}
	if ok {
		t.Error("expected false for wrong evidence type")
	}
}

// ---------------------------------------------------------------------------
// TestVerify_BlockHashMismatch
// Create evidence with bytes that don't match the root CID, should return false.
// ---------------------------------------------------------------------------
func TestVerify_BlockHashMismatch(t *testing.T) {
	mockCAS := mock.NewCAS()
	resolver := implicit.NewResolver(mockCAS)

	// Create a CID from some original data
	originalData := []byte("original block data")
	c := helperCID(originalData)

	// But provide evidence with different bytes
	wrongData := []byte("wrong block data")
	ev := evidence.NewImplicitEvidence(wrongData)

	target := cid.Cid{}

	ok, err := resolver.Verify(context.Background(), c, "", target, ev)
	if err != nil {
		t.Fatalf("Verify should not error on hash mismatch, got: %v", err)
	}
	if ok {
		t.Error("expected false when evidence bytes do not match root CID")
	}
}

// ---------------------------------------------------------------------------
// TestVerify_ValidImplicitEvidence
// Create a raw block, put it in mock CAS, resolve it, verify the evidence matches.
// ---------------------------------------------------------------------------
func TestVerify_ValidImplicitEvidence(t *testing.T) {
	mockCAS := mock.NewCAS()
	resolver := implicit.NewResolver(mockCAS)

	blockData := []byte("verify me")
	c, err := mockCAS.Put(context.Background(), blockData)
	if err != nil {
		t.Fatalf("failed to put block: %v", err)
	}

	// Resolve to get evidence
	_, _, ev, err := resolver.Resolve(context.Background(), c, "")
	if err != nil {
		t.Fatalf("Resolve failed: %v", err)
	}

	// Verify the evidence - for empty path, target should equal root
	ok, err := resolver.Verify(context.Background(), c, "", c, ev)
	if err != nil {
		t.Fatalf("Verify failed: %v", err)
	}
	if !ok {
		t.Error("expected true for valid implicit evidence")
	}
}

// ---------------------------------------------------------------------------
// TestResolve_PathOnRawBlock
// Raw block with path should return block content (can't resolve paths on raw blocks).
// ---------------------------------------------------------------------------
func TestResolve_PathOnRawBlock(t *testing.T) {
	mockCAS := mock.NewCAS()
	resolver := implicit.NewResolver(mockCAS)

	blockData := []byte("raw block with path")
	c := helperCID(blockData)
	mockCAS.AddBlock(c, blockData)

	// Raw blocks cannot resolve path segments; they should just return
	// the block content as evidence with no target.
	matchedPath, target, ev, err := resolver.Resolve(context.Background(), c, "some/segment")
	if err != nil {
		t.Fatalf("Resolve on raw block with path should not error: %v", err)
	}
	if matchedPath != "" {
		t.Errorf("expected empty matchedPath for raw block, got %q", matchedPath)
	}
	if target.Defined() {
		t.Errorf("expected zero CID for target on raw block, got %s", target)
	}
	implEv, ok := ev.(*evidence.ImplicitEvidence)
	if !ok {
		t.Fatalf("expected ImplicitEvidence, got %T", ev)
	}
	if string(implEv.Bytes()) != string(blockData) {
		t.Errorf("evidence bytes mismatch: expected %q, got %q", blockData, implEv.Bytes())
	}
}

// ---------------------------------------------------------------------------
// TestNewResolverWithHAMTConfig
// Should create resolver with custom HAMT configuration.
// ---------------------------------------------------------------------------
func TestNewResolverWithHAMTConfig(t *testing.T) {
	mockCAS := mock.NewCAS()
	cfg := hamt.Config{
		BitWidth: 8,
		HashFunc: hamt.DefaultHashFunc,
		MaxDepth: 64,
	}
	resolver := implicit.NewResolverWithHAMTConfig(mockCAS, cfg)

	if resolver == nil {
		t.Fatal("expected non-nil resolver")
	}
}

// ---------------------------------------------------------------------------
// TestNewResolverWithCodecs
// Should create resolver with custom codec registry.
// ---------------------------------------------------------------------------
func TestNewResolverWithCodecs(t *testing.T) {
	mockCAS := mock.NewCAS()
	registry := codec.NewRegistry()
	resolver := implicit.NewResolverWithCodecs(mockCAS, registry)

	if resolver == nil {
		t.Fatal("expected non-nil resolver")
	}
}

// ---------------------------------------------------------------------------
// TestResolve_DagCborBlock
// Create a dag-cbor block and resolve it with empty path.
// ---------------------------------------------------------------------------
func TestResolve_DagCborBlock(t *testing.T) {
	mockCAS := mock.NewCAS()
	resolver := implicit.NewResolver(mockCAS)

	// Manually construct a minimal dag-cbor encoded map: {"key": "value"}
	// dag-cbor encoding of { "key": "value" }:
	// a1 = map(1), 63 = text(3), "key", 65 = text(5), "value"
	blockData := []byte{0xa1, 0x63, 'k', 'e', 'y', 0x65, 'v', 'a', 'l', 'u', 'e'}
	c := helperCIDWithCodec(blockData, cid.DagCBOR)
	mockCAS.AddBlock(c, blockData)

	matchedPath, target, ev, err := resolver.Resolve(context.Background(), c, "")
	if err != nil {
		t.Fatalf("Resolve dag-cbor block should not error: %v", err)
	}
	if matchedPath != "" {
		t.Errorf("expected empty matchedPath, got %q", matchedPath)
	}
	if target.Defined() {
		t.Errorf("expected zero CID for target, got %s", target)
	}
	implEv, ok := ev.(*evidence.ImplicitEvidence)
	if !ok {
		t.Fatalf("expected ImplicitEvidence, got %T", ev)
	}
	if string(implEv.Bytes()) != string(blockData) {
		t.Errorf("evidence bytes mismatch")
	}
}

// ---------------------------------------------------------------------------
// TestResolve_UnknownCodec
// CID with unknown codec should return block content as evidence.
// ---------------------------------------------------------------------------
func TestResolve_UnknownCodec(t *testing.T) {
	mockCAS := mock.NewCAS()
	resolver := implicit.NewResolver(mockCAS)

	blockData := []byte("unknown codec data")
	// Use identity codec (0x00) which is not registered
	c := helperCIDWithCodec(blockData, 0x00)
	mockCAS.AddBlock(c, blockData)

	matchedPath, target, ev, err := resolver.Resolve(context.Background(), c, "")
	if err != nil {
		t.Fatalf("Resolve with unknown codec should not error: %v", err)
	}
	if matchedPath != "" {
		t.Errorf("expected empty matchedPath, got %q", matchedPath)
	}
	if target.Defined() {
		t.Errorf("expected zero CID for target, got %s", target)
	}
	implEv, ok := ev.(*evidence.ImplicitEvidence)
	if !ok {
		t.Fatalf("expected ImplicitEvidence, got %T", ev)
	}
	if string(implEv.Bytes()) != string(blockData) {
		t.Errorf("evidence bytes mismatch: expected %q, got %q", blockData, implEv.Bytes())
	}
}

// ---------------------------------------------------------------------------
// TestResolve_NilCASWithHAMTConfig
// Resolver with nil CAS and HAMT config should error.
// ---------------------------------------------------------------------------
func TestResolve_NilCASWithHAMTConfig(t *testing.T) {
	cfg := hamt.Config{}
	resolver := implicit.NewResolverWithHAMTConfig(nil, cfg)

	c := helperCID([]byte("data"))
	_, _, _, err := resolver.Resolve(context.Background(), c, "")
	if err == nil {
		t.Fatal("expected error for nil CAS, got nil")
	}
}

// ---------------------------------------------------------------------------
// TestResolve_NilCASWithCodecs
// Resolver with nil CAS and custom codecs should error.
// ---------------------------------------------------------------------------
func TestResolve_NilCASWithCodecs(t *testing.T) {
	registry := codec.NewRegistry()
	resolver := implicit.NewResolverWithCodecs(nil, registry)

	c := helperCID([]byte("data"))
	_, _, _, err := resolver.Resolve(context.Background(), c, "")
	if err == nil {
		t.Fatal("expected error for nil CAS, got nil")
	}
}

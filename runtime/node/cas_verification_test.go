package node

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/storage/cas"
	casmock "github.com/dewebprotocol/malt/storage/cas/mock"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// TestNewNode_WrapsAutoInitCAS confirms the default config path (no
// WithCAS) wraps the read-side CAS in a VerifyingReader.
func TestNewNode_WrapsAutoInitCAS(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.State.RootDir = t.TempDir()
	cfg.State.KVStore.Type = "memory"
	cfg.State.KVStore.Path = filepath.Join(cfg.State.RootDir, "kv")
	cfg.CAS.Mode = "external"
	cfg.CAS.BaseURL = "http://127.0.0.1:4318"

	n, err := NewNode(WithConfig(cfg))
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer n.Close()

	if _, ok := n.CAS().(*cas.VerifyingReader); !ok {
		t.Fatalf("CAS = %T, want *cas.VerifyingReader for the default init path", n.CAS())
	}
}

// TestNewNode_ExplicitCASPassesThrough confirms tests that inject a mock CAS
// can still type-assert it back; the wrapper is opt-in for explicit readers.
func TestNewNode_ExplicitCASPassesThrough(t *testing.T) {
	mock := casmock.NewCAS()
	n, err := NewNode(append(testNodeOptions(t), WithCAS(mock))...)
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer n.Close()
	if got := n.CAS(); got != cas.Reader(mock) {
		t.Fatalf("CAS = %T (%p), want supplied mock %p", got, got, mock)
	}
}

// TestNewNode_WithCASVerification_ForcesWrappingExplicitReader confirms
// callers can opt the explicit-CAS path back into verification.
func TestNewNode_WithCASVerification_ForcesWrappingExplicitReader(t *testing.T) {
	mock := casmock.NewCAS()
	n, err := NewNode(
		WithConfig(testConfig(t)),
		WithCAS(mock),
		WithCASVerification(),
	)
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer n.Close()

	wrapped, ok := n.CAS().(*cas.VerifyingReader)
	if !ok {
		t.Fatalf("CAS = %T, want *cas.VerifyingReader", n.CAS())
	}
	if wrapped.Inner() != cas.Reader(mock) {
		t.Fatal("VerifyingReader.Inner did not return supplied mock")
	}
}

// TestNewNode_WithoutCASVerification_DisablesAutoWrap exercises the escape
// hatch for environments that already verify content elsewhere.
func TestNewNode_WithoutCASVerification_DisablesAutoWrap(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.State.RootDir = t.TempDir()
	cfg.State.KVStore.Type = "memory"
	cfg.State.KVStore.Path = filepath.Join(cfg.State.RootDir, "kv")
	cfg.CAS.Mode = "external"
	cfg.CAS.BaseURL = "http://127.0.0.1:4318"

	n, err := NewNode(WithConfig(cfg), WithoutCASVerification())
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer n.Close()
	if _, wrapped := n.CAS().(*cas.VerifyingReader); wrapped {
		t.Fatalf("CAS = %T, did not expect VerifyingReader when verification is disabled", n.CAS())
	}
}

// TestNewNode_VerificationCatchesTamperedCAS plugs an explicit CAS that
// returns mismatched bytes, opts into verification, and confirms Get rejects
// the corrupted block. This is the end-to-end guarantee that V3 closes.
func TestNewNode_VerificationCatchesTamperedCAS(t *testing.T) {
	mock := casmock.NewCAS()
	hash, err := mh.Sum([]byte("real"), mh.SHA2_256, -1)
	if err != nil {
		t.Fatalf("multihash: %v", err)
	}
	c := cid.NewCidV1(cid.Raw, hash)
	mock.AddBlock(c, []byte("tampered")) // intentionally mismatched

	n, err := NewNode(
		WithConfig(testConfig(t)),
		WithCAS(mock),
		WithCASVerification(),
	)
	if err != nil {
		t.Fatalf("NewNode: %v", err)
	}
	defer n.Close()

	if _, err := n.CAS().Get(context.Background(), c); err == nil {
		t.Fatal("expected verification error from tampered CAS, got nil")
	}
}

// TestWrapCASWithVerification_Idempotent confirms the helper does not stack
// wrappers when applied twice.
func TestWrapCASWithVerification_Idempotent(t *testing.T) {
	mock := casmock.NewCAS()
	first := wrapCASWithVerification(mock)
	second := wrapCASWithVerification(first)
	if first != second {
		t.Fatalf("expected idempotent wrap; first=%p second=%p", first, second)
	}
	if wrapCASWithVerification(nil) != nil {
		t.Fatal("expected nil wrap to return nil")
	}
}

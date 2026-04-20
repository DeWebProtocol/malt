package hamt_test

import (
	"testing"

	"github.com/dewebprotocol/malt/core/cas/mock"
	"github.com/dewebprotocol/malt/core/resolver/step/hamt"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func TestNewResolver(t *testing.T) {
	c := mock.NewCAS()
	r := hamt.NewResolver(c)

	if r == nil {
		t.Error("NewResolver should return non-nil resolver")
	}
}

func TestNewResolverWithOptions(t *testing.T) {
	c := mock.NewCAS()
	r := hamt.NewResolver(c,
		hamt.WithBitWidth(8),
		hamt.WithMaxDepth(100),
	)

	if r == nil {
		t.Error("NewResolver should return non-nil resolver")
	}
}

func TestResolveEmptyPath(t *testing.T) {
	c := mock.NewCAS()
	r := hamt.NewResolver(c)

	// Create a dummy CID
	root := newTestCID("test")

	// Resolve with empty path should return the root
	matchedPath, target, ev, err := r.Resolve(root, "")
	if err != nil {
		t.Errorf("Resolve with empty path should not error: %v", err)
	}
	if matchedPath != "" {
		t.Errorf("Empty path should return empty matchedPath, got %s", matchedPath)
	}
	if !target.Equals(root) {
		t.Error("Empty path should return the root CID")
	}
	if ev != nil {
		t.Error("Empty path should return nil evidence")
	}
}

func TestResolveUndefinedRoot(t *testing.T) {
	c := mock.NewCAS()
	r := hamt.NewResolver(c)

	_, _, _, err := r.Resolve(cid.Cid{}, "key")
	if err == nil {
		t.Error("Resolve with undefined root should error")
	}
}

func TestResolveNilCAS(t *testing.T) {
	r := hamt.NewResolver(nil)

	root := newTestCID("test")
	_, _, _, err := r.Resolve(root, "key")
	if err == nil {
		t.Error("Resolve with nil CAS should error")
	}
}

// Helper to create a test CID
func newTestCID(data string) cid.Cid {
	if data == "" {
		return cid.Cid{}
	}
	mhash, _ := mh.Sum([]byte(data), mh.SHA2_256, -1)
	return cid.NewCidV1(cid.Raw, mhash)
}

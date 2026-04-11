package ipfslocal

import (
	"context"
	"testing"

	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// fakeCID creates a deterministic CID from a string seed.
func fakeCID(seed string) cid.Cid {
	mhash, _ := mh.Sum([]byte(seed), mh.SHA2_256, -1)
	return cid.NewCidV1(cid.Raw, mhash)
}

func TestNewClient(t *testing.T) {
	c := NewClient("http://localhost:5001")
	if c == nil {
		t.Fatal("client should not be nil")
	}
	if c.apiURL != "http://localhost:5001" {
		t.Errorf("expected apiURL http://localhost:5001, got %s", c.apiURL)
	}
}

func TestClientGetWithoutDaemon(t *testing.T) {
	// Test that Get returns a proper error when IPFS daemon is not running
	c := NewClient("http://localhost:59999") // non-existent daemon
	ctx := context.Background()
	testCID := fakeCID("test")

	_, err := c.Get(ctx, testCID)
	if err == nil {
		t.Error("expected error when IPFS daemon is not running")
	}
	t.Logf("Get returned expected error: %v", err)
}

func TestClientHasWithoutDaemon(t *testing.T) {
	// Test that Has returns a proper error when IPFS daemon is not running
	c := NewClient("http://localhost:59999") // non-existent daemon
	ctx := context.Background()
	testCID := fakeCID("test")

	_, err := c.Has(ctx, testCID)
	if err == nil {
		t.Error("expected error when IPFS daemon is not running")
	}
	t.Logf("Has returned expected error: %v", err)
}

func TestClientPutWithoutDaemon(t *testing.T) {
	// Test that Put returns a proper error when IPFS daemon is not running
	c := NewClient("http://localhost:59999") // non-existent daemon
	ctx := context.Background()

	_, err := c.Put(ctx, []byte("test data"))
	if err == nil {
		t.Error("expected error when IPFS daemon is not running")
	}
	t.Logf("Put returned expected error: %v", err)
}

func TestClientImplementsInterface(t *testing.T) {
	// Verify that Client implements the cas.Client interface
	var _ interface {
		Get(ctx context.Context, c cid.Cid) ([]byte, error)
		Put(ctx context.Context, data []byte) (cid.Cid, error)
		Has(ctx context.Context, c cid.Cid) (bool, error)
	} = NewClient("http://localhost:5001")
}

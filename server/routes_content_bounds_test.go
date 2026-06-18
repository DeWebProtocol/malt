package server

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt/api/http"
	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/runtime/node"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// shortReader returns fewer bytes than the requested CID's hash would
// describe. It exists to exercise readContentPayload's bounds-check guard.
type shortReader struct {
	data map[string][]byte
}

func (r *shortReader) Get(_ context.Context, c cid.Cid) ([]byte, error) {
	if d, ok := r.data[c.String()]; ok {
		return d, nil
	}
	return nil, errors.New("not found")
}

func (r *shortReader) Has(_ context.Context, _ cid.Cid) (bool, error) {
	return false, nil
}

func newServerWithShortCAS(t *testing.T, reader *shortReader) *Server {
	t.Helper()
	cfg := config.DefaultConfig()
	cfg.State.RootDir = t.TempDir()
	cfg.State.KVStore.Type = "memory"
	cfg.State.KVStore.Path = filepath.Join(cfg.State.RootDir, "kv")
	cfg.CAS.Mode = "external"
	cfg.CAS.BaseURL = "http://127.0.0.1:4318"

	// The bounds-check guard is defense-in-depth for callers that have
	// opted out of CID verification (the verifying wrapper would otherwise
	// reject mismatched bytes before the slice runs). Disable verification
	// here so the test exercises the bounds path it is meant to cover.
	n, err := node.NewNode(node.WithConfig(cfg), node.WithCAS(reader), node.WithoutCASVerification())
	if err != nil {
		t.Fatalf("create test node: %v", err)
	}
	t.Cleanup(func() { _ = n.Close() })
	return New(n, "127.0.0.1:0")
}

// TestReadContentPayload_RawRejectsOutOfRangeSlice is a regression for the
// historical TOCTOU panic where stat.Size was computed on one CAS Get and a
// subsequent shorter Get caused raw[start:endExclusive] to slice past
// len(raw). The bounds check now returns a structured error instead.
func TestReadContentPayload_RawRejectsOutOfRangeSlice(t *testing.T) {
	hash, _ := mh.Sum([]byte("hello world"), mh.SHA2_256, -1)
	c := cid.NewCidV1(cid.Raw, hash)
	mockData := []byte("hi") // 2 bytes; far short of the 11 we will request
	srv := newServerWithShortCAS(t, &shortReader{data: map[string][]byte{c.String(): mockData}})

	stat := &httpapi.PathStatResponse{Kind: "file", StorageKind: "raw", Key: c.String()}
	_, err := srv.readContentPayload(context.Background(), nil, stat, c, 0, 11)
	if err == nil {
		t.Fatal("expected bounds error, got nil")
	}
	if !strings.Contains(err.Error(), "outside raw payload") {
		t.Fatalf("error %v does not mention bounds violation", err)
	}
}

// TestReadContentPayload_RawAcceptsValidRange is the happy-path complement.
func TestReadContentPayload_RawAcceptsValidRange(t *testing.T) {
	data := []byte("the quick brown fox")
	hash, _ := mh.Sum(data, mh.SHA2_256, -1)
	c := cid.NewCidV1(cid.Raw, hash)
	srv := newServerWithShortCAS(t, &shortReader{data: map[string][]byte{c.String(): data}})

	stat := &httpapi.PathStatResponse{Kind: "file", StorageKind: "raw", Key: c.String()}
	got, err := srv.readContentPayload(context.Background(), nil, stat, c, 4, 9)
	if err != nil {
		t.Fatalf("readContentPayload error: %v", err)
	}
	if string(got) != "quick" {
		t.Fatalf("got %q, want %q", got, "quick")
	}
}

// TestReadContentPayload_RawRejectsNegativeStart locks down the other side
// of the bounds check.
func TestReadContentPayload_RawRejectsNegativeStart(t *testing.T) {
	data := []byte("abcd")
	hash, _ := mh.Sum(data, mh.SHA2_256, -1)
	c := cid.NewCidV1(cid.Raw, hash)
	srv := newServerWithShortCAS(t, &shortReader{data: map[string][]byte{c.String(): data}})

	stat := &httpapi.PathStatResponse{Kind: "file", StorageKind: "raw", Key: c.String()}
	_, err := srv.readContentPayload(context.Background(), nil, stat, c, -1, 1)
	if err == nil {
		t.Fatal("expected error for negative start")
	}
}

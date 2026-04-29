package merkledagimport

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	casmock "github.com/dewebprotocol/malt/core/cas/mock"
	cid "github.com/ipfs/go-cid"
)

func TestImportFileStoresUnixFSDAG(t *testing.T) {
	ctx := context.Background()
	casClient := casmock.NewCAS(casmock.WithoutLatency())
	file := filepath.Join(t.TempDir(), "hello.txt")
	if err := os.WriteFile(file, []byte("hello merkle dag"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	result, err := ImportPath(ctx, casClient, file, Options{
		Model:     ModelUnixFS,
		Layout:    LayoutBalanced,
		ChunkSize: 4,
	})
	if err != nil {
		t.Fatalf("import file: %v", err)
	}
	if result.Root == "" {
		t.Fatal("root should not be empty")
	}
	if result.Files != 1 {
		t.Fatalf("files = %d, want 1", result.Files)
	}
	if result.Bytes != int64(len("hello merkle dag")) {
		t.Fatalf("bytes = %d, want %d", result.Bytes, len("hello merkle dag"))
	}
	root, err := cid.Decode(result.Root)
	if err != nil {
		t.Fatalf("decode root: %v", err)
	}
	if _, err := casClient.Get(ctx, root); err != nil {
		t.Fatalf("root block should be stored in CAS: %v", err)
	}
}

func TestImportDirectoryStoresHAMTUnixFSDAG(t *testing.T) {
	ctx := context.Background()
	casClient := casmock.NewCAS(casmock.WithoutLatency())
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("readme"), 0o644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "guide.txt"), []byte("guide"), 0o644); err != nil {
		t.Fatalf("write guide: %v", err)
	}

	result, err := ImportPath(ctx, casClient, root, Options{
		Model:     ModelUnixFS,
		Layout:    LayoutHAMT,
		ChunkSize: 4,
	})
	if err != nil {
		t.Fatalf("import directory: %v", err)
	}
	if result.Files != 2 {
		t.Fatalf("files = %d, want 2", result.Files)
	}
	if result.Bytes != int64(len("readme")+len("guide")) {
		t.Fatalf("bytes = %d", result.Bytes)
	}
	rootCID, err := cid.Decode(result.Root)
	if err != nil {
		t.Fatalf("decode root: %v", err)
	}
	if rootCID.Type() != cid.DagProtobuf {
		t.Fatalf("root codec = %d, want dag-pb", rootCID.Type())
	}
	if _, err := casClient.Get(ctx, rootCID); err != nil {
		t.Fatalf("directory root should be stored in CAS: %v", err)
	}
}

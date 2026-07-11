package unixfs_test

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt/auth/commitment/ipa"
	"github.com/dewebprotocol/malt/auth/semantic/list"
	"github.com/dewebprotocol/malt/auth/semantic/mapping"
	"github.com/dewebprotocol/malt/layout/unixfs"
	"github.com/dewebprotocol/malt/runtime/arctable/overwrite"
	"github.com/dewebprotocol/malt/runtime/semantic/list/tree"
	mappingradix "github.com/dewebprotocol/malt/runtime/semantic/mapping/radix"
	"github.com/dewebprotocol/malt/storage/cas"
	casmock "github.com/dewebprotocol/malt/storage/cas/mock"
	kvmemory "github.com/dewebprotocol/malt/storage/kv/memory"
	cid "github.com/ipfs/go-cid"
)

type readOnlyCAS struct {
	reader cas.Reader
}

func (r *readOnlyCAS) Get(ctx context.Context, c cid.Cid) ([]byte, error) {
	return r.reader.Get(ctx, c)
}

func (r *readOnlyCAS) Has(ctx context.Context, c cid.Cid) (bool, error) {
	return r.reader.Has(ctx, c)
}

func TestReaderFacadeAcceptsReadOnlyCAS(t *testing.T) {
	ctx := context.Background()
	maps, lists := newFacadeSemantics(t)
	blocks := casmock.NewCAS()
	writer, err := unixfs.NewWriter(unixfs.WriterOptions{
		Namespace:   "facade-capability-test",
		ChunkSize:   4,
		Map:         maps,
		List:        lists,
		Blocks:      blocks,
		BlockWriter: blocks,
	})
	if err != nil {
		t.Fatalf("NewWriter failed: %v", err)
	}
	root, err := writer.AddFile(ctx, cid.Undef, "docs/readme.txt", []byte("reader-only"))
	if err != nil {
		t.Fatalf("AddFile failed: %v", err)
	}

	readBlocks := &readOnlyCAS{reader: blocks}
	if _, ok := any(readBlocks).(cas.Writer); ok {
		t.Fatal("read-only CAS unexpectedly implements cas.Writer")
	}
	reader, err := unixfs.NewReader(unixfs.ReaderOptions{
		Namespace: "facade-capability-test",
		ChunkSize: 4,
		Map:       maps,
		List:      lists,
		Blocks:    readBlocks,
	})
	if err != nil {
		t.Fatalf("NewReader failed: %v", err)
	}
	if _, ok := reader.(interface {
		AddFile(context.Context, cid.Cid, string, []byte) (cid.Cid, error)
	}); ok {
		t.Fatal("Reader facade exposes AddFile")
	}

	got, err := reader.ReadFile(ctx, root, "docs/readme.txt")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(got) != "reader-only" {
		t.Fatalf("ReadFile = %q, want reader-only", got)
	}
}

func TestNewWriterRequiresExplicitCASWriter(t *testing.T) {
	maps, lists := newFacadeSemantics(t)
	_, err := unixfs.NewWriter(unixfs.WriterOptions{
		Namespace: "facade-writer-test",
		Map:       maps,
		List:      lists,
		Blocks:    &readOnlyCAS{reader: casmock.NewCAS()},
	})
	if err == nil || !strings.Contains(err.Error(), "CAS writer is nil") {
		t.Fatalf("NewWriter error = %v, want CAS writer is nil", err)
	}
}

func TestProductionDoesNotImportConcreteSemanticRuntime(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(sourceFile)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("unquote import in %s: %v", name, err)
			}
			if strings.HasPrefix(importPath, "github.com/dewebprotocol/malt/runtime/") {
				t.Errorf("%s imports runtime package %q", name, importPath)
			}
		}
	}
}

func newFacadeSemantics(t *testing.T) (mapping.Semantics, list.Semantics) {
	t.Helper()
	kv := kvmemory.New()
	arcs, err := overwrite.NewArcTable(overwrite.WithKVStore(kv))
	if err != nil {
		t.Fatalf("overwrite.NewArcTable failed: %v", err)
	}
	scheme, err := ipa.NewScheme()
	if err != nil {
		t.Fatalf("ipa.NewScheme failed: %v", err)
	}
	maps, err := mappingradix.NewMap(scheme, arcs)
	if err != nil {
		t.Fatalf("radix.NewMap failed: %v", err)
	}
	lists, err := tree.NewList(scheme, arcs)
	if err != nil {
		t.Fatalf("tree.NewList failed: %v", err)
	}
	return maps, lists
}

var _ cas.Reader = (*readOnlyCAS)(nil)

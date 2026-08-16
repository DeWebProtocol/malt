package unixfs_test

import (
	"bytes"
	"context"
	"testing"

	unixfs "github.com/dewebprotocol/malt-client/unixfs"
	cid "github.com/ipfs/go-cid"
)

type flushCountingBlocks struct {
	*realRemote
	flushes int
}

type capturingRootCreator struct {
	*realRemote
	bindings map[string]string
}

func (c *capturingRootCreator) CreateStagedRoot(ctx context.Context, bindings map[string]string) (cid.Cid, error) {
	c.bindings = make(map[string]string, len(bindings))
	for path, target := range bindings {
		c.bindings[path] = target
	}
	return c.realRemote.CreateStagedRoot(ctx, bindings)
}

func (b *flushCountingBlocks) Flush(context.Context) error {
	b.flushes++
	return nil
}

func TestLayoutKindsAreVersionedAndClosed(t *testing.T) {
	for _, kind := range []unixfs.LayoutKind{unixfs.LayoutFlatV1, unixfs.LayoutHybridV1} {
		parsed, err := unixfs.ParseLayoutKind(string(kind))
		if err != nil || parsed != kind {
			t.Fatalf("ParseLayoutKind(%q) = %q, %v", kind, parsed, err)
		}
		layout, err := unixfs.NewLayout(kind)
		if err != nil || layout.Kind() != kind {
			t.Fatalf("NewLayout(%q) = %#v, %v", kind, layout, err)
		}
	}
	for _, invalid := range []string{"", "flat", "hybrid", "FLAT-V2"} {
		if _, err := unixfs.ParseLayoutKind(invalid); err == nil {
			t.Fatalf("ParseLayoutKind(%q) succeeded", invalid)
		}
	}
}

func TestFlatLayoutMaterializesOneMapAndRawDirectoryManifests(t *testing.T) {
	remote := newRealRemote(t)
	root := unixfs.NewStagedDirectory()
	docs := unixfs.EnsureStagedDirectory(root, "docs")
	docs.Changed = true
	readme, err := remote.Put(t.Context(), []byte("readme"))
	if err != nil {
		t.Fatal(err)
	}
	rootFile, err := remote.Put(t.Context(), []byte("root"))
	if err != nil {
		t.Fatal(err)
	}
	if err := unixfs.SetStagedFile(root, "docs/readme.txt", readme); err != nil {
		t.Fatal(err)
	}
	if err := unixfs.SetStagedFile(root, "root.txt", rootFile); err != nil {
		t.Fatal(err)
	}
	layout, err := unixfs.NewLayout(unixfs.LayoutFlatV1)
	if err != nil {
		t.Fatal(err)
	}
	blocks := &flushCountingBlocks{realRemote: remote}
	roots := &capturingRootCreator{realRemote: remote}
	result, err := layout.Materialize(t.Context(), roots, blocks, root)
	if err != nil {
		t.Fatal(err)
	}
	if result.MALTMaps != 1 || result.MALTObjects != 1 || result.ArcSets != 1 || result.ArcCount != 4 {
		t.Fatalf("flat materialization stats = %#v", result)
	}
	if blocks.flushes != 1 {
		t.Fatalf("flat manifest batch flushes = %d, want 1", blocks.flushes)
	}
	for _, path := range []string{"@payload", "docs", "docs/readme.txt", "root.txt"} {
		if roots.bindings[path] == "" {
			t.Fatalf("flat root omitted binding %q: %#v", path, roots.bindings)
		}
	}
	if len(roots.bindings) != 4 || roots.bindings["docs"] != result.Descendants["docs"].String() {
		t.Fatalf("flat root bindings = %#v", roots.bindings)
	}
	if len(result.Descendants) != 3 || result.Descendants["docs"].Equals(result.Key) {
		t.Fatalf("flat descendants = %#v", result.Descendants)
	}

	reader, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: remote, Blocks: remote})
	if err != nil {
		t.Fatal(err)
	}
	directory, err := reader.Stat(t.Context(), result.Key, "docs")
	if err != nil {
		t.Fatal(err)
	}
	if directory.Kind != unixfs.StagedKindDirectory || directory.StorageKind != "raw" ||
		directory.PayloadBinding != nil || len(directory.Entries) != 1 {
		t.Fatalf("flat directory stat = %#v", directory)
	}
	file, err := reader.ReadFile(t.Context(), result.Key, "docs/readme.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(file.Body, []byte("readme")) {
		t.Fatalf("flat file body = %q", file.Body)
	}
}

func TestVerifiedWriterFlatLayoutRoundTrip(t *testing.T) {
	remote := newRealRemote(t)
	layout, err := unixfs.NewLayout(unixfs.LayoutFlatV1)
	if err != nil {
		t.Fatal(err)
	}
	writer, err := unixfs.NewWriter(unixfs.WriterOptions{
		Remote: remote, Blocks: remote, Roots: remote, Layout: layout, ChunkSize: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	empty, err := writer.EmptyDirectory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	directory, err := writer.AddDirectory(t.Context(), empty.CandidateRoot, "docs/generated")
	if err != nil {
		t.Fatal(err)
	}
	written, err := writer.AddFile(t.Context(), directory.CandidateRoot, "docs/readme.txt", []byte("flat writer"))
	if err != nil {
		t.Fatal(err)
	}
	read, err := writer.ReadFile(t.Context(), written.CandidateRoot, "docs/readme.txt")
	if err != nil || string(read.Body) != "flat writer" {
		t.Fatalf("flat writer read = %q, %v", keptBody(read), err)
	}
	removed, err := writer.RemovePath(t.Context(), written.CandidateRoot, "docs/generated")
	if err != nil {
		t.Fatal(err)
	}
	if removed.CandidateRoot.Equals(written.CandidateRoot) {
		t.Fatal("flat removal did not change the root")
	}
	if _, err := writer.Stat(t.Context(), removed.CandidateRoot, "docs/generated"); err == nil {
		t.Fatal("flat removal candidate retained the removed directory")
	}
}

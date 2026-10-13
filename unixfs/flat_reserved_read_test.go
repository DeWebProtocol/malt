package unixfs_test

import (
	"testing"

	unixfs "github.com/dewebprotocol/malt-client/unixfs"
	model "github.com/dewebprotocol/malt-client/unixfs/model"
	cid "github.com/ipfs/go-cid"
)

func TestFlatReaderDistinguishesLiteralPayloadNameFromMetadata(t *testing.T) {
	remote := newRealRemote(t)
	manifest := func(entries []model.DirectoryEntry) cid.Cid {
		t.Helper()
		body, err := model.EncodeDirectoryManifest(entries)
		if err != nil {
			t.Fatal(err)
		}
		key, err := remote.PutWithCodec(t.Context(), body.Data, body.Codec)
		if err != nil {
			t.Fatal(err)
		}
		return key
	}
	bindings := map[string]string{
		"@payload": manifest([]model.DirectoryEntry{
			{Name: "@payload", Type: model.DirectoryEntryTypeFile},
			{Name: "@scope", Type: model.DirectoryEntryTypeDir},
			{Name: "src", Type: model.DirectoryEntryTypeDir},
		}).String(),
		"/@scope": manifest([]model.DirectoryEntry{{Name: "file", Type: model.DirectoryEntryTypeFile}}).String(),
		"src":     manifest([]model.DirectoryEntry{{Name: "@payload", Type: model.DirectoryEntryTypeFile}}).String(),
	}
	paths := []string{"@payload", "@scope/file", "src/@payload"}
	for _, path := range paths {
		key, err := remote.Put(t.Context(), []byte(path))
		if err != nil {
			t.Fatal(err)
		}
		label, err := model.FlatPathLabel(path)
		if err != nil {
			t.Fatal(err)
		}
		bindings[string(label)] = key.String()
	}
	root, err := remote.UpdateStagedRoot(t.Context(), cid.Undef, bindings)
	if err != nil {
		t.Fatal(err)
	}
	opts := unixfs.ReaderOptions{Remote: remote, Blocks: remote, Layout: unixfs.LayoutFlatV1}
	reader, err := unixfs.NewReader(opts)
	if err != nil {
		t.Fatal(err)
	}
	statter, err := unixfs.NewStagedPathStatter(opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			resolved, err := reader.Resolve(t.Context(), root, path)
			if err != nil {
				t.Fatal(err)
			}
			stat, err := reader.(unixfs.LookupReader).Lookup(t.Context(), root, path)
			if err != nil || stat.Kind != unixfs.StagedKindFile || !stat.Payload.Equals(resolved.Target) {
				t.Fatalf("lookup: %+v, %v", stat, err)
			}
			staged, err := statter.StatStagedPath(t.Context(), root.String(), path)
			if err != nil || staged.Kind != unixfs.StagedKindFile || staged.Key != resolved.Target.String() {
				t.Fatalf("staged stat: %+v, %v", staged, err)
			}
			read, err := reader.ReadFile(t.Context(), root, path)
			if err != nil || string(keptBody(read)) != path {
				t.Fatalf("read: %q, %v", keptBody(read), err)
			}
		})
	}
	stat, err := reader.Stat(t.Context(), root, "")
	if err != nil || stat.Kind != unixfs.StagedKindDirectory || len(stat.Entries) != 3 {
		t.Fatalf("root manifest: %+v, %v", stat, err)
	}
}

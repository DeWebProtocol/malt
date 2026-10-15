package importer

import (
	"context"
	"errors"
	"testing"

	casmemory "github.com/dewebprotocol/malt-client/internal/cas/memory"
	mdag "github.com/ipfs/boxo/ipld/merkledag"
	unixfs "github.com/ipfs/boxo/ipld/unixfs"
	cid "github.com/ipfs/go-cid"
)

type batchCountingStore struct {
	*casmemory.Store
	directories []cid.Cid
	fail        bool
}

func (s *batchCountingStore) PutWithCodec(ctx context.Context, data []byte, codec uint64) (cid.Cid, error) {
	directory := false
	if codec == cid.DagProtobuf {
		if node, err := mdag.DecodeProtobuf(data); err == nil {
			if fsnode, err := unixfs.FSNodeFromBytes(node.Data()); err == nil {
				directory = fsnode.Type() == unixfs.TDirectory
			}
		}
	}
	if directory && s.fail {
		return cid.Undef, errors.New("injected directory write failure")
	}
	key, err := s.Store.PutWithCodec(ctx, data, codec)
	if err == nil && directory {
		s.directories = append(s.directories, key)
	}
	return key, err
}

func batchFixture(t *testing.T, files map[string]string) (*Editor, *batchCountingStore) {
	t.Helper()
	store := &batchCountingStore{Store: casmemory.New()}
	editor, err := NewEditor(store, Options{DirLayout: DirLayoutBasic, ChunkSize: 4, PreserveFileMode: true})
	if err != nil {
		t.Fatal(err)
	}
	batch, err := editor.BeginBatch()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range sortedKeys(files) {
		if err := batch.PutFile(t.Context(), path, []byte(files[path]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := batch.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	return editor, store
}

func assertBatchRoot(t *testing.T, editor *Editor, files map[string]string) {
	t.Helper()
	// The independently exercised single-file editor constructs the same final
	// UnixFS graph, including file modes, links, sizes and directory encoding.
	oracle, err := NewEditor(casmemory.New(), editor.opts)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range sortedKeys(files) {
		if err := oracle.PutFile(t.Context(), path, []byte(files[path]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := oracle.EnsureRoot(t.Context()); err != nil {
		t.Fatal(err)
	}
	if editor.Root() != oracle.Root() {
		t.Fatalf("batch root %s differs from independent final-tree root %s", editor.Root(), oracle.Root())
	}
}

func TestBatchWritesOnlyFinalDirectories(t *testing.T) {
	files := map[string]string{"dir/a": "old-a", "dir/b": "old-b", "untouched/file": "stable"}
	editor, store := batchFixture(t, files)
	if len(store.directories) != 3 {
		t.Fatalf("initial directory writes = %d, want dir, untouched and root (3)", len(store.directories))
	}
	before := editor.Root()
	store.directories = nil
	batch, err := editor.BeginBatch()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"dir/a", "dir/b"} {
		files[path] = "new-" + path
		if err := batch.PutFile(t.Context(), path, []byte(files[path]), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.directories) != 0 || editor.Root() != before {
		t.Fatal("uncommitted batch wrote directories or advanced the root")
	}
	if err := batch.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(store.directories) != 2 {
		t.Fatalf("directory writes = %d, want final dir and root (2)", len(store.directories))
	}
	assertBatchRoot(t, editor, files)
	old, err := cid.Parse(before)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(t.Context(), old); err != nil {
		t.Fatalf("old immutable root was not retained: %v", err)
	}
	store.directories = nil
	noop, err := editor.BeginBatch()
	if err != nil {
		t.Fatal(err)
	}
	if err := noop.Commit(t.Context()); err != nil || len(store.directories) != 0 {
		t.Fatalf("no-op wrote directories: %v, %v", store.directories, err)
	}
	if err := noop.Commit(t.Context()); err == nil {
		t.Fatal("closed batch committed twice")
	}
}

func TestBatchPrunesDirectoriesAndHandlesTypeTransitions(t *testing.T) {
	files := map[string]string{"a": "old-a", "b/c": "old-c", "untouched/file": "stable"}
	editor, store := batchFixture(t, files)
	for _, step := range []struct {
		remove []string
		put    map[string]string
		writes int
	}{
		{[]string{"a", "b/c"}, map[string]string{"a/x": "new-x", "b": "new-b"}, 2},
		{[]string{"a/x", "b"}, map[string]string{"a": "final-a", "b/c": "final-c"}, 2},
		{[]string{"a", "b/c", "untouched/file"}, nil, 1},
		{nil, map[string]string{"deep/path/a": "a", "deep/path/b": "b"}, 3},
		{[]string{"deep/path/a", "deep/path/b"}, nil, 1},
	} {
		store.directories = nil
		batch, err := editor.BeginBatch()
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range step.remove {
			if err := batch.RemoveFile(t.Context(), path); err != nil {
				t.Fatal(err)
			}
			delete(files, path)
		}
		for _, path := range sortedKeys(step.put) {
			if err := batch.PutFile(t.Context(), path, []byte(step.put[path]), 0o644); err != nil {
				t.Fatal(err)
			}
			files[path] = step.put[path]
		}
		if err := batch.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(store.directories) != step.writes {
			t.Fatalf("directory writes = %d, want %d", len(store.directories), step.writes)
		}
		assertBatchRoot(t, editor, files)
	}
}

func TestBatchRejectsFailedAndStaleUpdates(t *testing.T) {
	for _, failure := range []string{"missing", "path", "file-ancestor", "storage", "stale", "cancelled"} {
		t.Run(failure, func(t *testing.T) {
			editor, store := batchFixture(t, map[string]string{"file": "original"})
			batch, err := editor.BeginBatch()
			if err != nil {
				t.Fatal(err)
			}
			ctx := t.Context()
			switch failure {
			case "missing":
				err = batch.RemoveFile(ctx, "missing/file")
			case "path":
				err = batch.PutFile(ctx, "../escape", []byte("bad"), 0o644)
			case "file-ancestor":
				err = batch.PutFile(ctx, "file/child", []byte("bad"), 0o644)
			default:
				if err := batch.PutFile(ctx, "file", []byte("updated"), 0o644); err != nil {
					t.Fatal(err)
				}
				if failure == "storage" {
					store.fail = true
				} else if failure == "stale" {
					if err := editor.PutFile(ctx, "other", []byte("other"), 0o644); err != nil {
						t.Fatal(err)
					}
				} else {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				}
			}
			if (failure == "missing" || failure == "path" || failure == "file-ancestor") && err == nil {
				t.Fatal("invalid mutation succeeded")
			}
			before := editor.Root()
			if err := batch.Commit(ctx); err == nil || editor.Root() != before {
				t.Fatalf("failed commit changed root or succeeded: %v", err)
			}
		})
	}
	for _, layout := range []string{DirLayoutHAMT, DirLayoutAdaptive} {
		editor, err := NewEditor(casmemory.New(), Options{DirLayout: layout})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := editor.BeginBatch(); err == nil {
			t.Fatalf("batch accepted unsupported layout %s", layout)
		}
	}
}

package staging

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"sync"
	"testing"

	"github.com/dewebprotocol/malt-client/cache"
	filesystemservice "github.com/dewebprotocol/malt-client/filesystem/service"
	"github.com/dewebprotocol/malt-client/journal"
	"github.com/dewebprotocol/malt-client/unixfs"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

type fakeBase struct {
	mu         sync.Mutex
	infos      map[string]filesystemservice.Info
	bodies     map[string][]byte
	rangeReads int
}

func (b *fakeBase) Stat(_ context.Context, _ filesystemservice.View, name string) (filesystemservice.Info, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	info, ok := b.infos[name]
	if !ok {
		return filesystemservice.Info{}, unixfs.ErrNotFound
	}
	return info, nil
}

func (b *fakeBase) ReadDir(_ context.Context, _ filesystemservice.View, directory string) ([]filesystemservice.DirEntry, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	info, ok := b.infos[directory]
	if !ok {
		return nil, unixfs.ErrNotFound
	}
	if !info.IsDir() {
		return nil, unixfs.ErrNotDirectory
	}
	entries := make([]filesystemservice.DirEntry, 0)
	for name, child := range b.infos {
		if name == "" || name == directory || parentPath(name) != directory {
			continue
		}
		entries = append(entries, filesystemservice.DirEntry{Name: path.Base(name), Kind: child.Kind})
	}
	slices.SortFunc(entries, func(left, right filesystemservice.DirEntry) int { return stringsCompare(left.Name, right.Name) })
	return entries, nil
}

func (b *fakeBase) ReadFileRange(_ context.Context, _ filesystemservice.View, name string, offset, length uint64) ([]byte, filesystemservice.Info, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	info, ok := b.infos[name]
	if !ok {
		return nil, filesystemservice.Info{}, unixfs.ErrNotFound
	}
	if info.IsDir() {
		return nil, info, unixfs.ErrNotFile
	}
	b.rangeReads++
	return sliceRange(b.bodies[name], offset, length), info, nil
}

func TestDurableWriteOverlayFsyncRestartAndPinnedHandle(t *testing.T) {
	root := t.TempDir()
	view := stagingTestView(t)
	base := newFakeBase(t)
	service, cacheStore, journalPath := newStagingService(t, root, base)

	first, err := service.StageWrite(t.Context(), view, "docs/new.txt", []byte("first local body"), false)
	if err != nil || first.Status != journal.StatusLocalDirty || first.Sequence != 1 {
		t.Fatalf("first StageWrite=%#v err=%v", first, err)
	}
	handle, err := service.Open(t.Context(), view, "docs/new.txt")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.StageWrite(t.Context(), view, "docs/new.txt", []byte("second local body"), false)
	if err != nil || second.Sequence != 2 {
		t.Fatalf("second StageWrite=%#v err=%v", second, err)
	}
	pinned, err := handle.Read(t.Context(), 0, 64)
	if err != nil || string(pinned) != "first local body" {
		t.Fatalf("pinned handle read=%q err=%v", pinned, err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := handle.Read(t.Context(), 0, 1); !errors.Is(err, ErrClosed) {
		t.Fatalf("closed handle error=%v", err)
	}
	body, info, err := service.ReadFileRange(t.Context(), view, "docs/new.txt", 7, 5)
	if err != nil || string(body) != "local" || info.Size != uint64(len("second local body")) {
		t.Fatalf("overlay read=%q info=%#v err=%v", body, info, err)
	}
	entries, err := service.ReadDir(t.Context(), view, "docs")
	if err != nil || entryNames(entries) != "new.txt,old.txt" {
		t.Fatalf("overlay entries=%#v err=%v", entries, err)
	}
	fsync, err := service.Fsync(t.Context(), view)
	if err != nil || fsync.Profile != LocalFsyncProfile || fsync.MaxSequence != 2 || !fsync.LocalDurable || fsync.RemotePersisted || fsync.RootAccepted || fsync.CandidateRoot != "" {
		t.Fatalf("Fsync=%#v err=%v", fsync, err)
	}
	if base.rangeReads != 0 {
		t.Fatalf("local overlay unexpectedly read remote body %d time(s)", base.rangeReads)
	}

	reopenedJournal, err := journal.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := New(Options{Base: base, Cache: cacheStore, Journal: reopenedJournal})
	if err != nil {
		t.Fatal(err)
	}
	body, _, err = reopened.ReadFileRange(t.Context(), view, "docs/new.txt", 0, 64)
	if err != nil || string(body) != "second local body" {
		t.Fatalf("restart overlay read=%q err=%v", body, err)
	}

	otherView := view
	otherView.Root = stagingTestCID(t, []byte("other accepted root"))
	if _, err := reopened.Stat(t.Context(), otherView, "docs/new.txt"); !errors.Is(err, unixfs.ErrNotFound) {
		t.Fatalf("dirty operation leaked across selected roots: %v", err)
	}
}

func TestNamespaceOverlayMkdirRenameUnlinkAndRemoveDir(t *testing.T) {
	view := stagingTestView(t)
	service, _, _ := newStagingService(t, t.TempDir(), newFakeBase(t))
	if _, err := service.StageMkdir(t.Context(), view, "docs/work", false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StageWrite(t.Context(), view, "docs/work/note.txt", []byte("note"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StageRename(t.Context(), view, "docs/work", "docs/moved", false); err != nil {
		t.Fatal(err)
	}
	body, _, err := service.ReadFileRange(t.Context(), view, "docs/moved/note.txt", 0, 16)
	if err != nil || string(body) != "note" {
		t.Fatalf("renamed local body=%q err=%v", body, err)
	}
	if _, err := service.Stat(t.Context(), view, "docs/work"); !errors.Is(err, unixfs.ErrNotFound) {
		t.Fatalf("rename source still exists: %v", err)
	}
	if _, err := service.StageRemoveDir(t.Context(), view, "docs/moved", false); !errors.Is(err, ErrNotEmpty) {
		t.Fatalf("non-empty remove error=%v", err)
	}
	if _, err := service.StageUnlink(t.Context(), view, "docs/moved/note.txt", false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StageRemoveDir(t.Context(), view, "docs/moved", false); err != nil {
		t.Fatal(err)
	}
	if entries, err := service.ReadDir(t.Context(), view, "docs"); err != nil || entryNames(entries) != "old.txt" {
		t.Fatalf("entries after remove=%#v err=%v", entries, err)
	}

	if _, err := service.StageRename(t.Context(), view, "archive", "renamed", false); err != nil {
		t.Fatal(err)
	}
	body, _, err = service.ReadFileRange(t.Context(), view, "renamed/base.txt", 0, 16)
	if err != nil || string(body) != "base" {
		t.Fatalf("renamed base subtree body=%q err=%v", body, err)
	}
	if entries, err := service.ReadDir(t.Context(), view, "renamed"); err != nil || entryNames(entries) != "base.txt" {
		t.Fatalf("renamed base entries=%#v err=%v", entries, err)
	}
	if _, err := service.StageRename(t.Context(), view, "renamed", "renamed/child", false); err == nil {
		t.Fatal("directory was renamed below itself")
	}
}

func TestOfflineJournalConcurrencyAndExactViewIdentity(t *testing.T) {
	view := stagingTestView(t)
	service, _, _ := newStagingService(t, t.TempDir(), newFakeBase(t))
	var wait sync.WaitGroup
	errs := make(chan error, 16)
	for index := range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			name := fmt.Sprintf("docs/offline-%02d.txt", index)
			operation, err := service.StageWrite(t.Context(), view, name, []byte(name), true)
			if err != nil || operation.Status != journal.StatusOfflineOnly {
				errs <- errors.Join(err, fmt.Errorf("operation=%#v", operation))
			}
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	result, err := service.Fsync(t.Context(), view)
	if err != nil || result.MaxSequence != 16 {
		t.Fatalf("concurrent Fsync=%#v err=%v", result, err)
	}
	entries, err := service.ReadDir(t.Context(), view, "docs")
	if err != nil || len(entries) != 17 {
		t.Fatalf("concurrent entries=%d err=%v", len(entries), err)
	}
	wrong := view
	wrong.Revision++
	entries, err = service.ReadDir(t.Context(), wrong, "docs")
	if err != nil || entryNames(entries) != "old.txt" {
		t.Fatalf("operations leaked across revision: %#v err=%v", entries, err)
	}
}

func TestReconcileRemovesUnreferencedLocalBodyAndRejectsMissingReferencedBody(t *testing.T) {
	root := t.TempDir()
	view := stagingTestView(t)
	base := newFakeBase(t)
	cacheStore, err := cache.Open(filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	orphanBody := []byte("orphan")
	orphanCID, err := rawCID(orphanBody)
	if err != nil {
		t.Fatal(err)
	}
	orphanBinding := bindingFor(view, orphanCID)
	if _, err := cacheStore.PutLocal(orphanBinding, orphanBody, cache.StateLocalDirty); err != nil {
		t.Fatal(err)
	}
	journalStore, err := journal.Open(filepath.Join(root, "operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Options{Base: base, Cache: cacheStore, Journal: journalStore})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cacheStore.Inspect(orphanBinding); !errors.Is(err, cache.ErrMiss) {
		t.Fatalf("orphan cache entry survived reconcile: %v", err)
	}
	operation, err := service.StageWrite(t.Context(), view, "docs/lost.txt", []byte("lost"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := cacheStore.Remove(bindingFromOperation(operation)); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{Base: base, Cache: cacheStore, Journal: journalStore}); err == nil {
		t.Fatal("restart accepted a journaled write with missing local bytes")
	}
}

func TestReconcileRequiresCompletedCandidateBodyUntilAcceptedViewChanges(t *testing.T) {
	root := t.TempDir()
	view := stagingTestView(t)
	base := newFakeBase(t)
	service, cacheStore, journalPath := newStagingService(t, root, base)
	operation, err := service.StageWrite(t.Context(), view, "docs/candidate.txt", []byte("candidate"), false)
	if err != nil {
		t.Fatal(err)
	}
	journalStore, err := journal.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journalStore.FreezeForUpload(operation.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := journalStore.Complete(operation.OperationID, stagingTestCID(t, []byte("candidate root")).String()); err != nil {
		t.Fatal(err)
	}
	if err := cacheStore.Remove(bindingFromOperation(operation)); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{Base: base, Cache: cacheStore, Journal: journalStore}); err == nil {
		t.Fatal("restart accepted a completed but not locally accepted candidate with missing staged bytes")
	}
}

func TestFailedJournalAppendLeavesOnlyReconcileableUnacknowledgedBody(t *testing.T) {
	root := t.TempDir()
	view := stagingTestView(t)
	base := newFakeBase(t)
	cacheStore, err := cache.Open(filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	failed := errors.New("journal disk full")
	service, err := New(Options{Base: base, Cache: cacheStore, Journal: failingJournal{err: failed}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.StageWrite(t.Context(), view, "docs/uncommitted.txt", []byte("uncommitted"), false); !errors.Is(err, failed) {
		t.Fatalf("StageWrite error=%v, want %v", err, failed)
	}
	journalStore, err := journal.Open(filepath.Join(root, "operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{Base: base, Cache: cacheStore, Journal: journalStore}); err != nil {
		t.Fatalf("reconcile unacknowledged cache body: %v", err)
	}
	entries, err := cacheStore.List()
	if err != nil || len(entries) != 0 {
		t.Fatalf("unacknowledged cache entries=%#v err=%v", entries, err)
	}
}

func TestCanceledMutationDoesNotCreateJournalIntent(t *testing.T) {
	view := stagingTestView(t)
	service, _, journalPath := newStagingService(t, t.TempDir(), newFakeBase(t))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := service.StageWrite(ctx, view, "docs/canceled.txt", []byte("canceled"), false); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled StageWrite error=%v", err)
	}
	store, err := journal.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if operations, err := store.List(); err != nil || len(operations) != 0 {
		t.Fatalf("canceled journal operations=%#v err=%v", operations, err)
	}
}

type failingJournal struct{ err error }

func (f failingJournal) Append(journal.Intent, journal.Status) (journal.Operation, error) {
	return journal.Operation{}, f.err
}

func (f failingJournal) List() ([]journal.Operation, error) { return []journal.Operation{}, nil }

func newStagingService(t *testing.T, root string, base Base) (*Service, *cache.Store, string) {
	t.Helper()
	cacheStore, err := cache.Open(filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(root, "operations.json")
	journalStore, err := journal.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	service, err := New(Options{Base: base, Cache: cacheStore, Journal: journalStore})
	if err != nil {
		t.Fatal(err)
	}
	return service, cacheStore, journalPath
}

func newFakeBase(t *testing.T) *fakeBase {
	t.Helper()
	root := stagingTestCID(t, []byte("base root"))
	oldBody := []byte("old remote body")
	oldPayload := stagingTestCID(t, oldBody)
	baseBody := []byte("base")
	basePayload := stagingTestCID(t, baseBody)
	return &fakeBase{
		infos: map[string]filesystemservice.Info{
			"":                 {Path: "", Kind: unixfs.StagedKindDirectory, NodeRoot: root},
			"docs":             {Path: "docs", Name: "docs", Kind: unixfs.StagedKindDirectory, NodeRoot: root},
			"docs/old.txt":     {Path: "docs/old.txt", Name: "old.txt", Kind: unixfs.StagedKindFile, Payload: oldPayload, StorageKind: "raw", Size: uint64(len(oldBody))},
			"archive":          {Path: "archive", Name: "archive", Kind: unixfs.StagedKindDirectory, NodeRoot: root},
			"archive/base.txt": {Path: "archive/base.txt", Name: "base.txt", Kind: unixfs.StagedKindFile, Payload: basePayload, StorageKind: "raw", Size: uint64(len(baseBody))},
		},
		bodies: map[string][]byte{"docs/old.txt": oldBody, "archive/base.txt": baseBody},
	}
}

func stagingTestView(t *testing.T) filesystemservice.View {
	t.Helper()
	return filesystemservice.View{
		DatasetID: "bucket-one", Branch: "main",
		Root: stagingTestCID(t, []byte("accepted root")), Revision: 7,
	}
}

func stagingTestCID(t *testing.T, body []byte) cid.Cid {
	t.Helper()
	digest, err := mh.Sum(body, mh.SHA2_256, -1)
	if err != nil {
		t.Fatal(err)
	}
	return cid.NewCidV1(cid.Raw, digest)
}

func parentPath(value string) string {
	parent := path.Dir(value)
	if parent == "." {
		return ""
	}
	return parent
}

func entryNames(entries []filesystemservice.DirEntry) string {
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name
	}
	slices.Sort(names)
	return stringsJoin(names, ",")
}

func stringsCompare(left, right string) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func stringsJoin(values []string, separator string) string {
	if len(values) == 0 {
		return ""
	}
	result := values[0]
	for _, value := range values[1:] {
		result += separator + value
	}
	return result
}

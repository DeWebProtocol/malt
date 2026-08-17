package staging

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

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
	service, _, journalPath := newStagingService(t, root, base)

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

	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(Options{
		Base: base, CacheDirectory: filepath.Join(root, "cache"), JournalPath: journalPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
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

func TestOffsetWriteAndTruncateAreAtomicDurableOverlayOperations(t *testing.T) {
	root := t.TempDir()
	view := stagingTestView(t)
	base := newFakeBase(t)
	service, _, journalPath := newStagingService(t, root, base)

	first, err := service.StageWriteAt(t.Context(), view, "docs/old.txt", 4, []byte("local"), false)
	if err != nil || first.Sequence != 1 {
		t.Fatalf("first StageWriteAt=%#v err=%v", first, err)
	}
	body, _, err := service.ReadFileRange(t.Context(), view, "docs/old.txt", 0, 64)
	if err != nil || string(body) != "old locale body" {
		t.Fatalf("offset overlay body=%q err=%v", body, err)
	}
	second, err := service.StageWriteAt(t.Context(), view, "docs/old.txt", 17, []byte("!"), false)
	if err != nil || second.Sequence != 2 {
		t.Fatalf("extended StageWriteAt=%#v err=%v", second, err)
	}
	body, _, err = service.ReadFileRange(t.Context(), view, "docs/old.txt", 0, 64)
	if err != nil || !bytes.Equal(body, []byte{'o', 'l', 'd', ' ', 'l', 'o', 'c', 'a', 'l', 'e', ' ', 'b', 'o', 'd', 'y', 0, 0, '!'}) {
		t.Fatalf("zero-filled extension=%v err=%v", body, err)
	}
	third, err := service.StageTruncate(t.Context(), view, "docs/old.txt", 3, false)
	if err != nil || third.Sequence != 3 {
		t.Fatalf("shrink StageTruncate=%#v err=%v", third, err)
	}
	fourth, err := service.StageTruncate(t.Context(), view, "docs/old.txt", 5, false)
	if err != nil || fourth.Sequence != 4 {
		t.Fatalf("extend StageTruncate=%#v err=%v", fourth, err)
	}
	body, _, err = service.ReadFileRange(t.Context(), view, "docs/old.txt", 0, 64)
	if err != nil || !bytes.Equal(body, []byte{'o', 'l', 'd', 0, 0}) {
		t.Fatalf("truncated overlay=%v err=%v", body, err)
	}
	if base.rangeReads != 1 {
		t.Fatalf("offset operations fetched immutable base %d times, want once", base.rangeReads)
	}
	if operation, err := service.StageWriteAt(t.Context(), view, "docs/old.txt", 0, nil, false); err != nil || operation.OperationID != "" {
		t.Fatalf("zero write operation=%#v err=%v", operation, err)
	}
	if _, err := service.StageWriteAt(t.Context(), view, "docs/old.txt", ^uint64(0), []byte("x"), false); err == nil {
		t.Fatal("overflowing offset write was accepted")
	}

	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(Options{Base: base, CacheDirectory: filepath.Join(root, "cache"), JournalPath: journalPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	body, _, err = reopened.ReadFileRange(t.Context(), view, "docs/old.txt", 0, 64)
	if err != nil || !bytes.Equal(body, []byte{'o', 'l', 'd', 0, 0}) {
		t.Fatalf("restarted truncated overlay=%v err=%v", body, err)
	}
}

func TestWholeFileStagingLimitRejectsBeforeReadOrAllocation(t *testing.T) {
	root := t.TempDir()
	view := stagingTestView(t)
	base := newFakeBase(t)
	oversized := base.infos["docs/old.txt"]
	oversized.Size = 17
	base.infos["docs/old.txt"] = oversized
	service, err := New(Options{
		Base: base, CacheDirectory: filepath.Join(root, "cache"),
		JournalPath: filepath.Join(root, "operations.json"), MaxStagedFileBytes: 16,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })

	if _, err := service.StageWrite(t.Context(), view, "docs/new.txt", make([]byte, 17), false); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("oversized StageWrite error=%v", err)
	}
	if _, err := service.StageWriteAt(t.Context(), view, "docs/old.txt", 16, []byte("x"), false); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("oversized StageWriteAt error=%v", err)
	}
	if _, err := service.StageTruncate(t.Context(), view, "docs/old.txt", 17, false); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("oversized StageTruncate error=%v", err)
	}
	if _, err := service.StageWriteAt(t.Context(), view, "docs/old.txt", 0, []byte("x"), false); !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("oversized remote materialization error=%v", err)
	}
	if base.rangeReads != 0 {
		t.Fatalf("oversized remote body was read %d time(s)", base.rangeReads)
	}
	if _, err := New(Options{
		Base: base, CacheDirectory: filepath.Join(t.TempDir(), "cache"),
		JournalPath: filepath.Join(t.TempDir(), "operations.json"), MaxStagedFileBytes: ^uint64(0),
	}); err == nil {
		t.Fatal("file limit beyond local address space was accepted")
	}
}

func TestRestartRejectsDirtyBodyAboveReducedWholeFileLimit(t *testing.T) {
	root := t.TempDir()
	view := stagingTestView(t)
	base := newFakeBase(t)
	first, err := New(Options{
		Base: base, CacheDirectory: filepath.Join(root, "cache"),
		JournalPath: filepath.Join(root, "operations.json"), MaxStagedFileBytes: 32,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.StageWrite(t.Context(), view, "docs/new.txt", bytes.Repeat([]byte("x"), 24), false); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{
		Base: base, CacheDirectory: filepath.Join(root, "cache"),
		JournalPath: filepath.Join(root, "operations.json"), MaxStagedFileBytes: 16,
	}); !errors.Is(err, cache.ErrBodyTooLarge) {
		t.Fatalf("restart with lower staged-file limit error=%v", err)
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

func TestRejectsNonCanonicalViewIdentityBeforePersistingIntent(t *testing.T) {
	view := stagingTestView(t)
	service, _, journalPath := newStagingService(t, t.TempDir(), newFakeBase(t))
	tests := []struct {
		name   string
		mutate func(*filesystemservice.View)
	}{
		{name: "dataset leading whitespace", mutate: func(value *filesystemservice.View) { value.DatasetID = " bucket-one" }},
		{name: "branch trailing whitespace", mutate: func(value *filesystemservice.View) { value.Branch = "main " }},
		{name: "dataset NUL", mutate: func(value *filesystemservice.View) { value.DatasetID = "bucket\x00one" }},
		{name: "branch invalid UTF-8", mutate: func(value *filesystemservice.View) { value.Branch = string([]byte{0xff}) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := view
			test.mutate(&invalid)
			if _, err := service.StageWrite(t.Context(), invalid, "docs/rejected.txt", []byte("rejected"), false); err == nil {
				t.Fatal("StageWrite accepted a non-canonical View identity")
			}
			if _, err := service.Fsync(t.Context(), invalid); err == nil {
				t.Fatal("Fsync accepted a non-canonical View identity")
			}
		})
	}
	store, err := journal.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if operations, err := store.List(); err != nil || len(operations) != 0 {
		t.Fatalf("invalid Views persisted operations=%#v err=%v", operations, err)
	}
}

func TestExclusiveCacheAndJournalLeasesPreventCrossServiceReconcileRace(t *testing.T) {
	root := t.TempDir()
	opts := Options{
		Base: newFakeBase(t), CacheDirectory: filepath.Join(root, "cache"),
		JournalPath: filepath.Join(root, "operations.json"), LeaseTimeout: 30 * time.Millisecond,
	}
	first, err := New(opts)
	if err != nil {
		t.Fatal(err)
	}
	conflicts := []Options{
		opts,
		{Base: opts.Base, CacheDirectory: opts.CacheDirectory, JournalPath: filepath.Join(root, "other-operations.json"), LeaseTimeout: opts.LeaseTimeout},
		{Base: opts.Base, CacheDirectory: filepath.Join(root, "other-cache"), JournalPath: opts.JournalPath, LeaseTimeout: opts.LeaseTimeout},
	}
	for index, conflict := range conflicts {
		if _, err := New(conflict); err == nil {
			t.Fatalf("conflicting Service %d acquired a shared state path", index)
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := New(opts)
	if err != nil {
		t.Fatalf("lease was not released by Close: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Stat(t.Context(), stagingTestView(t), "docs"); !errors.Is(err, ErrServiceClosed) {
		t.Fatalf("closed Service error=%v", err)
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
	service, err := New(Options{
		Base: base, CacheDirectory: filepath.Join(root, "cache"), JournalPath: filepath.Join(root, "operations.json"),
	})
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
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{Base: base, CacheDirectory: filepath.Join(root, "cache"), JournalPath: filepath.Join(root, "operations.json"), LeaseTimeout: time.Second}); err == nil {
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
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{Base: base, CacheDirectory: filepath.Join(root, "cache"), JournalPath: journalPath, LeaseTimeout: time.Second}); err == nil {
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
	service := &Service{base: base, cache: cacheStore, journal: failingJournal{err: failed}}
	if err := service.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := service.StageWrite(t.Context(), view, "docs/uncommitted.txt", []byte("uncommitted"), false); !errors.Is(err, failed) {
		t.Fatalf("StageWrite error=%v, want %v", err, failed)
	}
	reopened, err := New(Options{
		Base: base, CacheDirectory: filepath.Join(root, "cache"), JournalPath: filepath.Join(root, "operations.json"),
	})
	if err != nil {
		t.Fatalf("reconcile unacknowledged cache body: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
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
func (f failingJournal) FreezeBatchForUpload([]string) ([]journal.Operation, error) {
	return nil, f.err
}
func (f failingJournal) MarkBatchConflicted([]string, string) ([]journal.Operation, error) {
	return nil, f.err
}
func (f failingJournal) CompleteBatch([]string, string) ([]journal.Operation, error) {
	return nil, f.err
}

func newStagingService(t *testing.T, root string, base Base) (*Service, *cache.Store, string) {
	t.Helper()
	journalPath := filepath.Join(root, "operations.json")
	service, err := New(Options{Base: base, CacheDirectory: filepath.Join(root, "cache"), JournalPath: journalPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	cacheStore, ok := service.cache.(*cache.Store)
	if !ok {
		t.Fatal("staging Service did not open the durable cache store")
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

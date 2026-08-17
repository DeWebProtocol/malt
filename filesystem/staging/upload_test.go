package staging

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/dewebprotocol/malt-client/cache"
	"github.com/dewebprotocol/malt-client/journal"
)

func TestUploadBatchFreezesRetryIdentityCompletesCandidateAndRetainsHistory(t *testing.T) {
	root := t.TempDir()
	view := stagingTestView(t)
	service, cacheStore, journalPath := newStagingService(t, root, newFakeBase(t))
	first, err := service.StageWrite(t.Context(), view, "docs/first.txt", []byte("first"), false)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := service.PrepareUpload(t.Context(), view)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Operations) != 1 || len(batch.Pending) != 1 || batch.Pending[0].Status != journal.StatusPendingUpload || len(batch.Payloads) != 1 || string(batch.Payloads[0].Body) != "first" {
		t.Fatalf("first upload batch=%#v", batch)
	}
	retry, err := service.PrepareUpload(t.Context(), view)
	if err != nil {
		t.Fatal(err)
	}
	if retry.OperationID != batch.OperationID || retry.Pending[0].RetryID != batch.Pending[0].RetryID {
		t.Fatalf("retry identity changed: first=%#v retry=%#v", batch, retry)
	}
	firstCandidate := stagingTestCID(t, []byte("first candidate"))
	completed, err := service.CompleteUpload(t.Context(), retry, firstCandidate)
	if err != nil || len(completed) != 1 || completed[0].Status != journal.StatusCompleted || completed[0].ResultRoot != firstCandidate.String() {
		t.Fatalf("first completion=%#v err=%v", completed, err)
	}
	if entry, err := cacheStore.Inspect(bindingFromOperation(first)); err != nil || entry.State != cache.StateCandidate {
		t.Fatalf("first candidate cache=%#v err=%v", entry, err)
	}

	second, err := service.StageWrite(t.Context(), view, "docs/second.txt", []byte("second"), true)
	if err != nil {
		t.Fatal(err)
	}
	next, err := service.PrepareUpload(t.Context(), view)
	if err != nil {
		t.Fatal(err)
	}
	if next.OperationID == batch.OperationID || len(next.Operations) != 2 || next.Operations[0].OperationID != first.OperationID || next.Operations[0].Status != journal.StatusCompleted || len(next.Pending) != 1 || next.Pending[0].OperationID != second.OperationID || len(next.Payloads) != 2 {
		t.Fatalf("next upload batch=%#v", next)
	}
	secondCandidate := stagingTestCID(t, []byte("second candidate"))
	if _, err := service.CompleteUpload(t.Context(), next, secondCandidate); err != nil {
		t.Fatal(err)
	}
	reopenedJournal, err := journal.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	operations, err := reopenedJournal.List()
	if err != nil || len(operations) != 2 || operations[0].ResultRoot != firstCandidate.String() || operations[1].ResultRoot != secondCandidate.String() {
		t.Fatalf("candidate history=%#v err=%v", operations, err)
	}
	if _, err := service.PrepareUpload(t.Context(), view); !errors.Is(err, ErrNoPendingUpload) {
		t.Fatalf("completed overlay remained pending: %v", err)
	}
}

func TestUploadBatchConflictBlocksReplayAndMarksCache(t *testing.T) {
	view := stagingTestView(t)
	service, cacheStore, _ := newStagingService(t, t.TempDir(), newFakeBase(t))
	operation, err := service.StageWrite(t.Context(), view, "docs/conflict.txt", []byte("conflict"), false)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := service.PrepareUpload(t.Context(), view)
	if err != nil {
		t.Fatal(err)
	}
	conflicted, err := service.MarkUploadConflicted(t.Context(), batch, "conflict-one")
	if err != nil || len(conflicted) != 1 || conflicted[0].Status != journal.StatusConflicted {
		t.Fatalf("conflicted upload=%#v err=%v", conflicted, err)
	}
	if entry, err := cacheStore.Inspect(bindingFromOperation(operation)); err != nil || entry.State != cache.StateConflicted {
		t.Fatalf("conflicted cache=%#v err=%v", entry, err)
	}
	if _, err := service.PrepareUpload(t.Context(), view); !errors.Is(err, ErrUploadConflict) {
		t.Fatalf("conflicted operation became replayable: %v", err)
	}
	if _, err := service.CompleteUpload(t.Context(), batch, stagingTestCID(t, []byte("malicious candidate"))); !errors.Is(err, ErrUploadBatch) {
		t.Fatalf("conflicted batch completed: %v", err)
	}
}

func TestNoChangeCompletionIsDurableAndRestartRecoverable(t *testing.T) {
	root := t.TempDir()
	view := stagingTestView(t)
	service, cacheStore, journalPath := newStagingService(t, root, newFakeBase(t))
	operation, err := service.StageWrite(t.Context(), view, "docs/same.txt", []byte("same"), false)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := service.PrepareUpload(t.Context(), view)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := service.CompleteNoChange(t.Context(), batch)
	if err != nil || len(completed) != 1 || completed[0].Status != journal.StatusCompleted || completed[0].ResultRoot != view.Root.String() {
		t.Fatalf("no-change completion=%#v err=%v", completed, err)
	}
	if entry, err := cacheStore.Inspect(bindingFromOperation(operation)); err != nil || entry.State != cache.StateCandidate {
		t.Fatalf("no-change cache=%#v err=%v", entry, err)
	}
	if _, err := service.CompleteNoChange(t.Context(), batch); err != nil {
		t.Fatalf("exact no-change retry failed: %v", err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(Options{Base: newFakeBase(t), CacheDirectory: filepath.Join(root, "cache"), JournalPath: journalPath})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := reopened.PrepareUpload(t.Context(), view); !errors.Is(err, ErrNoPendingUpload) {
		t.Fatalf("restarted no-change batch remained pending: %v", err)
	}
}

func TestUploadCompletionRejectsTamperingButAllowsLaterLocalIntent(t *testing.T) {
	view := stagingTestView(t)
	service, _, _ := newStagingService(t, t.TempDir(), newFakeBase(t))
	if _, err := service.StageWrite(t.Context(), view, "docs/one.txt", []byte("one"), false); err != nil {
		t.Fatal(err)
	}
	batch, err := service.PrepareUpload(t.Context(), view)
	if err != nil {
		t.Fatal(err)
	}
	tampered := batch
	tampered.Pending = cloneJournalOperations(batch.Pending)
	tampered.Pending[0].Path = "docs/substituted.txt"
	if _, err := service.CompleteUpload(t.Context(), tampered, stagingTestCID(t, []byte("candidate"))); !errors.Is(err, ErrUploadBatch) {
		t.Fatalf("tampered batch error=%v", err)
	}
	if _, err := service.StageWrite(t.Context(), view, "docs/two.txt", []byte("two"), false); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CompleteUpload(t.Context(), batch, stagingTestCID(t, []byte("first subset candidate"))); err != nil {
		t.Fatalf("later local intent invalidated exact prior batch: %v", err)
	}
	next, err := service.PrepareUpload(t.Context(), view)
	if err != nil || len(next.Pending) != 1 || len(next.Operations) != 2 {
		t.Fatalf("later intent batch=%#v err=%v", next, err)
	}
}

func TestUploadBatchRejectsShrunkenOrDuplicatePendingSet(t *testing.T) {
	view := stagingTestView(t)
	service, _, _ := newStagingService(t, t.TempDir(), newFakeBase(t))
	for _, name := range []string{"docs/one.txt", "docs/two.txt"} {
		if _, err := service.StageWrite(t.Context(), view, name, []byte(name), false); err != nil {
			t.Fatal(err)
		}
	}
	batch, err := service.PrepareUpload(t.Context(), view)
	if err != nil || len(batch.Pending) != 2 {
		t.Fatalf("prepared batch=%#v err=%v", batch, err)
	}
	candidate := stagingTestCID(t, []byte("candidate"))
	shrunk := batch
	shrunk.Pending = cloneJournalOperations(batch.Pending[:1])
	if _, err := service.CompleteUpload(t.Context(), shrunk, candidate); !errors.Is(err, ErrUploadBatch) {
		t.Fatalf("shrunken pending set error=%v", err)
	}
	duplicate := batch
	duplicate.Pending = append(cloneJournalOperations(batch.Pending), batch.Pending[0])
	if _, err := service.MarkUploadConflicted(t.Context(), duplicate, "conflict"); !errors.Is(err, ErrUploadBatch) {
		t.Fatalf("duplicate pending set error=%v", err)
	}
}

func TestReconcileRepairsCacheStateAcrossBatchCrashWindows(t *testing.T) {
	root := t.TempDir()
	view := stagingTestView(t)
	service, cacheStore, journalPath := newStagingService(t, root, newFakeBase(t))
	operation, err := service.StageWrite(t.Context(), view, "docs/crash.txt", []byte("crash"), false)
	if err != nil {
		t.Fatal(err)
	}
	store, err := journal.Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FreezeBatchForUpload([]string{operation.OperationID}); err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if entry, err := cacheStore.Inspect(bindingFromOperation(operation)); err != nil || entry.State != cache.StatePendingUpload {
		t.Fatalf("pending crash repair=%#v err=%v", entry, err)
	}
	candidate := stagingTestCID(t, []byte("crash candidate"))
	if _, err := store.CompleteBatch([]string{operation.OperationID}, candidate.String()); err != nil {
		t.Fatal(err)
	}
	if err := service.Reconcile(t.Context()); err != nil {
		t.Fatal(err)
	}
	if entry, err := cacheStore.Inspect(bindingFromOperation(operation)); err != nil || entry.State != cache.StateCandidate {
		t.Fatalf("candidate crash repair=%#v err=%v", entry, err)
	}

	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(Options{
		Base: newFakeBase(t), CacheDirectory: filepath.Join(root, "cache"), JournalPath: journalPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUploadOutcomeRetryRepairsCacheAfterJournalCommit(t *testing.T) {
	for _, test := range []struct {
		name      string
		apply     func(*Service, UploadBatch) error
		wantState cache.State
	}{
		{
			name: "complete",
			apply: func(service *Service, batch UploadBatch) error {
				_, err := service.CompleteUpload(t.Context(), batch, stagingTestCID(t, []byte("candidate after cache failure")))
				return err
			},
			wantState: cache.StateCandidate,
		},
		{
			name: "no_change",
			apply: func(service *Service, batch UploadBatch) error {
				_, err := service.CompleteNoChange(t.Context(), batch)
				return err
			},
			wantState: cache.StateCandidate,
		},
		{
			name: "conflict",
			apply: func(service *Service, batch UploadBatch) error {
				_, err := service.MarkUploadConflicted(t.Context(), batch, "conflict-after-cache-failure")
				return err
			},
			wantState: cache.StateConflicted,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			view := stagingTestView(t)
			service, durableCache, _ := newStagingService(t, t.TempDir(), newFakeBase(t))
			operation, err := service.StageWrite(t.Context(), view, "docs/retry.txt", []byte("retry body"), false)
			if err != nil {
				t.Fatal(err)
			}
			batch, err := service.PrepareUpload(t.Context(), view)
			if err != nil {
				t.Fatal(err)
			}
			injected := errors.New("cache metadata disk full")
			service.cache = &failOnceReconcileCache{cacheStore: durableCache, err: injected}
			if err := test.apply(service, batch); !errors.Is(err, injected) {
				t.Fatalf("first outcome error=%v, want injected cache failure", err)
			}
			if entry, err := durableCache.Inspect(bindingFromOperation(operation)); err != nil || entry.State != cache.StatePendingUpload {
				t.Fatalf("cache before exact retry=%#v err=%v", entry, err)
			}
			if err := test.apply(service, batch); err != nil {
				t.Fatalf("exact outcome retry failed: %v", err)
			}
			if entry, err := durableCache.Inspect(bindingFromOperation(operation)); err != nil || entry.State != test.wantState {
				t.Fatalf("cache after exact retry=%#v err=%v", entry, err)
			}
		})
	}
}

type failOnceReconcileCache struct {
	cacheStore
	err error
}

func (c *failOnceReconcileCache) ReconcileLocalState(binding cache.Binding, state cache.State) (cache.Entry, error) {
	return c.ReconcileLocalStateBounded(binding, state, DefaultMaxStagedFileBytes)
}

func (c *failOnceReconcileCache) ReconcileLocalStateBounded(binding cache.Binding, state cache.State, maxBytes uint64) (cache.Entry, error) {
	if c.err != nil {
		err := c.err
		c.err = nil
		return cache.Entry{}, err
	}
	return c.cacheStore.ReconcileLocalStateBounded(binding, state, maxBytes)
}

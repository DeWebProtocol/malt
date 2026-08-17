package journal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	cid "github.com/ipfs/go-cid"
	"github.com/multiformats/go-multibase"
	"github.com/multiformats/go-multihash"
)

func TestJournalReplayOrderRetryIdentityAndRestart(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "operations.json")
	store, err := Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	first := testIntent(t, "op-one", "retry-one", KindWrite, "docs/one.txt", "")
	second := testIntent(t, "op-two", "retry-two", KindMkdir, "docs/two", "")
	firstOperation, err := store.Append(first, StatusLocalDirty)
	if err != nil {
		t.Fatal(err)
	}
	secondOperation, err := store.Append(second, StatusOfflineOnly)
	if err != nil {
		t.Fatal(err)
	}
	if firstOperation.Sequence != 1 || secondOperation.Sequence != 2 {
		t.Fatalf("journal sequences = %d, %d", firstOperation.Sequence, secondOperation.Sequence)
	}
	again, err := store.Append(first, StatusLocalDirty)
	if err != nil || again.Sequence != firstOperation.Sequence || again.RetryID != firstOperation.RetryID {
		t.Fatalf("idempotent append = %#v, %v", again, err)
	}
	frozen, err := store.FreezeForUpload(first.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if frozen.Status != StatusPendingUpload || frozen.RetryID != "retry-one" {
		t.Fatalf("frozen operation = %#v", frozen)
	}
	if _, err := store.MarkOffline(first.OperationID); !errors.Is(err, ErrRequestFrozen) {
		t.Fatalf("pending request was demoted to offline-only: %v", err)
	}

	reopened, err := Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := reopened.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 || pending[0].OperationID != first.OperationID || pending[0].RetryID != "retry-one" || pending[1].OperationID != second.OperationID {
		t.Fatalf("replay order after restart = %#v", pending)
	}
	assertJournalOwnerOnly(t, journalPath)
}

func TestBatchFreezeAndCompleteAreAtomicOrderedAndIdempotent(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	firstIntent := testIntent(t, "op-first", "retry-first", KindWrite, "docs/first.txt", "")
	secondIntent := testIntent(t, "op-second", "retry-second", KindMkdir, "docs/second", "")
	first, err := store.Append(firstIntent, StatusLocalDirty)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Append(secondIntent, StatusOfflineOnly)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FreezeBatchForUpload([]string{first.OperationID, "missing"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial freeze error=%v", err)
	}
	if current, err := store.Get(first.OperationID); err != nil || current.Status != StatusLocalDirty {
		t.Fatalf("failed batch changed first operation=%#v err=%v", current, err)
	}
	frozen, err := store.FreezeBatchForUpload([]string{second.OperationID, first.OperationID})
	if err != nil {
		t.Fatal(err)
	}
	if len(frozen) != 2 || frozen[0].Sequence != first.Sequence || frozen[1].Sequence != second.Sequence || frozen[0].Status != StatusPendingUpload || frozen[1].Status != StatusPendingUpload {
		t.Fatalf("frozen batch=%#v", frozen)
	}
	if _, err := store.FreezeBatchForUpload([]string{first.OperationID, second.OperationID}); err != nil {
		t.Fatalf("idempotent freeze: %v", err)
	}
	resultRoot := testJournalCID(t, []byte("verified batch candidate")).String()
	completed, err := store.CompleteBatch([]string{second.OperationID, first.OperationID}, resultRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(completed) != 2 || completed[0].Status != StatusCompleted || completed[1].Status != StatusCompleted || completed[0].ResultRoot != resultRoot || completed[1].ResultRoot != resultRoot {
		t.Fatalf("completed batch=%#v", completed)
	}
	if _, err := store.CompleteBatch([]string{first.OperationID, second.OperationID}, resultRoot); err != nil {
		t.Fatalf("idempotent completion: %v", err)
	}
	otherRoot := testJournalCID(t, []byte("substituted batch candidate")).String()
	if _, err := store.CompleteBatch([]string{first.OperationID, second.OperationID}, otherRoot); !errors.Is(err, ErrIdentityReuse) {
		t.Fatalf("candidate substitution error=%v", err)
	}
}

func TestBatchConflictIsAtomicAndCannotReclassify(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.Append(testIntent(t, "op-first", "retry-first", KindWrite, "docs/first.txt", ""), StatusLocalDirty)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Append(testIntent(t, "op-second", "retry-second", KindUnlink, "docs/second.txt", ""), StatusLocalDirty)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.FreezeBatchForUpload([]string{first.OperationID, second.OperationID}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkBatchConflicted([]string{first.OperationID, "missing"}, "conflict-one"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial conflict error=%v", err)
	}
	if current, err := store.Get(first.OperationID); err != nil || current.Status != StatusPendingUpload {
		t.Fatalf("failed conflict batch changed first=%#v err=%v", current, err)
	}
	conflicted, err := store.MarkBatchConflicted([]string{second.OperationID, first.OperationID}, "conflict-one")
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicted) != 2 || conflicted[0].Status != StatusConflicted || conflicted[1].ConflictID != "conflict-one" {
		t.Fatalf("conflicted batch=%#v", conflicted)
	}
	if _, err := store.MarkBatchConflicted([]string{first.OperationID, second.OperationID}, "conflict-one"); err != nil {
		t.Fatalf("idempotent conflict: %v", err)
	}
	if _, err := store.MarkBatchConflicted([]string{first.OperationID, second.OperationID}, "conflict-two"); !errors.Is(err, ErrIdentityReuse) {
		t.Fatalf("conflict reclassification error=%v", err)
	}
}

func TestConflictResolutionCompletionAndPruningRemainExplicit(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "operations.json")
	store, err := Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	intent := testIntent(t, "op-one", "retry-one", KindWrite, "docs/one.txt", "")
	if _, err := store.Append(intent, StatusLocalDirty); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FreezeForUpload(intent.OperationID); err != nil {
		t.Fatal(err)
	}
	conflicted, err := store.MarkConflicted(intent.OperationID, "conflict-branch-one")
	if err != nil || conflicted.Status != StatusConflicted {
		t.Fatalf("conflicted operation = %#v, %v", conflicted, err)
	}
	store, err = Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if replayable, err := store.Replayable(); err != nil || len(replayable) != 0 {
		t.Fatalf("unresolved conflict became replayable: %#v, %v", replayable, err)
	}
	if unfinished, err := store.Unfinished(); err != nil || len(unfinished) != 1 || unfinished[0].Status != StatusConflicted {
		t.Fatalf("conflict inspection = %#v, %v", unfinished, err)
	}
	replacement := testIntent(t, "op-replacement", "retry-replacement", KindWrite, "docs/one.txt", "")
	replacement.BaseRoot = testJournalCID(t, []byte("merged base root")).String()
	replacement.BaseRevision = 8
	original, resolved, err := store.ResolveConflict(intent.OperationID, replacement, StatusLocalDirty)
	if err != nil || original.Status != StatusSuperseded || original.ReplacementOperationID != replacement.OperationID ||
		resolved.Status != StatusLocalDirty || resolved.RetryID != replacement.RetryID {
		t.Fatalf("resolved operations original=%#v replacement=%#v err=%v", original, resolved, err)
	}
	againOriginal, againReplacement, err := store.ResolveConflict(intent.OperationID, replacement, StatusLocalDirty)
	if err != nil || againOriginal.Sequence != original.Sequence || againReplacement.Sequence != resolved.Sequence {
		t.Fatalf("idempotent conflict resolution = %#v %#v, %v", againOriginal, againReplacement, err)
	}
	if _, err := store.FreezeForUpload(replacement.OperationID); err != nil {
		t.Fatal(err)
	}
	resultRoot := testJournalCID(t, []byte("verified candidate root")).String()
	completed, err := store.Complete(replacement.OperationID, resultRoot)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Status != StatusCompleted || completed.ResultRoot != resultRoot || completed.RetryID != replacement.RetryID {
		t.Fatalf("completed operation = %#v", completed)
	}
	if pending, err := store.Pending(); err != nil || len(pending) != 0 {
		t.Fatalf("pending after completion = %#v, %v", pending, err)
	}
	if reopened, err := Open(journalPath); err != nil {
		t.Fatal(err)
	} else {
		if record, err := reopened.Get(intent.OperationID); err != nil || record.Status != StatusSuperseded {
			t.Fatalf("superseded audit record after restart = %#v, %v", record, err)
		}
		if record, err := reopened.Get(replacement.OperationID); err != nil || record.Status != StatusCompleted {
			t.Fatalf("completed replacement after restart = %#v, %v", record, err)
		}
	}
	if removed, err := store.PruneCompleted(); err != nil || removed != 2 {
		t.Fatalf("pruned completed = %d, %v", removed, err)
	}
	if _, err := store.Get(replacement.OperationID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pruned replacement still exists: %v", err)
	}
	if _, err := store.Get(intent.OperationID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("finished conflict chain still exists: %v", err)
	}
	reusedRetry := testIntent(t, "op-after-prune", replacement.RetryID, KindMkdir, "docs/after-prune", "")
	if _, err := store.Append(reusedRetry, StatusOfflineOnly); !errors.Is(err, ErrIdentityReuse) {
		t.Fatalf("prune released a frozen retry identity: %v", err)
	}
	reusedOperation := testIntent(t, replacement.OperationID, "retry-after-prune", KindMkdir, "docs/after-prune", "")
	if _, err := store.Append(reusedOperation, StatusOfflineOnly); !errors.Is(err, ErrIdentityReuse) {
		t.Fatalf("prune released an operation identity: %v", err)
	}
	restarted, err := Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Append(reusedRetry, StatusOfflineOnly); !errors.Is(err, ErrIdentityReuse) {
		t.Fatalf("restart released a frozen retry tombstone: %v", err)
	}
}

func TestCompleteCanonicalizesEquivalentCIDRepresentationsAcrossRestart(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "operations.json")
	store, err := Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	intent := testIntent(t, "op-one", "retry-one", KindWrite, "docs/one.txt", "")
	if _, err := store.Append(intent, StatusLocalDirty); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FreezeForUpload(intent.OperationID); err != nil {
		t.Fatal(err)
	}
	canonical := testJournalCID(t, []byte("candidate result"))
	alternate, err := canonical.StringOfBase(multibase.Base36)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := store.Complete(intent.OperationID, alternate)
	if err != nil || completed.ResultRoot != canonical.String() {
		t.Fatalf("alternate completion = %#v, %v", completed, err)
	}
	reopened, err := Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	again, err := reopened.Complete(intent.OperationID, canonical.String())
	if err != nil || again.ResultRoot != canonical.String() {
		t.Fatalf("canonical retry = %#v, %v", again, err)
	}
}

func TestJournalRejectsIdentityReuseAndInvalidTransitions(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	intent := testIntent(t, "op-one", "retry-one", KindWrite, "docs/one.txt", "")
	if _, err := store.Append(intent, StatusLocalDirty); err != nil {
		t.Fatal(err)
	}
	changed := intent
	changed.Path = "docs/other.txt"
	if _, err := store.Append(changed, StatusLocalDirty); !errors.Is(err, ErrIdentityReuse) {
		t.Fatalf("operation identity reuse error = %v", err)
	}
	reusedRetry := testIntent(t, "op-two", "retry-one", KindMkdir, "docs/two", "")
	if _, err := store.Append(reusedRetry, StatusOfflineOnly); !errors.Is(err, ErrIdentityReuse) {
		t.Fatalf("retry identity reuse error = %v", err)
	}
	if _, err := store.Complete(intent.OperationID, testJournalCID(t, []byte("candidate")).String()); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("dirty operation completed without upload: %v", err)
	}
	if _, err := store.MarkConflicted(intent.OperationID, ""); err == nil {
		t.Fatal("empty conflict identity was accepted")
	}
}

func TestJournalCanonicalOperationValidation(t *testing.T) {
	base := testIntent(t, "op-one", "retry-one", KindWrite, "docs/one.txt", "")
	tests := []Intent{base, base, base, base, base}
	tests[0].Path = "../escape"
	tests[1].Path = "/absolute"
	tests[2].PayloadCID = "not-a-cid"
	tests[3].BaseRoot = "not-a-cid"
	tests[4].Destination = "unexpected"
	invalidOperationID := base
	invalidOperationID.OperationID = "op-\xff"
	tests = append(tests, invalidOperationID)
	invalidPath := base
	invalidPath.Path = "docs/\xff"
	tests = append(tests, invalidPath)
	for i, intent := range tests {
		store, err := Open(filepath.Join(t.TempDir(), "operations.json"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Append(intent, StatusLocalDirty); err == nil {
			t.Fatalf("invalid intent %d was accepted", i)
		}
	}
	rename := testIntent(t, "op-rename", "retry-rename", KindRename, "docs/old", "docs/new")
	store, err := Open(filepath.Join(t.TempDir(), "operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Append(rename, StatusLocalDirty); err != nil {
		t.Fatalf("canonical rename rejected: %v", err)
	}
}

func TestJournalRejectsInvalidUTF8ConflictIdentity(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "operations.json"))
	if err != nil {
		t.Fatal(err)
	}
	intent := testIntent(t, "op-one", "retry-one", KindWrite, "docs/one.txt", "")
	if _, err := store.Append(intent, StatusLocalDirty); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MarkConflicted(intent.OperationID, "conflict-\xff"); err == nil {
		t.Fatal("journal accepted an invalid UTF-8 conflict identity")
	}
}

func TestJournalRejectsLossyUnicodeInPersistedJSON(t *testing.T) {
	tests := []struct {
		name        string
		replacement []byte
		wantError   string
	}{
		{name: "invalid UTF-8", replacement: []byte{0xff}, wantError: "not valid UTF-8"},
		{name: "lone surrogate", replacement: []byte(`\ud800`), wantError: "surrogate"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			journalPath := filepath.Join(t.TempDir(), "operations.json")
			store, err := Open(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			intent := testIntent(t, "op-one", "retry-one", KindWrite, "docs/one.txt", "")
			if _, err := store.Append(intent, StatusLocalDirty); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(journalPath)
			if err != nil {
				t.Fatal(err)
			}
			data = bytes.Replace(data, []byte(`"branch": "main"`), append([]byte(`"branch": "`), append(test.replacement, '"')...), 1)
			if err := os.WriteFile(journalPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(journalPath); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Open error = %v, want strict Unicode rejection containing %q", err, test.wantError)
			}
		})
	}
}

func TestJournalRejectsCorruptPersistedReplayIdentity(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "operations.json")
	intent := testIntent(t, "op-one", "retry-one", KindMkdir, "docs", "")
	now := testTime()
	state := persistedState{
		Version: journalVersion, NextSequence: 2,
		Operations: map[string]Operation{
			"wrong-key": {Intent: intent, Sequence: 1, Status: StatusOfflineOnly, CreatedAt: now, UpdatedAt: now},
		},
		UsedOperationIDs: map[string]bool{intent.OperationID: true},
		UsedRetryIDs:     map[string]bool{intent.RetryID: true},
	}
	data, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journalPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(journalPath); err == nil {
		t.Fatal("journal accepted mismatched persisted operation identity")
	}
}

func TestJournalRejectsImpossibleSupersededGraphs(t *testing.T) {
	original := testIntent(t, "op-original", "retry-original", KindWrite, "docs/one.txt", "")
	second := testIntent(t, "op-second", "retry-second", KindMkdir, "docs/two", "")
	replacement := testIntent(t, "op-replacement", "retry-replacement", KindWrite, "docs/one.txt", "")
	now := testTime()
	valid := persistedState{
		Version: journalVersion, NextSequence: 4,
		Operations: map[string]Operation{
			original.OperationID: {
				Intent: original, Sequence: 1, Status: StatusSuperseded, ConflictID: "conflict-one",
				ReplacementOperationID: replacement.OperationID, CreatedAt: now, UpdatedAt: now,
			},
			second.OperationID: {
				Intent: second, Sequence: 2, Status: StatusLocalDirty, CreatedAt: now, UpdatedAt: now,
			},
			replacement.OperationID: {
				Intent: replacement, Sequence: 3, Status: StatusLocalDirty, CreatedAt: now, UpdatedAt: now,
			},
		},
		UsedOperationIDs: map[string]bool{original.OperationID: true, second.OperationID: true, replacement.OperationID: true},
		UsedRetryIDs:     map[string]bool{original.RetryID: true, second.RetryID: true, replacement.RetryID: true},
	}
	tests := []struct {
		name   string
		mutate func(*persistedState)
	}{
		{name: "missing conflict identity", mutate: func(state *persistedState) {
			operation := state.Operations[original.OperationID]
			operation.ConflictID = ""
			state.Operations[original.OperationID] = operation
		}},
		{name: "shared replacement", mutate: func(state *persistedState) {
			operation := state.Operations[second.OperationID]
			operation.Status = StatusSuperseded
			operation.ConflictID = "conflict-two"
			operation.ReplacementOperationID = replacement.OperationID
			state.Operations[second.OperationID] = operation
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := clonePersistedState(valid)
			test.mutate(&state)
			journalPath := filepath.Join(t.TempDir(), "operations.json")
			data, err := json.Marshal(state)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(journalPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(journalPath); err == nil {
				t.Fatal("journal accepted an impossible superseded graph")
			}
		})
	}
}

func TestConcurrentJournalWritersPreserveEverySequence(t *testing.T) {
	journalPath := filepath.Join(t.TempDir(), "operations.json")
	first, err := Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	stores := []*Store{first, second}
	var wait sync.WaitGroup
	errorsCh := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			intent := testIntent(t, fmt.Sprintf("op-%02d", i), fmt.Sprintf("retry-%02d", i), KindMkdir, fmt.Sprintf("docs/%02d", i), "")
			_, err := stores[i%len(stores)].Append(intent, StatusOfflineOnly)
			errorsCh <- err
		}(i)
	}
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := Open(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := reopened.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 20 {
		t.Fatalf("pending operations = %d, want 20", len(pending))
	}
	for i, operation := range pending {
		if operation.Sequence != uint64(i+1) {
			t.Fatalf("operation %d sequence = %d", i, operation.Sequence)
		}
	}
}

func testIntent(t *testing.T, operationID, retryID string, kind Kind, operationPath, destination string) Intent {
	t.Helper()
	intent := Intent{
		OperationID: operationID, RetryID: retryID, DatasetID: "bucket-one", Branch: "main",
		BaseRoot: testJournalCID(t, []byte("base root")).String(), BaseRevision: 7,
		Kind: kind, Path: operationPath, Destination: destination, EncryptionEpoch: 3,
	}
	if kind == KindWrite {
		intent.PayloadCID = testJournalCID(t, []byte("payload for "+operationID)).String()
	}
	return intent
}

func testJournalCID(t *testing.T, body []byte) cid.Cid {
	t.Helper()
	prefix := cid.Prefix{Version: 1, Codec: cid.Raw, MhType: multihash.SHA2_256, MhLength: -1}
	value, err := prefix.Sum(body)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func testTime() time.Time { return time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC) }

func assertJournalOwnerOnly(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("journal mode = %o, want owner-only", info.Mode().Perm())
	}
}

func clonePersistedState(value persistedState) persistedState {
	cloned := value
	cloned.Operations = make(map[string]Operation, len(value.Operations))
	for key, operation := range value.Operations {
		cloned.Operations[key] = operation
	}
	cloned.UsedOperationIDs = make(map[string]bool, len(value.UsedOperationIDs))
	for key, used := range value.UsedOperationIDs {
		cloned.UsedOperationIDs[key] = used
	}
	cloned.UsedRetryIDs = make(map[string]bool, len(value.UsedRetryIDs))
	for key, used := range value.UsedRetryIDs {
		cloned.UsedRetryIDs[key] = used
	}
	return cloned
}

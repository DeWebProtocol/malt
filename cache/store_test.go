package cache

import (
	"bytes"
	"context"
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
	"github.com/multiformats/go-multihash"
)

func TestVerifiedCacheHitRevalidatesExactBindingCIDAndProof(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("verified remote payload")
	binding := testBinding(t, body)
	evidence := VerificationEvidence{
		Profile: "malt.unixfs.path-proof/v1", Evidence: []byte("proof-list-and-request"), VerifiedAt: time.Now().UTC(),
	}
	if _, err := store.PutVerified(binding, body, evidence); err != nil {
		t.Fatal(err)
	}

	verified := 0
	verifier := ProofVerifierFunc(func(_ context.Context, got Binding, gotEvidence VerificationEvidence) error {
		verified++
		if bindingID(got) != bindingID(binding) || gotEvidence.Profile != evidence.Profile || string(gotEvidence.Evidence) != string(evidence.Evidence) {
			return fmt.Errorf("verification input was not exact")
		}
		return nil
	})
	got, entry, err := store.ReadVerified(t.Context(), binding, verifier)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) || entry.State != StateVerifiedClean || verified != 1 {
		t.Fatalf("verified cache read = %q, %#v, calls=%d", got, entry, verified)
	}
	if _, _, err := store.ReadVerified(t.Context(), binding, nil); err == nil {
		t.Fatal("cache returned bytes without a local proof verifier")
	}

	wrongBindings := []Binding{binding, binding, binding}
	wrongBindings[0].Root = testCID(t, []byte("different root"))
	wrongBindings[1].Revision++
	wrongBindings[2].EncryptionEpoch++
	for _, wrong := range wrongBindings {
		if got, _, err := store.ReadVerified(t.Context(), wrong, verifier); !errors.Is(err, ErrBindingMismatch) || got != nil {
			t.Fatalf("wrong binding read = %q, %v", got, err)
		}
	}
	wrongDataset := binding
	wrongDataset.DatasetID = "other-dataset"
	if got, _, err := store.ReadVerified(t.Context(), wrongDataset, verifier); !errors.Is(err, ErrMiss) || got != nil {
		t.Fatalf("wrong dataset read = %q, %v", got, err)
	}
}

func TestInvalidCachedProofMarksEntryStaleWithoutExposingBytes(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("proof-bound payload")
	binding := testBinding(t, body)
	if _, err := store.PutVerified(binding, body, testEvidence()); err != nil {
		t.Fatal(err)
	}
	rejected := errors.New("invalid ProofList")
	got, _, err := store.ReadVerified(t.Context(), binding, ProofVerifierFunc(func(context.Context, Binding, VerificationEvidence) error {
		return rejected
	}))
	if got != nil || !errors.Is(err, rejected) {
		t.Fatalf("invalid proof read = %q, %v", got, err)
	}
	entry, err := store.Inspect(binding)
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != StateStale || entry.Verification != nil {
		t.Fatalf("invalid proof entry = %#v", entry)
	}
}

func TestCorruptOrMissingCacheBodyIsRejectedAndMarkedStale(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(string) error
	}{
		{name: "corrupt", mutate: func(path string) error { return os.WriteFile(path, []byte("corrupt"), 0o600) }},
		{name: "missing", mutate: func(path string) error { return os.Remove(path) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			body := []byte("cached body")
			binding := testBinding(t, body)
			if _, err := store.PutVerified(binding, body, testEvidence()); err != nil {
				t.Fatal(err)
			}
			if err := test.mutate(store.bodyPath(bindingID(binding))); err != nil {
				t.Fatal(err)
			}
			got, _, err := store.ReadVerified(t.Context(), binding, acceptingVerifier())
			if got != nil || !errors.Is(err, ErrCorrupt) {
				t.Fatalf("corrupt read = %q, %v", got, err)
			}
			entry, err := store.Inspect(binding)
			if err != nil {
				t.Fatal(err)
			}
			if entry.State != StateStale || entry.Verification != nil {
				t.Fatalf("corrupt entry state = %#v", entry)
			}
		})
	}
}

func TestCacheStateMachineSeparatesRemoteVerifiedDirtyPendingOfflineAndConflict(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("local candidate payload")
	binding := testBinding(t, body)
	binding.Revision = 0
	entry, err := store.PutLocal(binding, body, StateLocalDirty)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Verification != nil || entry.State != StateLocalDirty {
		t.Fatalf("dirty entry = %#v", entry)
	}
	if got, _, err := store.ReadLocal(binding); err != nil || string(got) != string(body) {
		t.Fatalf("local body = %q, %v", got, err)
	}
	for _, next := range []State{StateOfflineOnly, StatePendingUpload, StateCandidate, StateConflicted, StateLocalDirty} {
		entry, err = store.Transition(binding, next)
		if err != nil || entry.State != next {
			t.Fatalf("transition to %s = %#v, %v", next, entry, err)
		}
	}
	if _, err := store.Transition(binding, StateVerifiedClean); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("dirty entry became verified without proof: %v", err)
	}
	entry, err = store.ReconcileLocalState(binding, StateCandidate)
	if err != nil || entry.State != StateCandidate || entry.Verification != nil {
		t.Fatalf("reconciled candidate entry=%#v err=%v", entry, err)
	}
	if _, err := store.ReconcileLocalState(binding, StateVerifiedClean); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("local reconciliation created verified state: %v", err)
	}

	remoteBinding := testBinding(t, []byte("not fetched"))
	remote, err := store.RecordRemote(remoteBinding)
	if err != nil || remote.State != StateUnmaterializedRemote || remote.BodyPresent {
		t.Fatalf("unmaterialized entry = %#v, %v", remote, err)
	}
	if got, _, err := store.ReadVerified(t.Context(), remoteBinding, acceptingVerifier()); got != nil || !errors.Is(err, ErrNotVerified) {
		t.Fatalf("unmaterialized read = %q, %v", got, err)
	}
}

func TestPutCannotBypassPendingConflictOrVerifiedState(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("immutable local payload")
	binding := testBinding(t, body)
	if _, err := store.PutLocal(binding, body, StatePendingUpload); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("PutLocal created pending state without a transition: %v", err)
	}
	if _, err := store.PutLocal(binding, body, StateConflicted); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("PutLocal created conflict state without a transition: %v", err)
	}
	if _, err := store.PutLocal(binding, body, StateLocalDirty); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Transition(binding, StatePendingUpload); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutLocal(binding, body, StateOfflineOnly); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("PutLocal demoted pending upload: %v", err)
	}
	if _, err := store.PutVerified(binding, body, testEvidence()); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("PutVerified erased pending upload: %v", err)
	}
	entry, err := store.Inspect(binding)
	if err != nil || entry.State != StatePendingUpload {
		t.Fatalf("pending state after rejected Put = %#v, %v", entry, err)
	}
	if _, err := store.Transition(binding, StateConflicted); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutLocal(binding, body, StateLocalDirty); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("PutLocal erased conflict: %v", err)
	}

	verifiedBody := []byte("verified payload")
	verifiedBinding := testBinding(t, verifiedBody)
	if _, err := store.PutVerified(verifiedBinding, verifiedBody, testEvidence()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutLocal(verifiedBinding, verifiedBody, StateLocalDirty); !errors.Is(err, ErrInvalidState) {
		t.Fatalf("PutLocal overwrote verified state: %v", err)
	}
}

func TestRecordRemoteDoesNotDowngradeVerifiedEntry(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("already materialized")
	binding := testBinding(t, body)
	if _, err := store.PutVerified(binding, body, testEvidence()); err != nil {
		t.Fatal(err)
	}
	entry, err := store.RecordRemote(binding)
	if err != nil {
		t.Fatal(err)
	}
	if entry.State != StateVerifiedClean || entry.Verification == nil {
		t.Fatalf("RecordRemote downgraded verified entry: %#v", entry)
	}
}

func TestConcurrentCacheFillAcrossStoresIsSerializedAndRestartable(t *testing.T) {
	directory := t.TempDir()
	first, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("concurrently fetched payload")
	binding := testBinding(t, body)
	stores := []*Store{first, second}
	errCh := make(chan error, 20)
	var wait sync.WaitGroup
	for i := 0; i < 20; i++ {
		wait.Add(1)
		go func(i int) {
			defer wait.Done()
			_, err := stores[i%len(stores)].PutVerified(binding, body, testEvidence())
			errCh <- err
		}(i)
	}
	wait.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := reopened.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].State != StateVerifiedClean {
		t.Fatalf("restarted cache entries = %#v", entries)
	}
	if got, _, err := reopened.ReadVerified(t.Context(), binding, acceptingVerifier()); err != nil || string(got) != string(body) {
		t.Fatalf("restarted cache read = %q, %v", got, err)
	}
	assertOwnerOnly(t, reopened.metaPath)
	assertOwnerOnly(t, reopened.bodyPath(bindingID(binding)))
}

func TestRemoveHoldsCrossProcessLockUntilBlobDeletion(t *testing.T) {
	directory := t.TempDir()
	remover, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	replacer, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("replacement payload")
	binding := testBinding(t, body)
	if _, err := remover.PutVerified(binding, body, testEvidence()); err != nil {
		t.Fatal(err)
	}
	enteredRemove := make(chan struct{})
	continueRemove := make(chan struct{})
	remover.removeFile = func(path string) error {
		close(enteredRemove)
		<-continueRemove
		return os.Remove(path)
	}
	removeErr := make(chan error, 1)
	go func() { removeErr <- remover.Remove(binding) }()
	<-enteredRemove
	replaceDone := make(chan error, 1)
	replaceStarted := make(chan struct{})
	go func() {
		close(replaceStarted)
		_, err := replacer.PutVerified(binding, body, testEvidence())
		replaceDone <- err
	}()
	<-replaceStarted
	select {
	case err := <-replaceDone:
		t.Fatalf("replacement escaped removal lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(continueRemove)
	if err := <-removeErr; err != nil {
		t.Fatal(err)
	}
	if err := <-replaceDone; err != nil {
		t.Fatal(err)
	}
	if got, _, err := replacer.ReadVerified(t.Context(), binding, acceptingVerifier()); err != nil || string(got) != string(body) {
		t.Fatalf("replacement after removal = %q, %v", got, err)
	}
}

func TestRemoveFailureKeepsMetadataReachableForRetry(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("sensitive cached payload")
	binding := testBinding(t, body)
	if _, err := store.PutVerified(binding, body, testEvidence()); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected remove failure")
	store.removeFile = func(string) error { return injected }
	if err := store.Remove(binding); !errors.Is(err, injected) {
		t.Fatalf("Remove error = %v, want injected failure", err)
	}
	store.removeFile = os.Remove
	if entry, err := store.Inspect(binding); err != nil || entry.State != StateVerifiedClean {
		t.Fatalf("failed removal lost reachable metadata: %#v, %v", entry, err)
	}
	if err := store.Remove(binding); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Inspect(binding); !errors.Is(err, ErrMiss) {
		t.Fatalf("retried removal left metadata: %v", err)
	}
}

func TestRemoveMetadataFailureLeavesReachableRecordForRetry(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("sensitive cached payload")
	binding := testBinding(t, body)
	if _, err := store.PutVerified(binding, body, testEvidence()); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected metadata failure")
	store.metadataWriteHook = func() error { return injected }
	if err := store.Remove(binding); !errors.Is(err, injected) {
		t.Fatalf("Remove error = %v, want injected metadata failure", err)
	}
	store.metadataWriteHook = nil

	restarted, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if entry, err := restarted.Inspect(binding); err != nil || entry.State != StateVerifiedClean {
		t.Fatalf("failed metadata commit lost the reachable record: %#v, %v", entry, err)
	}
	if err := restarted.Remove(binding); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Inspect(binding); !errors.Is(err, ErrMiss) {
		t.Fatalf("retried removal left metadata: %v", err)
	}
}

func TestFailedMetadataCommitCleansNewBodyAndRestartReconcilesCrashOrphans(t *testing.T) {
	directory := t.TempDir()
	store, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("uncommitted payload")
	binding := testBinding(t, body)
	injected := errors.New("injected metadata failure")
	store.metadataWriteHook = func() error { return injected }
	if _, err := store.PutVerified(binding, body, testEvidence()); !errors.Is(err, injected) {
		t.Fatalf("PutVerified error = %v, want injected metadata failure", err)
	}
	store.metadataWriteHook = nil
	if _, err := os.Stat(store.bodyPath(bindingID(binding))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed Put left an orphaned body: %v", err)
	}
	if _, err := store.Inspect(binding); !errors.Is(err, ErrMiss) {
		t.Fatalf("failed Put committed metadata: %v", err)
	}

	orphanPath := store.bodyPath(bindingID(binding))
	if err := os.WriteFile(orphanPath, body, 0o600); err != nil {
		t.Fatal(err)
	}
	temporaryPath := filepath.Join(directory, "blobs", ".cache-body-crash")
	if err := os.WriteFile(temporaryPath, []byte("partial sensitive body"), 0o600); err != nil {
		t.Fatal(err)
	}
	metadataTemp := filepath.Join(directory, ".cache-metadata-crash")
	if err := os.WriteFile(metadataTemp, []byte("partial metadata"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(directory); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{orphanPath, temporaryPath, metadataTemp} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("restart did not reconcile orphan %s: %v", filepath.Base(path), err)
		}
	}
}

func TestCacheRejectsInvalidUTF8BindingAndEvidenceBeforePersistence(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	body := []byte("payload")
	binding := testBinding(t, body)
	binding.DatasetID = "bucket-\xff"
	if _, err := store.PutVerified(binding, body, testEvidence()); err == nil {
		t.Fatal("cache accepted an invalid UTF-8 dataset identity")
	}
	binding = testBinding(t, body)
	evidence := testEvidence()
	evidence.Profile = "profile-\xff"
	if _, err := store.PutVerified(binding, body, evidence); err == nil {
		t.Fatal("cache accepted an invalid UTF-8 evidence profile")
	}
}

func TestCacheRejectsLossyUnicodeInPersistedJSON(t *testing.T) {
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
			directory := t.TempDir()
			store, err := Open(directory)
			if err != nil {
				t.Fatal(err)
			}
			body := []byte("payload")
			binding := testBinding(t, body)
			if _, err := store.PutVerified(binding, body, testEvidence()); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(store.metaPath)
			if err != nil {
				t.Fatal(err)
			}
			data = bytes.Replace(data, []byte(testEvidence().Profile), test.replacement, 1)
			if err := os.WriteFile(store.metaPath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(directory); err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("Open error = %v, want strict Unicode rejection containing %q", err, test.wantError)
			}
		})
	}
}

func TestReadLocalNeverReturnsCorruptBytes(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(string, []byte) error
	}{
		{name: "missing", mutate: func(path string, _ []byte) error { return os.Remove(path) }},
		{name: "short", mutate: func(path string, _ []byte) error { return os.WriteFile(path, []byte("x"), 0o600) }},
		{name: "same-length corrupt", mutate: func(path string, body []byte) error {
			corrupt := bytes.Repeat([]byte{'x'}, len(body))
			return os.WriteFile(path, corrupt, 0o600)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			body := []byte("local candidate payload")
			binding := testBinding(t, body)
			binding.Revision = 0
			if _, err := store.PutLocal(binding, body, StateLocalDirty); err != nil {
				t.Fatal(err)
			}
			if err := test.mutate(store.bodyPath(bindingID(binding)), body); err != nil {
				t.Fatal(err)
			}
			got, _, err := store.ReadLocal(binding)
			if got != nil || err == nil {
				t.Fatalf("ReadLocal returned corrupt bytes %q with error %v", got, err)
			}
		})
	}
}

func TestOpenRejectsImpossiblePersistedCacheStateCombinations(t *testing.T) {
	body := []byte("persisted payload")
	binding := testBinding(t, body)
	now := time.Now().UTC()
	tests := []Entry{
		{Binding: binding, State: StateUnmaterializedRemote, BodyPresent: true, Size: int64(len(body)), UpdatedAt: now},
		{Binding: binding, State: StateLocalDirty, BodyPresent: false, UpdatedAt: now},
		{Binding: binding, State: StatePendingUpload, BodyPresent: false, Size: int64(len(body)), UpdatedAt: now},
		{Binding: binding, State: StateCandidate, BodyPresent: false, Size: int64(len(body)), UpdatedAt: now},
		{Binding: binding, State: StateCandidate, BodyPresent: true, Size: int64(len(body)), Verification: ptrEvidence(testEvidence()), UpdatedAt: now},
		{Binding: binding, State: StateOfflineOnly, BodyPresent: true, Size: int64(len(body)), Verification: ptrEvidence(testEvidence()), UpdatedAt: now},
	}
	for i, entry := range tests {
		directory := t.TempDir()
		state := metadata{Version: metadataVersion, Entries: map[string]Entry{bindingID(binding): entry}}
		data, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, "metadata.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(directory); err == nil {
			t.Fatalf("impossible persisted cache entry %d was accepted: %#v", i, entry)
		}
	}
}

func testBinding(t *testing.T, body []byte) Binding {
	t.Helper()
	return Binding{
		DatasetID: "bucket-one", Branch: "main", Root: testCID(t, []byte("accepted root")),
		Revision: 3, CID: testCID(t, body), EncryptionEpoch: 7,
	}
}

func testCID(t *testing.T, body []byte) cid.Cid {
	t.Helper()
	prefix := cid.Prefix{Version: 1, Codec: cid.Raw, MhType: multihash.SHA2_256, MhLength: -1}
	value, err := prefix.Sum(body)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func testEvidence() VerificationEvidence {
	return VerificationEvidence{Profile: "malt.unixfs.path-proof/v1", Evidence: []byte("proof"), VerifiedAt: time.Now().UTC()}
}

func ptrEvidence(value VerificationEvidence) *VerificationEvidence { return &value }

func acceptingVerifier() ProofVerifier {
	return ProofVerifierFunc(func(context.Context, Binding, VerificationEvidence) error { return nil })
}

func assertOwnerOnly(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("%s mode = %o, want owner-only", filepath.Base(path), info.Mode().Perm())
	}
}

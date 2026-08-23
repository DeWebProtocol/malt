package trust

import (
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
)

func TestWithAcceptedRootFencesConcurrentPromotionAcrossStoreInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roots.json")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	base := testRoot
	next := candidateRoot
	if _, err := first.Trust("docs", base, "unixfs", "gateway", "test"); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	guardedDone := make(chan error, 1)
	go func() {
		guardedDone <- first.WithAcceptedRoot("docs", base, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	promotionDone := make(chan error, 1)
	promotionStarted := make(chan struct{})
	go func() {
		close(promotionStarted)
		_, err := second.Trust("docs", next, "unixfs", "gateway", "concurrent")
		promotionDone <- err
	}()
	<-promotionStarted
	select {
	case err := <-promotionDone:
		t.Fatalf("promotion crossed accepted-root fence: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-guardedDone; err != nil {
		t.Fatal(err)
	}
	if err := <-promotionDone; err != nil {
		t.Fatal(err)
	}
	if err := first.WithAcceptedRoot("docs", base, func() error { return nil }); !errors.Is(err, ErrAcceptedRootChanged) {
		t.Fatalf("stale accepted-root fence error=%v", err)
	}
}

const testRoot = "bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"
const candidateRoot = "bafkreib6u4dvknbd5g7pp7z2ex2jvdkbo3hytm5v6hlx3q3iibgfk5j5wi"
const secondCandidateRoot = "bafkqaaa"

func TestRecordAndCandidateLegacyUnkeyedLiteralsRemainSourceCompatible(t *testing.T) {
	_ = Candidate{"", "", "", time.Time{}}
	_ = Record{"", "", "", "", "", "", time.Time{}, nil}
}

func TestCandidateRequiresExplicitAcceptance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roots.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Trust("docs", testRoot, "unixfs", "http://gateway", "manual"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddCandidate("docs", candidateRoot, testRoot, "upload"); err != nil {
		t.Fatal(err)
	}
	record, err := store.Get("docs")
	if err != nil {
		t.Fatal(err)
	}
	if record.AcceptedRoot != testRoot || len(record.Candidates) != 1 {
		t.Fatalf("candidate changed accepted root: %#v", record)
	}
	if _, err := store.AcceptCandidate("docs", candidateRoot, "manual"); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	record, err = reopened.Get("docs")
	if err != nil {
		t.Fatal(err)
	}
	if record.AcceptedRoot != candidateRoot || record.PreviousRoot != testRoot {
		t.Fatalf("accepted record = %#v", record)
	}
	if _, err := reopened.AcceptCandidate("docs", testRoot, "rollback"); !errors.Is(err, ErrCandidateNotFound) {
		t.Fatalf("unexpected rollback acceptance error: %v", err)
	}
}

func TestBootstrapCandidateRequiresExplicitCandidateAcceptance(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "roots.json"))
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.AddCandidate("fresh", candidateRoot, "", "local-genesis")
	if err != nil {
		t.Fatal(err)
	}
	if record.AcceptedRoot != "" || len(record.Candidates) != 1 || record.Candidates[0].BaseRoot != "" {
		t.Fatalf("bootstrap candidate record = %#v", record)
	}
	if _, _, err := AcceptedRoot(store, "fresh"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("bootstrap candidate became accepted: %v", err)
	}
	record, err = store.AcceptCandidate("fresh", candidateRoot, "explicit-bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	if record.AcceptedRoot != candidateRoot || len(record.Candidates) != 0 {
		t.Fatalf("accepted bootstrap candidate = %#v", record)
	}
}

func TestCIDRepresentationsAreCanonicalizedAcrossTrustWorkflow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roots.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	alternateRoot := alternateCIDString(t, testRoot)
	alternateCandidate := alternateCIDString(t, candidateRoot)

	record, err := store.Trust("docs", alternateRoot, "unixfs", "", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if record.AcceptedRoot != testRoot {
		t.Fatalf("trusted root = %q, want canonical %q", record.AcceptedRoot, testRoot)
	}
	record, err = store.AddCandidate("docs", alternateCandidate, alternateRoot, "upload")
	if err != nil {
		t.Fatal(err)
	}
	if len(record.Candidates) != 1 || record.Candidates[0].Root != candidateRoot || record.Candidates[0].BaseRoot != testRoot {
		t.Fatalf("canonical candidate record = %#v", record)
	}
	record, err = store.AcceptCandidate("docs", alternateCandidate, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if record.AcceptedRoot != candidateRoot || record.PreviousRoot != testRoot {
		t.Fatalf("accepted canonical record = %#v", record)
	}
}

func TestTrustEquivalentRootPreservesDistinctPreviousRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roots.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Trust("docs", testRoot, "unixfs", "first.example", "initial"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Trust("docs", candidateRoot, "unixfs", "second.example", "advance"); err != nil {
		t.Fatal(err)
	}
	record, err := store.Trust("docs", alternateCIDString(t, candidateRoot), "updated", "third.example", "refresh")
	if err != nil {
		t.Fatal(err)
	}
	if record.AcceptedRoot != candidateRoot || record.PreviousRoot != testRoot {
		t.Fatalf("equivalent re-trust changed root history: %#v", record)
	}
	if record.Profile != "updated" || record.Gateway != "third.example" || record.Source != "refresh" {
		t.Fatalf("equivalent re-trust did not refresh metadata: %#v", record)
	}
}

func TestOpenCanonicalizesPersistedCIDRepresentationsAndDuplicates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roots.json")
	persisted := legacyState{Version: 1, Roots: map[string]Record{
		"docs": {
			Alias:        "docs",
			AcceptedRoot: alternateCIDString(t, testRoot),
			PreviousRoot: alternateCIDString(t, secondCandidateRoot),
			Candidates: []Candidate{
				{Root: alternateCIDString(t, candidateRoot), BaseRoot: alternateCIDString(t, testRoot), Source: "first"},
				{Root: candidateRoot, BaseRoot: testRoot, Source: "last"},
			},
		},
	}}
	writeTestState(t, path, persisted)

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Get("docs")
	if err != nil {
		t.Fatal(err)
	}
	if record.AcceptedRoot != testRoot || record.PreviousRoot != secondCandidateRoot {
		t.Fatalf("canonical persisted record = %#v", record)
	}
	if len(record.Candidates) != 1 || record.Candidates[0].Root != candidateRoot || record.Candidates[0].BaseRoot != testRoot || record.Candidates[0].Source != "last" {
		t.Fatalf("canonical persisted candidates = %#v", record.Candidates)
	}
	if _, err := store.AcceptCandidate("docs", alternateCIDString(t, candidateRoot), "manual"); err != nil {
		t.Fatalf("accept canonicalized persisted candidate: %v", err)
	}
}

func TestOpenDropsPersistedCandidateEquivalentToAcceptedRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roots.json")
	persisted := legacyState{Version: 1, Roots: map[string]Record{
		"docs": {
			Alias:        "docs",
			AcceptedRoot: alternateCIDString(t, testRoot),
			PreviousRoot: alternateCIDString(t, secondCandidateRoot),
			Candidates: []Candidate{
				{Root: testRoot, BaseRoot: alternateCIDString(t, testRoot), Source: "legacy-self"},
				{Root: alternateCIDString(t, candidateRoot), BaseRoot: testRoot, Source: "real-candidate"},
			},
		},
	}}
	writeTestState(t, path, persisted)

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Get("docs")
	if err != nil {
		t.Fatal(err)
	}
	if record.AcceptedRoot != testRoot || record.PreviousRoot != secondCandidateRoot {
		t.Fatalf("canonical persisted record = %#v", record)
	}
	if len(record.Candidates) != 1 || record.Candidates[0].Root != candidateRoot || record.Candidates[0].Source != "real-candidate" {
		t.Fatalf("persisted candidates after self-candidate migration = %#v", record.Candidates)
	}
	if _, err := store.AcceptCandidate("docs", alternateCIDString(t, testRoot), "manual"); !errors.Is(err, ErrCandidateNotFound) {
		t.Fatalf("accept equivalent-to-current candidate error = %v, want ErrCandidateNotFound", err)
	}
	after, err := store.Get("docs")
	if err != nil {
		t.Fatal(err)
	}
	if after.AcceptedRoot != testRoot || after.PreviousRoot != secondCandidateRoot {
		t.Fatalf("rejected self-acceptance changed roots: %#v", after)
	}
}

func TestOpenDropsPreviousRootEquivalentToAcceptedRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roots.json")
	persisted := legacyState{Version: 1, Roots: map[string]Record{
		"docs": {
			Alias:        "docs",
			AcceptedRoot: alternateCIDString(t, testRoot),
			PreviousRoot: testRoot,
		},
	}}
	writeTestState(t, path, persisted)

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Get("docs")
	if err != nil {
		t.Fatal(err)
	}
	if record.AcceptedRoot != testRoot || record.PreviousRoot != "" {
		t.Fatalf("canonical reload retained a self previous root: %#v", record)
	}
}

func TestOpenRejectsMalformedPersistedCID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roots.json")
	writeTestState(t, path, legacyState{Version: 1, Roots: map[string]Record{
		"docs": {Alias: "docs", AcceptedRoot: "not-a-cid"},
	}})
	if _, err := Open(path); err == nil {
		t.Fatal("Open accepted malformed persisted CID")
	}
}

func TestOpenRejectsLegacyAliasWithoutAcceptedRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roots.json")
	writeTestState(t, path, legacyState{Version: 1, Roots: map[string]Record{
		"docs": {Alias: "docs"},
	}})
	if _, err := Open(path); err == nil {
		t.Fatal("Open migrated a v1 alias without the formerly required accepted root")
	}
}

func TestOpenRejectsV2ObservationWithoutAuditTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roots.json")
	writeTestState(t, path, state{Version: trustStoreVersion, Roots: map[string]RootState{
		"docs": {Alias: "docs", ObservedHeads: []ObservedHead{{
			Source: "gateway", DatasetID: "bucket-one", Branch: "main",
			CommitID: "commit-one", Root: testRoot, Revision: 1,
		}}},
	}})
	if _, err := Open(path); err == nil {
		t.Fatal("Open accepted a v2 observation without audit time")
	}
}

func TestAddCandidateRejectsStaleBaseAfterAcceptedRootAdvances(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "roots.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Trust("docs", testRoot, "unixfs", "", "manual"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Trust("docs", candidateRoot, "unixfs", "", "concurrent-update"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddCandidate("docs", secondCandidateRoot, testRoot, "stale-operation"); !errors.Is(err, ErrStaleCandidate) {
		t.Fatalf("AddCandidate stale error = %v, want ErrStaleCandidate", err)
	}
	record, err := store.Get("docs")
	if err != nil {
		t.Fatal(err)
	}
	if record.AcceptedRoot != candidateRoot || len(record.Candidates) != 0 {
		t.Fatalf("stale add changed record: %#v", record)
	}
}

func TestAcceptCandidateRejectsStaleSibling(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "roots.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Trust("docs", testRoot, "unixfs", "", "manual"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddCandidate("docs", candidateRoot, testRoot, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddCandidate("docs", secondCandidateRoot, testRoot, "second"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptCandidate("docs", secondCandidateRoot, "manual"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AcceptCandidate("docs", candidateRoot, "manual"); !errors.Is(err, ErrStaleCandidate) {
		t.Fatalf("AcceptCandidate stale error = %v, want ErrStaleCandidate", err)
	}
	record, err := store.Get("docs")
	if err != nil {
		t.Fatal(err)
	}
	if record.AcceptedRoot != secondCandidateRoot || record.PreviousRoot != testRoot {
		t.Fatalf("stale acceptance changed record: %#v", record)
	}
}

func TestIndependentStoresReloadBeforeMutating(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roots.json")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Trust("first", testRoot, "unixfs", "", "first-process"); err != nil {
		t.Fatal(err)
	}
	if _, err := second.Trust("second", candidateRoot, "unixfs", "", "second-process"); err != nil {
		t.Fatal(err)
	}
	roots, err := first.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 || roots[0].Alias != "first" || roots[1].Alias != "second" {
		t.Fatalf("roots after interleaved writers = %#v", roots)
	}
}

func TestIndependentStoresSerializeConcurrentMutations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roots.json")
	stores := make([]*Store, 2)
	for i := range stores {
		store, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		stores[i] = store
	}

	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := stores[i%len(stores)].Trust(fmt.Sprintf("root-%02d", i), testRoot, "unixfs", "", "concurrent-test")
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	roots, err := stores[0].List()
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 20 {
		t.Fatalf("roots after concurrent writers = %d, want 20", len(roots))
	}
}

func TestLegacyV1StoreMigratesToStructuredV2WithoutChangingTrust(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roots.json")
	writeTestState(t, path, legacyState{Version: 1, Roots: map[string]Record{
		"docs": {
			Alias: "docs", Profile: "unixfs", Gateway: "https://gateway.example",
			AcceptedRoot: testRoot, Source: "manual",
			Candidates: []Candidate{{Root: candidateRoot, BaseRoot: testRoot, Source: "local-write"}},
		},
	}})
	legacyBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Get("docs")
	if err != nil {
		t.Fatal(err)
	}
	if record.AcceptedRoot != testRoot || len(record.Candidates) != 1 || record.Candidates[0].Root != candidateRoot {
		t.Fatalf("migrated compatibility record = %#v", record)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var persisted state
	if err := json.Unmarshal(data, &persisted); err != nil {
		t.Fatal(err)
	}
	value := persisted.Roots["docs"]
	if persisted.Version != trustStoreVersion || value.Accepted == nil || value.Accepted.Root != testRoot {
		t.Fatalf("migrated persisted state = %#v", persisted)
	}
	if strings.Contains(string(data), `"accepted_root"`) {
		t.Fatalf("v2 store retained flattened v1 authority field: %s", data)
	}
	assertLegacyRecovery(t, path, legacyBytes)
	if _, err := Open(path); err != nil {
		t.Fatal(err)
	}
	assertLegacyRecovery(t, path, legacyBytes)
}

func TestLegacyV1MigrationFailureRetainsExactRecoveryArtifact(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*storeFileOps, string, error)
	}{
		{
			name: "live temporary write",
			configure: func(ops *storeFileOps, _ string, injected error) {
				write := ops.write
				ops.write = func(file *os.File, data []byte) (int, error) {
					name := filepath.Base(file.Name())
					if strings.HasPrefix(name, ".roots-") && !strings.HasPrefix(name, ".roots-v1-recovery-") {
						return 0, injected
					}
					return write(file, data)
				}
			},
		},
		{
			name: "post-rename directory sync",
			configure: func(ops *storeFileOps, livePath string, injected error) {
				syncParent := ops.syncParent
				ops.syncParent = func(path string) error {
					if path == livePath {
						return injected
					}
					return syncParent(path)
				}
			},
		},
		{
			name: "final live-file protection",
			configure: func(ops *storeFileOps, livePath string, injected error) {
				secure := ops.secure
				liveCalls := 0
				ops.secure = func(path string) error {
					if path == livePath {
						liveCalls++
						if liveCalls == 2 {
							return injected
						}
					}
					return secure(path)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "roots.json")
			legacyBytes := writeLegacyStore(t, path)
			injected := errors.New("injected migration failure")
			ops := defaultStoreFileOps()
			test.configure(&ops, path, injected)
			if _, err := openWithFileOps(path, ops); !errors.Is(err, injected) {
				t.Fatalf("Open error = %v, want injected failure", err)
			}
			assertLegacyRecovery(t, path, legacyBytes)
		})
	}
}

func TestObservedHeadNeverBecomesCandidateOrAcceptedImplicitly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roots.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Trust("docs", testRoot, "unixfs", "https://gateway.example", "manual"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddCandidate("docs", candidateRoot, testRoot, "local-write"); err != nil {
		t.Fatal(err)
	}
	record, err := store.ObserveHead("docs", observedHead(secondCandidateRoot, 7))
	if err != nil {
		t.Fatal(err)
	}
	if record.AcceptedRoot != testRoot || len(record.Candidates) != 1 || record.Candidates[0].Root != candidateRoot {
		t.Fatalf("remote observation changed accepted/candidate state: %#v", record)
	}
	state, err := store.GetState("docs")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ObservedHeads) != 1 || state.ObservedHeads[0].Root != secondCandidateRoot {
		t.Fatalf("remote observation was not recorded separately: %#v", state)
	}
	if _, err := store.AcceptCandidate("docs", secondCandidateRoot, "wrong-path"); !errors.Is(err, ErrCandidateNotFound) {
		t.Fatalf("observed root became candidate: %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	after, err := reopened.GetState("docs")
	if err != nil {
		t.Fatal(err)
	}
	if after.Accepted == nil || after.Accepted.Root != testRoot || len(after.ObservedHeads) != 1 || after.ObservedHeads[0].Root != secondCandidateRoot {
		t.Fatalf("restart changed observation authority: %#v", after)
	}
}

func TestObservedOnlyAliasRequiresExplicitObservationAcceptance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roots.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ObserveHead("docs", observedHead(testRoot, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get("docs"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("compatibility Get exposed observation-only alias: %v", err)
	}
	compatibilityRecords, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(compatibilityRecords) != 0 {
		t.Fatalf("compatibility List exposed observation-only state: %#v", compatibilityRecords)
	}
	states, err := store.ListStates()
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].Accepted != nil || len(states[0].ObservedHeads) != 1 {
		t.Fatalf("structured state omitted observation-only alias: %#v", states)
	}
	if _, _, err := AcceptedRoot(store, "docs"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("observed-only alias accepted-root error = %v", err)
	}
	if _, err := store.AddCandidate("docs", candidateRoot, testRoot, "local-write"); !errors.Is(err, ErrStaleCandidate) {
		t.Fatalf("observed-only alias accepted candidate: %v", err)
	}
	if _, err := store.AcceptObserved("docs", candidateRoot, "unixfs", "https://gateway.example", "manual"); !errors.Is(err, ErrObservationNotFound) {
		t.Fatalf("unobserved root acceptance error = %v", err)
	}
	record, err := store.AcceptObserved("docs", testRoot, "unixfs", "https://gateway.example", "manual")
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.GetState("docs")
	if err != nil {
		t.Fatal(err)
	}
	if record.AcceptedRoot != testRoot || len(record.Candidates) != 0 || len(state.ObservedHeads) != 1 {
		t.Fatalf("explicit observation acceptance record=%#v state=%#v", record, state)
	}
}

func TestObservedHeadRejectsStaleAndSameRevisionConflictAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "roots.json")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ObserveHead("docs", observedHead(candidateRoot, 9)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ObserveHead("docs", observedHead(testRoot, 8)); !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("stale observation error = %v", err)
	}
	conflict := observedHead(testRoot, 9)
	conflict.CommitID = "different-commit"
	if _, err := store.ObserveHead("docs", conflict); !errors.Is(err, ErrConflictingObservation) {
		t.Fatalf("same-revision conflict error = %v", err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	state, err := reopened.GetState("docs")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.ObservedHeads) != 1 || state.ObservedHeads[0].Root != candidateRoot || state.ObservedHeads[0].Revision != 9 {
		t.Fatalf("rejected observations changed persisted state: %#v", state)
	}
}

func TestObserveHeadRejectsMalformedTupleWithoutCreatingAlias(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "roots.json"))
	if err != nil {
		t.Fatal(err)
	}
	malformed := observedHead("not-a-cid", 1)
	if _, err := store.ObserveHead("docs", malformed); err == nil {
		t.Fatal("ObserveHead accepted a corrupt remote root")
	}
	if _, err := store.Get("docs"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("malformed observation created trust state: %v", err)
	}
}

func observedHead(root string, revision uint64) ObservedHead {
	return ObservedHead{
		Source: "https://gateway.example", DatasetID: "bucket-one", Branch: "main",
		CommitID: fmt.Sprintf("commit-%d", revision), Root: root, Revision: revision,
	}
}

func alternateCIDString(t *testing.T, raw string) string {
	t.Helper()
	parsed, err := cid.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	alternate, err := parsed.StringOfBase(multibase.Base36)
	if err != nil {
		t.Fatal(err)
	}
	if alternate == parsed.String() {
		t.Fatalf("alternate CID representation did not change for %s", raw)
	}
	return alternate
}

func writeTestState(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeLegacyStore(t *testing.T, path string) []byte {
	t.Helper()
	value := legacyState{Version: 1, Roots: map[string]Record{
		"docs": {Alias: "docs", AcceptedRoot: testRoot, Profile: "unixfs", Source: "manual"},
	}}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return data
}

func assertLegacyRecovery(t *testing.T, path string, want []byte) {
	t.Helper()
	recoveryPath := LegacyRecoveryPath(path)
	got, err := os.ReadFile(recoveryPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("legacy recovery bytes changed:\n got %q\nwant %q", got, want)
	}
	info, err := os.Stat(recoveryPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("legacy recovery mode = %o, want owner-only", info.Mode().Perm())
	}
}

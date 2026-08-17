package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	clientadd "github.com/dewebprotocol/malt-client/application/add"
	"github.com/dewebprotocol/malt-client/bucketsync"
	cid "github.com/ipfs/go-cid"
)

type recordedPlanObservation struct {
	alias, source, datasetID, branch, commitID string
	root                                       cid.Cid
	revision                                   uint64
}

type recordingPlanRootPolicy struct {
	accepted    cid.Cid
	acceptedErr error
	observeErr  error
	observed    []recordedPlanObservation
}

func (p *recordingPlanRootPolicy) AcceptedRoot(string) (cid.Cid, error) {
	return p.accepted, p.acceptedErr
}

func (*recordingPlanRootPolicy) ObserveCandidate(string, cid.Cid, cid.Cid, string) error {
	return nil
}

func (p *recordingPlanRootPolicy) ObserveHead(alias, source, datasetID, branch, commitID string, root cid.Cid, revision uint64) error {
	p.observed = append(p.observed, recordedPlanObservation{
		alias: alias, source: source, datasetID: datasetID, branch: branch,
		commitID: commitID, root: root, revision: revision,
	})
	return p.observeErr
}

func TestAddPlanMaterializerLegacyUnkeyedLiteralRemainsSourceCompatible(t *testing.T) {
	var _ PlanMaterializer = AddPlanMaterializer{nil, nil}
	_ = NewAddPlanMaterializer(nil, nil)
	_ = PlanServiceOptions{Plan{}, "", "", nil, nil, nil, nil, nil, nil, nil, nil, nil}
	_ = UnacceptedRootError{"", "", cid.Undef, cid.Undef, false, nil}
	_ = PlanFailure{"", "", "", "", false, "", false, nil, "", "", "", false, ""}
}

type legacyPlanRootPolicy struct{}

func (legacyPlanRootPolicy) AcceptedRoot(string) (cid.Cid, error)                    { return cid.Undef, nil }
func (legacyPlanRootPolicy) ObserveCandidate(string, cid.Cid, cid.Cid, string) error { return nil }

func TestLegacyPlanRootPolicyRemainsSourceCompatibleAndFailsClosedForObservation(t *testing.T) {
	var legacy PlanRootPolicy = legacyPlanRootPolicy{}
	service := &PlanService{
		plan:  Plan{ID: "plan-one", Name: "documents", BucketID: "bucket-one", Branch: "main"},
		sync:  &fakeSync{workspace: bucketsync.Workspace{Remote: bucketsync.Head{CommitID: "commit-one", Root: "bafkqaaa", Revision: 1}}},
		roots: legacy,
	}
	if _, err := service.acceptedObservedRoot(t.Context()); err == nil || !strings.Contains(err.Error(), "does not support remote head observations") {
		t.Fatalf("legacy policy observation error = %v", err)
	}
}

func TestAcceptedObservedRootRecordsObservationNotCandidate(t *testing.T) {
	remoteRoot := cid.MustParse("bafkqaaa")
	policy := &recordingPlanRootPolicy{acceptedErr: errors.New("no accepted root")}
	service := &PlanService{
		plan: Plan{ID: "plan-one", Name: "documents", BucketID: "bucket-one", Branch: "main"},
		sync: &fakeSync{workspace: bucketsync.Workspace{
			Remote: bucketsync.Head{CommitID: "commit-seven", Root: remoteRoot.String(), Revision: 7},
		}},
		roots: policy,
	}
	_, err := service.acceptedObservedRoot(t.Context())
	var rootErr *UnacceptedRootError
	if !errors.As(err, &rootErr) {
		t.Fatalf("acceptedObservedRoot error = %v", err)
	}
	if rootErr.CandidateRecorded {
		t.Fatalf("unaccepted root state = %#v", rootErr)
	}
	if len(policy.observed) != 1 {
		t.Fatalf("observations = %#v", policy.observed)
	}
	observation := policy.observed[0]
	if observation.alias != "backup:bucket-one:main" || observation.source != "dataset:bucket-one" ||
		observation.datasetID != "bucket-one" || observation.branch != "main" ||
		observation.commitID != "commit-seven" || !observation.root.Equals(remoteRoot) || observation.revision != 7 {
		t.Fatalf("recorded observation = %#v", observation)
	}
}

func TestAcceptedObservedRootRejectsStaleObservationBeforeTrustSelection(t *testing.T) {
	staleErr := errors.New("stale observation")
	policy := &recordingPlanRootPolicy{accepted: cid.MustParse("bafkqaaa"), observeErr: staleErr}
	service := &PlanService{
		plan: Plan{ID: "plan-one", Name: "documents", BucketID: "bucket-one", Branch: "main"},
		sync: &fakeSync{workspace: bucketsync.Workspace{
			Remote: bucketsync.Head{CommitID: "commit-one", Root: "bafkqaaa", Revision: 1},
		}},
		roots: policy,
	}
	if _, err := service.acceptedObservedRoot(t.Context()); !errors.Is(err, staleErr) {
		t.Fatalf("stale observation error = %v", err)
	}
}

type inspectingPlanMaterializer struct {
	key      [32]byte
	branch   string
	order    *[]string
	restored map[string]string
}

func (m *inspectingPlanMaterializer) MaterializeManifest(ctx context.Context, archivePath string, _ cid.Cid) (*clientadd.Result, error) {
	*m.order = append(*m.order, "materialize:manifest")
	destination, err := os.MkdirTemp("", "malt-plan-manifest-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(destination)
	key := deriveManifestKey(m.key, m.branch)
	if err := restoreArchive(ctx, archivePath, destination, func(uint32) ([32]byte, error) {
		return key, nil
	}, false); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(destination, "manifest.json")); err != nil {
		return nil, err
	}
	return &clientadd.Result{NewRoot: "bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"}, nil
}

func (m *inspectingPlanMaterializer) MaterializeBinding(ctx context.Context, archivePath, bindingID string, _ cid.Cid) (*clientadd.Result, error) {
	*m.order = append(*m.order, "materialize:"+bindingID)
	destination, err := os.MkdirTemp("", "malt-plan-materializer-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(destination)
	key := deriveBindingKey(m.key, m.branch, bindingID)
	if err := restoreArchive(ctx, archivePath, destination, func(uint32) ([32]byte, error) {
		return key, nil
	}, false); err != nil {
		return nil, err
	}
	body, err := os.ReadFile(filepath.Join(destination, "value.txt"))
	if err != nil {
		return nil, err
	}
	m.restored[bindingID] = string(body)
	return &clientadd.Result{NewRoot: "bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"}, nil
}

func TestPlanBackupSnapshotsEveryChangedBindingBeforeRemoteObservation(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	for path, body := range map[string]string{first: "first-before", second: "second-before"} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "value.txt"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	plan := Plan{
		ID: "plan_test", Name: "documents", BucketID: "bucket-a", Branch: "main",
		CreatedAt: now, UpdatedAt: now,
		Bindings: []Binding{
			{ID: "binding_first", Name: "first", Source: first, ArchiveName: "first", CreatedAt: now},
			{ID: "binding_second", Name: "second", Source: second, ArchiveName: "second", CreatedAt: now},
		},
	}
	var order []string
	key := [32]byte{3, 1, 4}
	syncer := &fakeSync{
		workspace: bucketsyncWorkspaceInitialized(),
		order:     &order,
		onStatus: func() {
			if err := os.WriteFile(filepath.Join(first, "value.txt"), []byte("first-after"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	materializer := &inspectingPlanMaterializer{key: key, branch: "main", order: &order, restored: map[string]string{}}
	history, err := NewHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewPlanService(PlanServiceOptions{
		Plan: plan, TempDir: t.TempDir(), LockPath: filepath.Join(t.TempDir(), "plan.lock"),
		Keys: fixedKeys{epoch: 1, key: key}, Sync: syncer, Materializer: materializer, History: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Backup(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ChangedBindings) != 2 {
		t.Fatalf("changed bindings = %#v", result.ChangedBindings)
	}
	if materializer.restored["binding_first"] != "first-before" || materializer.restored["binding_second"] != "second-before" {
		t.Fatalf("materialized snapshots = %#v", materializer.restored)
	}
	if len(order) < 3 || order[0] != "status" {
		t.Fatalf("operation order = %#v", order)
	}
}

func TestPlanBackupSkipsUnchangedBindings(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "value.txt"), []byte("stable"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	plan := Plan{
		ID: "plan_test", Name: "documents", BucketID: "bucket-a", Branch: "main", CreatedAt: now,
		Bindings: []Binding{{ID: "binding_first", Name: "first", Source: source, ArchiveName: "first", CreatedAt: now}},
	}
	var order []string
	key := [32]byte{2, 7, 1}
	syncer := &fakeSync{workspace: bucketsyncWorkspaceInitialized(), order: &order}
	materializer := &inspectingPlanMaterializer{key: key, branch: "main", order: &order, restored: map[string]string{}}
	history, err := NewHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewPlanService(PlanServiceOptions{
		Plan: plan, TempDir: t.TempDir(), LockPath: filepath.Join(t.TempDir(), "plan.lock"),
		Keys: fixedKeys{epoch: 1, key: key}, Sync: syncer, Materializer: materializer, History: history,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Backup(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	order = nil
	second, err := service.Backup(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !second.Skipped || len(order) != 1 || order[0] != "status" {
		t.Fatalf("unchanged backup = %#v, order = %#v", second, order)
	}
	if err := os.WriteFile(filepath.Join(source, "value.txt"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	order = nil
	third, err := service.Backup(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if third.Skipped {
		t.Fatal("changed binding was skipped")
	}
	for _, operation := range order {
		if operation == "materialize:manifest" {
			t.Fatalf("unchanged manifest was re-encrypted and republished: %v", order)
		}
	}
}

func TestRestoredBaselinePreventsUnchangedRepublish(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "value.txt"), []byte("restored"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	plan := Plan{
		ID: "plan_restored", Name: "documents", BucketID: "bucket-a", Branch: "main",
		CreatedAt: now, UpdatedAt: now,
		Bindings: []Binding{{
			ID: "binding_restored", Name: "documents", Source: source,
			ArchiveName: "documents", CreatedAt: now,
		}},
	}
	var order []string
	key := [32]byte{5, 8, 13}
	history, err := NewHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	root := cid.MustParse("bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku")
	service, err := NewPlanService(PlanServiceOptions{
		Plan: plan, TempDir: t.TempDir(), LockPath: filepath.Join(t.TempDir(), "plan.lock"),
		Keys: fixedKeys{epoch: 1, key: key},
		Sync: &fakeSync{workspace: bucketsyncWorkspaceInitialized(), order: &order},
		Materializer: &inspectingPlanMaterializer{
			key: key, branch: "main", order: &order, restored: map[string]string{},
		},
		History: history, Roots: fixedRootPolicy{root: root},
	})
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := service.RecordRestoredBaseline(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if baseline.CandidateRoot != root.String() {
		t.Fatalf("restored baseline root = %s", baseline.CandidateRoot)
	}
	order = nil
	result, err := service.Backup(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Skipped || len(order) != 1 || order[0] != "status" {
		t.Fatalf("restored backup = %#v, order=%v", result, order)
	}
}

func bucketsyncWorkspaceInitialized() bucketsync.Workspace {
	return bucketsync.Workspace{Initialized: true}
}

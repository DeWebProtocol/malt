package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dewebprotocol/malt-client/bucketsync"
	encryptedfs "github.com/dewebprotocol/malt-client/unixfs/encrypted"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

type recordedPlanObservation struct {
	alias, source, datasetID, branch, commitID string
	root                                       cid.Cid
	revision                                   uint64
}

type recordingPlanRootPolicy struct {
	accepted     cid.Cid
	acceptedErr  error
	candidateErr error
	observeErr   error
	candidates   []recordedPlanCandidate
	observed     []recordedPlanObservation
}

type recordedPlanCandidate struct {
	alias, source string
	candidate     cid.Cid
	base          cid.Cid
}

func (p *recordingPlanRootPolicy) AcceptedRoot(string) (cid.Cid, error) {
	return p.accepted, p.acceptedErr
}

func (p *recordingPlanRootPolicy) ObserveCandidate(alias string, candidate, base cid.Cid, source string) error {
	p.candidates = append(p.candidates, recordedPlanCandidate{alias: alias, source: source, candidate: candidate, base: base})
	return p.candidateErr
}

func (p *recordingPlanRootPolicy) HasCandidate(alias string, candidate, base cid.Cid) (bool, error) {
	for _, value := range p.candidates {
		if value.alias == alias && value.candidate.Equals(candidate) &&
			((!value.base.Defined() && !base.Defined()) || value.base.Equals(base)) {
			return true, nil
		}
	}
	return false, nil
}

func (p *recordingPlanRootPolicy) ObserveHead(alias, source, datasetID, branch, commitID string, root cid.Cid, revision uint64) error {
	p.observed = append(p.observed, recordedPlanObservation{
		alias: alias, source: source, datasetID: datasetID, branch: branch,
		commitID: commitID, root: root, revision: revision,
	})
	return p.observeErr
}

func TestCurrentPlanFilesystemCapabilityAndErrorShapesCompile(t *testing.T) {
	_ = PlanServiceOptions{Plan: Plan{}}
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
	if _, err := service.acceptedObservedRoot(t.Context(), cid.Undef); err == nil || !strings.Contains(err.Error(), "does not support remote head observations") {
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
	_, err := service.acceptedObservedRoot(t.Context(), cid.Undef)
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

func TestAcceptedObservedRootIdentifiesMatchingLocalCandidate(t *testing.T) {
	remoteRoot := cid.MustParse("bafkqaaa")
	policy := &recordingPlanRootPolicy{acceptedErr: errors.New("no accepted root")}
	service := &PlanService{
		plan: Plan{ID: "plan-one", Name: "documents", BucketID: "bucket-one", Branch: "main"},
		sync: &fakeSync{workspace: bucketsync.Workspace{
			Remote: bucketsync.Head{CommitID: "commit-seven", Root: remoteRoot.String(), Revision: 7},
		}},
		roots: policy,
	}
	_, err := service.acceptedObservedRoot(t.Context(), remoteRoot)
	var rootErr *UnacceptedRootError
	if !errors.As(err, &rootErr) || !rootErr.CandidateRecorded {
		t.Fatalf("matching candidate error = %#v, %v", rootErr, err)
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
	if _, err := service.acceptedObservedRoot(t.Context(), cid.Undef); !errors.Is(err, staleErr) {
		t.Fatalf("stale observation error = %v", err)
	}
}

type inspectingPlanFilesystem struct {
	order                     *[]string
	snapshots                 map[string]string
	datasets                  map[string]*PlanDataset
	manifestBuilds            int
	manifestReuses            int
	lastManifestCID           cid.Cid
	root                      cid.Cid
	sourceFingerprintOverride string
	recoverSnapshots          int
	recoverDirectory          string
	recoverErr                error
	closeCalls                int
	closeErr                  error
}

func newInspectingPlanFilesystem(order *[]string) *inspectingPlanFilesystem {
	root, err := maltcid.NewMapKZGCid(make([]byte, maltcid.KZGCommitmentSize))
	if err != nil {
		panic(err)
	}
	return &inspectingPlanFilesystem{
		order: order, snapshots: map[string]string{}, datasets: map[string]*PlanDataset{}, root: root,
	}
}

func (m *inspectingPlanFilesystem) BeginSnapshot(_ context.Context, _ maltcid.BackendKind, _ string) (PlanFilesystemSnapshot, error) {
	*m.order = append(*m.order, "begin:snapshot")
	return m, nil
}

func (m *inspectingPlanFilesystem) DefaultBackend(context.Context) (maltcid.BackendKind, error) {
	*m.order = append(*m.order, "profile:backend")
	return maltcid.BackendKindKZG, nil
}

func (m *inspectingPlanFilesystem) RecoverSnapshots(_ context.Context, directory string) error {
	m.recoverSnapshots++
	m.recoverDirectory = directory
	return m.recoverErr
}

func (m *inspectingPlanFilesystem) Publish(context.Context) error {
	*m.order = append(*m.order, "publish:snapshot")
	return nil
}

func (m *inspectingPlanFilesystem) Close() error {
	m.closeCalls++
	return m.closeErr
}

func (m *inspectingPlanFilesystem) PrepareBinding(ctx context.Context, request encryptedfs.BindingSource) (encryptedfs.PreparedBinding, error) {
	*m.order = append(*m.order, "prepare:"+request.BindingID)
	body, err := os.ReadFile(filepath.Join(request.Source, "value.txt"))
	if err != nil {
		return encryptedfs.PreparedBinding{}, err
	}
	sourceFingerprint, err := fingerprintPinnedSource(ctx, request.Root, filepath.Base(request.Source))
	if err != nil {
		return encryptedfs.PreparedBinding{}, err
	}
	if m.sourceFingerprintOverride != "" {
		sourceFingerprint = m.sourceFingerprintOverride
	}
	m.snapshots[request.BindingID] = string(body)
	return encryptedfs.PreparedBinding{
		Manifest: encryptedfs.BindingManifest{
			ID: request.BindingID, Name: request.BindingName, PathName: request.PathName,
			Token: "e1-" + strings.Repeat("a", 52),
		},
		Root: cid.MustParse("bafkqaaa"), Epoch: request.Epoch, EncryptedBytes: int64(len(body)),
		SourceFingerprint: sourceFingerprint,
	}, nil
}

func (m *inspectingPlanFilesystem) BuildDataset(_ context.Context, build PlanDatasetBuildRequest) (encryptedfs.DatasetBuildResult, error) {
	*m.order = append(*m.order, "build:dataset")
	request := build.Request
	manifestCID := cid.Undef
	if build.ReuseManifest != nil {
		m.manifestReuses++
		manifestCID = build.ReuseManifest.manifestCID
	} else {
		m.manifestBuilds++
		manifestCID = cid.MustParse("bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku")
	}
	root := m.root
	bindings := make([]encryptedfs.BindingView, len(request.Bindings))
	manifests := make([]encryptedfs.BindingManifest, len(request.Bindings))
	for index, binding := range request.Bindings {
		bindings[index] = encryptedfs.BindingView{Manifest: binding.Manifest, Root: binding.Root}
		manifests[index] = binding.Manifest
	}
	view := &PlanDataset{
		root: root, manifestCID: manifestCID,
		manifest: encryptedfs.DatasetManifest{
			Profile: encryptedfs.ProfileID, Version: encryptedfs.ProfileVersion,
			DatasetID: request.DatasetID, PlanID: request.PlanID, DatasetName: request.DatasetName,
			Branch: request.Branch, Bindings: manifests,
		},
		bindings: bindings,
	}
	m.datasets[root.String()] = view
	m.lastManifestCID = manifestCID
	return encryptedfs.DatasetBuildResult{Root: root, ManifestCID: manifestCID}, nil
}

func (m *inspectingPlanFilesystem) LoadDataset(_ context.Context, root cid.Cid, _, _ string, _ encryptedfs.KeyResolver) (*PlanDataset, error) {
	value := m.datasets[root.String()]
	if value == nil {
		return nil, errors.New("dataset not found")
	}
	return value, nil
}

func (m *inspectingPlanFilesystem) RestoreBinding(_ context.Context, _ *PlanDataset, bindingID, destination string, _ encryptedfs.KeyResolver) error {
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(destination, "value.txt"), []byte(m.snapshots[bindingID]), 0o600)
}

func (m *inspectingPlanFilesystem) RestoreBindingRoot(_ context.Context, _ *PlanDataset, bindingID string, root *os.Root, _ encryptedfs.KeyResolver) error {
	return root.WriteFile("value.txt", []byte(m.snapshots[bindingID]), 0o600)
}

func TestPlanBackupChecksWorkspaceBeforeLocalSnapshot(t *testing.T) {
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
			{ID: "binding_first", Name: "first", Source: first, PathName: "first", CreatedAt: now},
			{ID: "binding_second", Name: "second", Source: second, PathName: "second", CreatedAt: now},
		},
	}
	var order []string
	key := [32]byte{3, 1, 4}
	workspace := bucketsyncWorkspaceInitialized(t)
	syncer := &fakeSync{workspace: workspace, order: &order}
	filesystem := newInspectingPlanFilesystem(&order)
	policy := &recordingPlanRootPolicy{accepted: cid.MustParse(workspace.Base.Root)}
	history, err := NewHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewPlanService(PlanServiceOptions{
		Plan: plan, LockPath: filepath.Join(t.TempDir(), "plan.lock"),
		Keys: fixedKeys{epoch: 1, key: key}, Sync: syncer, Filesystem: filesystem, History: history,
		Roots: policy,
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
	if filesystem.recoverSnapshots != 1 || filesystem.recoverDirectory != service.snapshotDirectory() {
		t.Fatalf("snapshot recovery calls=%d directory=%q", filesystem.recoverSnapshots, filesystem.recoverDirectory)
	}
	if filesystem.closeCalls != 1 {
		t.Fatalf("snapshot close calls = %d", filesystem.closeCalls)
	}
	if syncer.message != "encrypted MALT-native backup" || strings.Contains(syncer.message, plan.Name) ||
		strings.Contains(syncer.message, plan.Bindings[0].Name) || strings.Contains(syncer.message, plan.Bindings[0].PathName) {
		t.Fatalf("default remote commit message leaks local metadata: %q", syncer.message)
	}
	if len(policy.candidates) != 1 || !policy.candidates[0].candidate.Equals(filesystem.root) ||
		!policy.candidates[0].base.Equals(policy.accepted) || policy.candidates[0].alias != "backup:bucket-a:main" {
		t.Fatalf("recorded backup candidates = %#v", policy.candidates)
	}
	if filesystem.snapshots["binding_first"] != "first-before" || filesystem.snapshots["binding_second"] != "second-before" {
		t.Fatalf("encrypted filesystem snapshots = %#v", filesystem.snapshots)
	}
	if len(order) < 7 || order[0] != "status" || order[1] != "current" || order[2] != "begin:snapshot" ||
		order[3] != "prepare:binding_first" || order[4] != "prepare:binding_second" ||
		order[5] != "build:dataset" || order[6] != "publish:snapshot" {
		t.Fatalf("operation order = %#v", order)
	}
}

func TestPlanBackupMakesSnapshotRecoveryAndCleanupFailuresVisible(t *testing.T) {
	setup := func(t *testing.T) (*PlanService, *inspectingPlanFilesystem, *History, *[]string) {
		t.Helper()
		source := t.TempDir()
		if err := os.WriteFile(filepath.Join(source, "value.txt"), []byte("private"), 0o600); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		plan := Plan{
			ID: "plan_snapshot_cleanup", Name: "documents", BucketID: "bucket-a", Branch: "main", CreatedAt: now,
			Bindings: []Binding{{ID: "binding", Name: "documents", Source: source, PathName: "documents", CreatedAt: now}},
		}
		workspace := bucketsyncWorkspaceInitialized(t)
		order := &[]string{}
		filesystem := newInspectingPlanFilesystem(order)
		history, err := NewHistory(filepath.Join(t.TempDir(), "history.json"))
		if err != nil {
			t.Fatal(err)
		}
		service, err := NewPlanService(PlanServiceOptions{
			Plan: plan, LockPath: filepath.Join(t.TempDir(), "plan.lock"), Keys: fixedKeys{epoch: 1, key: [32]byte{3}},
			Sync: &fakeSync{workspace: workspace, order: order}, Filesystem: filesystem, History: history,
			Roots: &recordingPlanRootPolicy{accepted: cid.MustParse(workspace.Base.Root)},
		})
		if err != nil {
			t.Fatal(err)
		}
		return service, filesystem, history, order
	}

	t.Run("stale recovery", func(t *testing.T) {
		service, filesystem, _, order := setup(t)
		want := errors.New("stale snapshot cleanup failed")
		filesystem.recoverErr = want
		if _, err := service.Backup(t.Context(), ""); !errors.Is(err, want) {
			t.Fatalf("snapshot recovery error = %v", err)
		}
		if filesystem.recoverSnapshots != 1 || filesystem.closeCalls != 0 || len(*order) != 0 {
			t.Fatalf("recovery failure calls: recover=%d close=%d order=%v", filesystem.recoverSnapshots, filesystem.closeCalls, *order)
		}
	})

	t.Run("normal close", func(t *testing.T) {
		service, filesystem, history, order := setup(t)
		want := errors.New("snapshot cleanup failed")
		filesystem.closeErr = want
		result, err := service.Backup(t.Context(), "")
		if !errors.Is(err, want) || result == nil || result.CandidateRoot == "" {
			t.Fatalf("snapshot close result=%#v error=%v", result, err)
		}
		if filesystem.closeCalls != 1 || !slices.Contains(*order, "push") {
			t.Fatalf("close failure calls=%d order=%v", filesystem.closeCalls, *order)
		}
		if pending, pendingErr := history.Pending(); pendingErr != nil || pending != nil {
			t.Fatalf("completed publication pending=%#v err=%v", pending, pendingErr)
		}
	})
}

func TestPlanBackupAndRestorePreserveWhitespaceFilesystemPaths(t *testing.T) {
	parent := t.TempDir()
	spacedSource := filepath.Join(parent, "source ")
	plainSource := filepath.Join(parent, "source")
	for path, body := range map[string]string{spacedSource: "spaced", plainSource: "plain"} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "value.txt"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	plan := Plan{
		ID: "plan_spaces", Name: "Private Plan", BucketID: "bucket-spaces", Branch: "main", CreatedAt: now,
		Bindings: []Binding{{
			ID: "binding_spaces", Name: "Private Binding", Source: spacedSource,
			PathName: "   ", CreatedAt: now,
		}},
	}
	var order []string
	filesystem := newInspectingPlanFilesystem(&order)
	syncer := &fakeSync{workspace: bucketsync.Workspace{Initialized: true}, order: &order}
	policy := &recordingPlanRootPolicy{acceptedErr: errors.New("no accepted root")}
	history, err := NewHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewPlanService(PlanServiceOptions{
		Plan: plan, LockPath: filepath.Join(t.TempDir(), "plan.lock"),
		Keys: fixedKeys{epoch: 1, key: [32]byte{9}}, Sync: syncer,
		Filesystem: filesystem, History: history, Roots: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Backup(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	if filesystem.snapshots["binding_spaces"] != "spaced" {
		t.Fatalf("backup read adjacent directory: snapshots=%#v", filesystem.snapshots)
	}
	if syncer.message != "encrypted MALT-native backup" || strings.Contains(syncer.message, plan.Name) ||
		strings.Contains(syncer.message, plan.Bindings[0].Name) || strings.Contains(syncer.message, plan.Bindings[0].PathName) {
		t.Fatalf("default remote commit message leaks local metadata: %q", syncer.message)
	}
	policy.accepted = filesystem.root
	policy.acceptedErr = nil
	restoreParent := t.TempDir()
	spacedDestination := filepath.Join(restoreParent, "restore ")
	plainDestination := filepath.Join(restoreParent, "restore")
	if err := os.MkdirAll(plainDestination, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plainDestination, "sentinel"), []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := service.RestoreTo(t.Context(), spacedDestination, false); err != nil {
		t.Fatal(err)
	}
	restored, err := os.ReadFile(filepath.Join(spacedDestination, "   ", "value.txt"))
	if err != nil || string(restored) != "spaced" {
		t.Fatalf("restored whitespace path = %q err=%v", restored, err)
	}
	sentinel, err := os.ReadFile(filepath.Join(plainDestination, "sentinel"))
	if err != nil || string(sentinel) != "untouched" {
		t.Fatalf("adjacent plain destination changed: %q err=%v", sentinel, err)
	}
	if _, err := os.Stat(filepath.Join(plainDestination, "   ")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore wrote into adjacent plain destination: %v", err)
	}
}

func TestPlanBackupUsesTransportProfileForEmptyBucket(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "value.txt"), []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	plan := Plan{
		ID: "plan_empty", Name: "documents", BucketID: "bucket-empty", Branch: "main", CreatedAt: now,
		Bindings: []Binding{{ID: "binding", Name: "documents", Source: source, PathName: "documents", CreatedAt: now}},
	}
	var order []string
	filesystem := newInspectingPlanFilesystem(&order)
	policy := &recordingPlanRootPolicy{accepted: cid.Undef}
	history, err := NewHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewPlanService(PlanServiceOptions{
		Plan: plan, LockPath: filepath.Join(t.TempDir(), "plan.lock"),
		Keys: fixedKeys{epoch: 1, key: [32]byte{7}}, Sync: &fakeSync{workspace: bucketsync.Workspace{Initialized: true}, order: &order},
		Filesystem: filesystem, History: history, Roots: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Backup(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.CandidateRoot != filesystem.root.String() || len(order) < 3 || order[0] != "status" ||
		order[1] != "profile:backend" || order[2] != "current" {
		t.Fatalf("empty Bucket backup result=%#v order=%v", result, order)
	}
	if len(policy.candidates) != 1 || policy.candidates[0].base.Defined() {
		t.Fatalf("bootstrap backup candidate = %#v", policy.candidates)
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
		Bindings: []Binding{{ID: "binding_first", Name: "first", Source: source, PathName: "first", CreatedAt: now}},
	}
	var order []string
	key := [32]byte{2, 7, 1}
	workspace := bucketsyncWorkspaceInitialized(t)
	syncer := &fakeSync{workspace: workspace, order: &order}
	filesystem := newInspectingPlanFilesystem(&order)
	history, err := NewHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewPlanService(PlanServiceOptions{
		Plan: plan, LockPath: filepath.Join(t.TempDir(), "plan.lock"),
		Keys: fixedKeys{epoch: 1, key: key}, Sync: syncer, Filesystem: filesystem, History: history,
		Roots: fixedRootPolicy{root: cid.MustParse(workspace.Base.Root)},
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
	if filesystem.manifestBuilds != 1 || filesystem.manifestReuses != 1 {
		t.Fatalf("manifest builds=%d reuses=%d order=%v", filesystem.manifestBuilds, filesystem.manifestReuses, order)
	}
}

func TestPlanBackupRepublishesLegacyArchiveResultAsEncryptedFilesystem(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "value.txt"), []byte("stable"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	plan := Plan{
		ID: "plan_migrate", Name: "documents", BucketID: "bucket-a", Branch: "main", CreatedAt: now,
		Bindings: []Binding{{ID: "binding_first", Name: "first", Source: source, PathName: "first", CreatedAt: now}},
	}
	var order []string
	filesystem := newInspectingPlanFilesystem(&order)
	history, err := NewHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	workspace := bucketsyncWorkspaceInitialized(t)
	service, err := NewPlanService(PlanServiceOptions{
		Plan: plan, LockPath: filepath.Join(t.TempDir(), "plan.lock"),
		Keys:       fixedKeys{epoch: 1, key: [32]byte{2, 7, 1}},
		Sync:       &fakeSync{workspace: workspace, order: &order},
		Filesystem: filesystem, History: history,
		Roots: fixedRootPolicy{root: cid.MustParse(workspace.Base.Root)},
	})
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := FingerprintSource(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	manifestFingerprint, err := service.manifestFingerprint()
	if err != nil {
		t.Fatal(err)
	}
	if err := history.RecordResult(plan.ID, Result{
		PlanID: plan.ID, PlanName: plan.Name, Profile: "malt-backup/",
		BindingFingerprints: map[string]string{"binding_first": fingerprint},
		ManifestFingerprint: manifestFingerprint, CompletedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	result, err := service.Backup(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Skipped || result.Profile != encryptedfs.ProfileID || filesystem.manifestBuilds != 1 {
		t.Fatalf("legacy profile migration result=%#v manifest builds=%d", result, filesystem.manifestBuilds)
	}
}

func TestPlanBackupRejectsLegacyPendingProfileBeforeRemoteWork(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "value.txt"), []byte("stable"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	plan := Plan{
		ID: "plan_legacy_pending", Name: "documents", BucketID: "bucket-a", Branch: "main", CreatedAt: now,
		Bindings: []Binding{{ID: "binding", Name: "documents", Source: source, PathName: "documents", CreatedAt: now}},
	}
	var order []string
	workspace := bucketsyncWorkspaceInitialized(t)
	filesystem := newInspectingPlanFilesystem(&order)
	history, err := NewHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := history.SetPending(PendingBackup{
		BucketID: plan.BucketID, PlanID: plan.ID, Message: "legacy", CreatedAt: now,
		Result: Result{
			PlanID: plan.ID, PlanName: plan.Name, Source: plan.ID,
			Profile: "malt-backup/", Base: workspace.Base, CandidateRoot: filesystem.root.String(),
		},
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewPlanService(PlanServiceOptions{
		Plan: plan, LockPath: filepath.Join(t.TempDir(), "plan.lock"),
		Keys: fixedKeys{epoch: 1, key: [32]byte{7}}, Sync: &fakeSync{workspace: workspace, order: &order},
		Filesystem: filesystem, History: history, Roots: fixedRootPolicy{root: cid.MustParse(workspace.Base.Root)},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Backup(t.Context(), "")
	if !errors.Is(err, ErrPendingWorkspace) || !strings.Contains(err.Error(), "previous MALT runtime") {
		t.Fatalf("legacy pending error = %v", err)
	}
	if result == nil || result.Profile != "malt-backup/" {
		t.Fatalf("legacy pending result = %#v", result)
	}
	if len(order) != 0 {
		t.Fatalf("legacy pending profile performed remote work: %v", order)
	}
	if pending, err := history.Pending(); err != nil || pending == nil || pending.Result.Profile != "malt-backup/" {
		t.Fatalf("legacy pending journal changed: pending=%#v err=%v", pending, err)
	}
}

func TestPlanBackupRetryRestoresExactDurableCandidateBeforeRemoteWork(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "value.txt"), []byte("stable"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	plan := Plan{
		ID: "plan_pending", Name: "documents", BucketID: "bucket-a", Branch: "main", CreatedAt: now,
		Bindings: []Binding{{ID: "binding", Name: "documents", Source: source, PathName: "documents", CreatedAt: now}},
	}
	workspace := bucketsyncWorkspaceInitialized(t)
	base := cid.MustParse(workspace.Base.Root)
	candidate, err := maltcid.NewMapKZGCid(append([]byte{1}, make([]byte, maltcid.KZGCommitmentSize-1)...))
	if err != nil {
		t.Fatal(err)
	}
	var order []string
	policy := &recordingPlanRootPolicy{accepted: base, candidates: []recordedPlanCandidate{{
		alias: "backup:bucket-a:main", source: "encrypted-backup:" + plan.ID, candidate: candidate, base: base,
	}}}
	history, err := NewHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := history.SetPending(PendingBackup{
		BucketID: plan.BucketID, PlanID: plan.ID, Message: "retry", CandidateBase: base.String(), CandidateRecorded: true, CreatedAt: now,
		Result: Result{
			PlanID: plan.ID, PlanName: plan.Name, Source: plan.ID, Profile: encryptedfs.ProfileID,
			Base: workspace.Base, CandidateRoot: candidate.String(),
		},
	}); err != nil {
		t.Fatal(err)
	}
	service, err := NewPlanService(PlanServiceOptions{
		Plan: plan, LockPath: filepath.Join(t.TempDir(), "plan.lock"), Keys: fixedKeys{epoch: 1, key: [32]byte{7}},
		Sync: &fakeSync{workspace: workspace, order: &order}, Filesystem: newInspectingPlanFilesystem(&order),
		History: history, Roots: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Backup(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.RetriedPending || len(policy.candidates) != 1 || !policy.candidates[0].candidate.Equals(candidate) || !policy.candidates[0].base.Equals(base) {
		t.Fatalf("retried candidate result=%#v candidates=%#v", result, policy.candidates)
	}
	if len(order) < 3 || order[0] != "status" || order[1] != "stage" || order[2] != "push" {
		t.Fatalf("pending retry order = %v", order)
	}
}

func TestPlanBackupRejectsConflictAndUnacceptedBaseBeforeSnapshotPublication(t *testing.T) {
	source := t.TempDir()
	stableSource := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "value.txt"), []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stableSource, "value.txt"), []byte("stable"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	plan := Plan{
		ID: "plan_gate", Name: "documents", BucketID: "bucket-a", Branch: "main", CreatedAt: now,
		Bindings: []Binding{
			{ID: "binding", Name: "documents", Source: source, PathName: "documents", CreatedAt: now},
			{ID: "stable", Name: "stable", Source: stableSource, PathName: "stable", CreatedAt: now},
		},
	}
	for _, test := range []struct {
		name      string
		workspace func(*testing.T) bucketsync.Workspace
		policy    func(*testing.T, bucketsync.Workspace) PlanRootPolicy
		want      error
	}{
		{
			name: "conflict",
			workspace: func(t *testing.T) bucketsync.Workspace {
				workspace := bucketsyncWorkspaceInitialized(t)
				workspace.Stashes = []bucketsync.Stash{{ID: "stash", Status: "branched", Branch: "heads/conflict"}}
				return workspace
			},
			policy: func(_ *testing.T, workspace bucketsync.Workspace) PlanRootPolicy {
				return fixedRootPolicy{root: cid.MustParse(workspace.Base.Root)}
			},
			want: ErrBackupConflict,
		},
		{
			name:      "unaccepted base",
			workspace: bucketsyncWorkspaceInitialized,
			policy: func(t *testing.T, _ bucketsync.Workspace) PlanRootPolicy {
				commitment := make([]byte, maltcid.KZGCommitmentSize)
				commitment[0] = 1
				root, err := maltcid.NewMapKZGCid(commitment)
				if err != nil {
					t.Fatal(err)
				}
				return fixedRootPolicy{root: root}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			workspace := test.workspace(t)
			var order []string
			filesystem := newInspectingPlanFilesystem(&order)
			history, err := NewHistory(filepath.Join(t.TempDir(), "history.json"))
			if err != nil {
				t.Fatal(err)
			}
			service, err := NewPlanService(PlanServiceOptions{
				Plan: plan, LockPath: filepath.Join(t.TempDir(), "plan.lock"),
				Keys: fixedKeys{epoch: 1, key: [32]byte{1}}, Sync: &fakeSync{workspace: workspace, order: &order},
				Filesystem: filesystem, History: history, Roots: test.policy(t, workspace),
			})
			if err != nil {
				t.Fatal(err)
			}
			if test.name == "unaccepted base" {
				stableFingerprint, err := FingerprintSource(t.Context(), stableSource)
				if err != nil {
					t.Fatal(err)
				}
				manifestFingerprint, err := service.manifestFingerprint()
				if err != nil {
					t.Fatal(err)
				}
				if err := history.RecordResult(plan.ID, Result{
					PlanID: plan.ID, PlanName: plan.Name, Profile: encryptedfs.ProfileID,
					BindingFingerprints: map[string]string{"binding": "sha256:old", "stable": stableFingerprint},
					ManifestFingerprint: manifestFingerprint, CompletedAt: now,
				}); err != nil {
					t.Fatal(err)
				}
			}
			_, err = service.Backup(t.Context(), "")
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("backup error = %v, want %v", err, test.want)
			}
			if test.want == nil {
				var rootErr *UnacceptedRootError
				if !errors.As(err, &rootErr) {
					t.Fatalf("backup error = %v, want UnacceptedRootError", err)
				}
			}
			for _, operation := range order {
				if strings.HasPrefix(operation, "begin:") || strings.HasPrefix(operation, "prepare:") ||
					strings.HasPrefix(operation, "build:") || strings.HasPrefix(operation, "publish:") || operation == "stage" || operation == "push" {
					t.Fatalf("gate failure performed snapshot/publication operation %q: %v", operation, order)
				}
			}
		})
	}
}

func TestPlanBackupRejectsSourceRootReplacementBeforeEncryption(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory replacement semantics differ on Windows")
	}
	parent := t.TempDir()
	source := filepath.Join(parent, "source")
	replacement := filepath.Join(parent, "replacement")
	for path, body := range map[string]string{source: "approved", replacement: "replacement"} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "value.txt"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	plan := Plan{
		ID: "plan_swap", Name: "documents", BucketID: "bucket-a", Branch: "main", CreatedAt: now,
		Bindings: []Binding{{ID: "binding", Name: "documents", Source: source, PathName: "documents", CreatedAt: now}},
	}
	workspace := bucketsyncWorkspaceInitialized(t)
	var order []string
	syncer := &fakeSync{workspace: workspace, order: &order}
	syncer.onStatus = func() {
		syncer.onStatus = nil
		if err := os.Rename(source, filepath.Join(parent, "approved-old")); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, source); err != nil {
			t.Fatal(err)
		}
	}
	filesystem := newInspectingPlanFilesystem(&order)
	history, err := NewHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewPlanService(PlanServiceOptions{
		Plan: plan, LockPath: filepath.Join(t.TempDir(), "plan.lock"), Keys: fixedKeys{epoch: 1, key: [32]byte{3}},
		Sync: syncer, Filesystem: filesystem, History: history,
		Roots: fixedRootPolicy{root: cid.MustParse(workspace.Base.Root)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Backup(t.Context(), ""); err == nil || !strings.Contains(err.Error(), "changed before") {
		t.Fatalf("source replacement error = %v", err)
	}
	for _, operation := range order {
		if strings.HasPrefix(operation, "prepare:") || strings.HasPrefix(operation, "build:") ||
			strings.HasPrefix(operation, "publish:") || operation == "stage" || operation == "push" {
			t.Fatalf("source replacement reached publication operation %q: %v", operation, order)
		}
	}
}

func TestPlanBackupRejectsPreparedBytesThatDoNotMatchPinnedSource(t *testing.T) {
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "value.txt"), []byte("approved"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	plan := Plan{
		ID: "plan_encryption_gate", Name: "documents", BucketID: "bucket-a", Branch: "main", CreatedAt: now,
		Bindings: []Binding{{ID: "binding", Name: "documents", Source: source, PathName: "documents", CreatedAt: now}},
	}
	workspace := bucketsyncWorkspaceInitialized(t)
	var order []string
	filesystem := newInspectingPlanFilesystem(&order)
	filesystem.sourceFingerprintOverride = "sha256:" + strings.Repeat("0", 64)
	policy := &recordingPlanRootPolicy{accepted: cid.MustParse(workspace.Base.Root)}
	history, err := NewHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewPlanService(PlanServiceOptions{
		Plan: plan, LockPath: filepath.Join(t.TempDir(), "plan.lock"), Keys: fixedKeys{epoch: 1, key: [32]byte{3}},
		Sync: &fakeSync{workspace: workspace, order: &order}, Filesystem: filesystem, History: history, Roots: policy,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Backup(t.Context(), ""); err == nil || !strings.Contains(err.Error(), "bytes were encrypted") {
		t.Fatalf("encrypted-source mismatch error = %v", err)
	}
	if len(policy.candidates) != 0 {
		t.Fatalf("encrypted-source mismatch recorded candidate = %#v", policy.candidates)
	}
	for _, operation := range order {
		if strings.HasPrefix(operation, "build:") || strings.HasPrefix(operation, "publish:") ||
			operation == "stage" || operation == "push" {
			t.Fatalf("encrypted-source mismatch reached publication operation %q: %v", operation, order)
		}
	}
	if pending, err := history.Pending(); err != nil || pending != nil {
		t.Fatalf("encrypted-source mismatch pending=%#v err=%v", pending, err)
	}
}

func TestValidateDatasetForPlanRejectsRemoteMetadataDrift(t *testing.T) {
	now := time.Now().UTC()
	plan := Plan{
		ID: "plan", Name: "Documents", BucketID: "bucket", Branch: "main", CreatedAt: now,
		Bindings: []Binding{{ID: "binding", Name: "Notes", PathName: "Notes", Source: t.TempDir(), CreatedAt: now}},
	}
	manifest := encryptedfs.DatasetManifest{
		Profile: encryptedfs.ProfileID, Version: encryptedfs.ProfileVersion,
		DatasetID: "bucket", PlanID: "plan", DatasetName: "Documents", Branch: "main",
		Bindings: []encryptedfs.BindingManifest{{
			ID: "binding", Name: "Notes", PathName: "renamed", Token: "e1-" + strings.Repeat("a", 52),
		}},
	}
	if err := validateDatasetForPlan(manifest, plan); err == nil || !strings.Contains(err.Error(), "metadata") {
		t.Fatalf("remote metadata drift error = %v", err)
	}
	manifest.Bindings[0].PathName = "Notes"
	manifest.DatasetName = "renamed plan"
	if err := validateDatasetForPlan(manifest, plan); err == nil {
		t.Fatal("remote Plan-name drift was accepted")
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
			PathName: "documents", CreatedAt: now,
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
		Plan: plan, LockPath: filepath.Join(t.TempDir(), "plan.lock"),
		Keys:       fixedKeys{epoch: 1, key: key},
		Sync:       &fakeSync{workspace: bucketsyncWorkspaceInitialized(t), order: &order},
		Filesystem: newInspectingPlanFilesystem(&order),
		History:    history, Roots: fixedRootPolicy{root: root},
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

func bucketsyncWorkspaceInitialized(t *testing.T) bucketsync.Workspace {
	t.Helper()
	root, err := maltcid.NewMapKZGCid(make([]byte, maltcid.KZGCommitmentSize))
	if err != nil {
		t.Fatal(err)
	}
	head := bucketsync.Head{CommitID: "commit-base", Root: root.String(), Revision: 1}
	return bucketsync.Workspace{Initialized: true, Base: head, Remote: head}
}

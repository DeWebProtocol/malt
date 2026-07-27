package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	clientadd "github.com/dewebprotocol/malt-client/application/add"
	"github.com/dewebprotocol/malt-client/bucketsync"
	cid "github.com/ipfs/go-cid"
)

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

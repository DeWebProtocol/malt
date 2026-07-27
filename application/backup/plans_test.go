package backup

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidateSourceRejectsProtectedPathInEitherDirection(t *testing.T) {
	root := t.TempDir()
	protected := filepath.Join(root, "client-state")
	sourceInside := filepath.Join(protected, "accidental-source")
	sourceContaining := root
	for _, path := range []string{protected, sourceInside} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, source := range []string{sourceInside, sourceContaining} {
		if err := ValidateSource(source, []string{protected}); !errors.Is(err, ErrProtectedSource) {
			t.Fatalf("source %s error = %v", source, err)
		}
	}
}

func TestPlanStoreRequiresExplicitMergeAndRejectsOverlappingBindings(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	nested := filepath.Join(first, "nested")
	for _, path := range []string{first, second, nested} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := OpenPlanStore(filepath.Join(t.TempDir(), "plans.json"))
	if err != nil {
		t.Fatal(err)
	}
	plan, binding, err := store.Bind(BindRequest{
		BucketID: "bucket-a", BucketName: "documents", Branch: "main", Source: first,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Branch != "main" || len(plan.Bindings) != 1 || binding.Source != first {
		t.Fatalf("first binding = %#v, plan = %#v", binding, plan)
	}
	if _, _, err := store.Bind(BindRequest{
		BucketID: "bucket-a", BucketName: "documents", Branch: "main", Source: second,
	}); err == nil || !strings.Contains(err.Error(), "explicitly merge") {
		t.Fatalf("implicit merged binding error = %v", err)
	}
	plan, _, err = store.Bind(BindRequest{
		BucketID: "bucket-a", BucketName: "documents", Branch: "main", Source: second, Merge: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Bindings) != 2 {
		t.Fatalf("merged plan bindings = %#v", plan.Bindings)
	}
	if _, _, err := store.Bind(BindRequest{
		BucketID: "bucket-a", BucketName: "documents", Branch: "main",
		Source: nested, Merge: true, BindingName: "nested",
	}); err == nil || !strings.Contains(err.Error(), "overlaps") {
		t.Fatalf("overlapping binding error = %v", err)
	}
}

func TestPlanStoreUsesBranchesAsIndependentRestoreUnits(t *testing.T) {
	first := filepath.Join(t.TempDir(), "first")
	second := filepath.Join(t.TempDir(), "second")
	for _, path := range []string{first, second} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "plans.json")
	store, err := OpenPlanStore(path)
	if err != nil {
		t.Fatal(err)
	}
	mainPlan, _, err := store.Bind(BindRequest{BucketID: "bucket-a", BucketName: "docs", Branch: "main", Source: first})
	if err != nil {
		t.Fatal(err)
	}
	branchPlan, _, err := store.Bind(BindRequest{BucketID: "bucket-a", BucketName: "docs", Branch: "photos", Source: second})
	if err != nil {
		t.Fatal(err)
	}
	if mainPlan.ID == branchPlan.ID {
		t.Fatal("different branches were collapsed into one backup plan")
	}
	nested := filepath.Join(first, "nested")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Bind(BindRequest{
		BucketID: "bucket-b", BucketName: "other", Branch: "main", Source: nested,
	}); err == nil || !strings.Contains(err.Error(), "globally disjoint") {
		t.Fatalf("cross-plan overlapping binding error = %v", err)
	}
	reopened, err := OpenPlanStore(path)
	if err != nil {
		t.Fatal(err)
	}
	values, err := reopened.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != 2 {
		t.Fatalf("persisted plans = %#v", values)
	}
	scheduled, err := reopened.SetSchedule(branchPlan.ID, 30*time.Minute, true, "automatic")
	if err != nil {
		t.Fatal(err)
	}
	if scheduled.Every != 30*time.Minute || !scheduled.Enabled {
		t.Fatalf("scheduled plan = %#v", scheduled)
	}
}

func TestPlanStoreRejectsDuplicateBucketBranchTargets(t *testing.T) {
	now := time.Now().UTC()
	value := planFile{
		Version: planStoreVersion,
		Plans: map[string]Plan{
			"plan_one": {
				ID: "plan_one", Name: "one", BucketID: "bucket-a", Branch: "main",
				CreatedAt: now, UpdatedAt: now,
			},
			"plan_two": {
				ID: "plan_two", Name: "two", BucketID: "bucket-a", Branch: "main",
				CreatedAt: now, UpdatedAt: now,
			},
		},
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plans.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPlanStore(path); err == nil || !strings.Contains(err.Error(), "target the same Bucket") {
		t.Fatalf("duplicate Bucket branch target error = %v", err)
	}
}

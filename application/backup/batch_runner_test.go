package backup

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type batchTestEnvironment struct {
	plans    []Plan
	services map[string]*batchTestService
	buildErr map[string]error
}

func (e *batchTestEnvironment) List() ([]Plan, error) {
	return append([]Plan(nil), e.plans...), nil
}

func (e *batchTestEnvironment) Get(selector string) (Plan, error) {
	for _, plan := range e.plans {
		if plan.ID == selector || plan.Name == selector {
			return plan, nil
		}
	}
	return Plan{}, errors.New("missing plan " + selector)
}

func (e *batchTestEnvironment) PlanService(plan Plan) (PlanOperations, error) {
	if err := e.buildErr[plan.ID]; err != nil {
		return nil, err
	}
	return e.services[plan.ID], nil
}

type batchTestService struct {
	backupResult *Result
	backupErr    error
	syncResult   *Result
	syncErr      error
	calls        []string
	merge        []bool
}

func (s *batchTestService) Backup(_ context.Context, message string) (*Result, error) {
	s.calls = append(s.calls, "backup:"+message)
	return s.backupResult, s.backupErr
}

func (s *batchTestService) SyncWithOptions(_ context.Context, message string, opts SyncOptions) (*Result, error) {
	s.calls = append(s.calls, "sync:"+message)
	s.merge = append(s.merge, opts.MergeConflicts)
	return s.syncResult, s.syncErr
}

func TestBatchRunnerSharesSelectionAndExecutionAcrossBackupAndSync(t *testing.T) {
	fixed := time.Date(2026, time.August, 17, 8, 9, 10, 0, time.FixedZone("test", 2*60*60))
	plans := []Plan{
		{ID: "one", Name: "documents", BucketID: "bucket-one", Branch: "main"},
		{ID: "two", Name: "photos", BucketID: "bucket-two", Branch: "archive"},
	}
	one := &batchTestService{backupResult: &Result{PlanID: "one"}, syncResult: &Result{PlanID: "one"}}
	two := &batchTestService{backupResult: &Result{PlanID: "two"}, syncResult: &Result{PlanID: "two"}}
	environment := &batchTestEnvironment{
		plans: plans,
		services: map[string]*batchTestService{
			"one": one,
			"two": two,
		},
		buildErr: map[string]error{},
	}
	opens := 0
	runner, err := NewBatchRunner(BatchRunnerOptions{
		OpenEnvironment: func() (BatchEnvironment, error) {
			opens++
			return environment, nil
		},
		Now: func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatal(err)
	}

	backup, err := runner.BackupPlans(context.Background(), PlanRequest{
		Plans: []string{"photos", "two", "documents"}, Message: "snapshot",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := planRunIDs(backup.Runs), []string{"two", "one"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("backup run order = %v, want %v", got, want)
	}
	if !backup.CompletedAt.Equal(fixed.UTC()) || backup.CompletedAt.Location() != time.UTC {
		t.Fatalf("backup completion = %v, want %v UTC", backup.CompletedAt, fixed.UTC())
	}

	syncResult, err := runner.SyncPlans(context.Background(), PlanRequest{Message: "refresh", MergeConflicts: true})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := planRunIDs(syncResult.Runs), []string{"one", "two"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sync run order = %v, want %v", got, want)
	}
	if opens != 2 {
		t.Fatalf("environment opens = %d, want one coherent snapshot per operation", opens)
	}
	if got, want := one.calls, []string{"backup:snapshot", "sync:refresh"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("plan one calls = %v, want %v", got, want)
	}
	if got, want := two.calls, []string{"backup:snapshot", "sync:refresh"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("plan two calls = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(one.merge, []bool{true}) || !reflect.DeepEqual(two.merge, []bool{true}) {
		t.Fatalf("merge options were not shared across sync services: one=%v two=%v", one.merge, two.merge)
	}
}

func TestBatchRunnerReturnsPartialRunsAndTypedFailures(t *testing.T) {
	plan := Plan{ID: "one", Name: "documents", BucketID: "bucket-one", Branch: "main"}
	conflict := &ConflictError{Plan: plan.ID, Branch: "conflicts/local"}
	service := &batchTestService{backupResult: &Result{PlanID: plan.ID}, backupErr: conflict}
	environment := &batchTestEnvironment{
		plans: []Plan{plan}, services: map[string]*batchTestService{plan.ID: service}, buildErr: map[string]error{},
	}
	runner, err := NewBatchRunner(BatchRunnerOptions{
		OpenEnvironment: func() (BatchEnvironment, error) { return environment, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := runner.BackupPlans(context.Background(), PlanRequest{})
	if err == nil || !errors.Is(err, ErrBackupConflict) {
		t.Fatalf("backup error = %v, want wrapped conflict", err)
	}
	var batchErr *BatchError
	if !errors.As(err, &batchErr) || len(batchErr.Failures) != 1 {
		t.Fatalf("batch error = %#v, want one typed failure", err)
	}
	if len(result.Runs) != 1 || len(result.Failures) != 1 {
		t.Fatalf("partial result = %#v, want one run and one failure", result)
	}
	failure := result.Failures[0]
	if !failure.Conflict || !failure.MergeAvailable || failure.ConflictBranch != "conflicts/local" {
		t.Fatalf("conflict metadata = %#v", failure)
	}
}

func TestBatchRunnerRejectsInvalidOrUnavailableEnvironments(t *testing.T) {
	if _, err := NewBatchRunner(BatchRunnerOptions{}); err == nil {
		t.Fatal("NewBatchRunner accepted a nil environment factory")
	}

	tests := []struct {
		name string
		open func() (BatchEnvironment, error)
		want string
	}{
		{
			name: "open failure",
			open: func() (BatchEnvironment, error) { return nil, errors.New("load runtime config") },
			want: "load runtime config",
		},
		{
			name: "nil environment",
			open: func() (BatchEnvironment, error) { return nil, nil },
			want: "environment is nil",
		},
		{
			name: "no plans",
			open: func() (BatchEnvironment, error) {
				return &batchTestEnvironment{services: map[string]*batchTestService{}, buildErr: map[string]error{}}, nil
			},
			want: "no backup plans are configured",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner, err := NewBatchRunner(BatchRunnerOptions{OpenEnvironment: test.open})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runner.BackupPlans(context.Background(), PlanRequest{}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("BackupPlans error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestBatchRunnerRecordsCancellationWithoutConstructingPlanService(t *testing.T) {
	plan := Plan{ID: "one", Name: "documents", BucketID: "bucket-one", Branch: "main"}
	environment := &batchTestEnvironment{
		plans: []Plan{plan}, services: map[string]*batchTestService{}, buildErr: map[string]error{},
	}
	runner, err := NewBatchRunner(BatchRunnerOptions{
		OpenEnvironment: func() (BatchEnvironment, error) { return environment, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := runner.SyncPlans(ctx, PlanRequest{})
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("sync error = %v, want context cancellation", err)
	}
	if len(result.Runs) != 0 || len(result.Failures) != 1 || result.Failures[0].Error != context.Canceled.Error() {
		t.Fatalf("canceled result = %#v", result)
	}
}

func planRunIDs(runs []PlanRun) []string {
	result := make([]string, len(runs))
	for index, run := range runs {
		result[index] = run.PlanID
	}
	return result
}

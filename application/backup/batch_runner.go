package backup

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// PlanCatalog is the narrow persisted-plan capability required by BatchRunner.
// PlanStore implements it, while tests and alternate runtime compositions can
// provide an in-memory catalog.
type PlanCatalog interface {
	List() ([]Plan, error)
	Get(string) (Plan, error)
}

// PlanOperations is one configured plan's reusable application service.
type PlanOperations interface {
	Backup(context.Context, string) (*Result, error)
	SyncWithOptions(context.Context, string, SyncOptions) (*Result, error)
}

// BatchEnvironment binds a stable plan catalog to the operation services made
// from the same runtime configuration snapshot. Transport, keyring, and trust
// composition remain outside this application package.
type BatchEnvironment interface {
	PlanCatalog
	PlanService(Plan) (PlanOperations, error)
}

type BatchRunnerOptions struct {
	OpenEnvironment func() (BatchEnvironment, error)
	Now             func() time.Time
}

// BatchRunner is the shared backup/sync application service used by foreground
// commands, the local daemon API, and scheduled runs.
type BatchRunner struct {
	mu              sync.Mutex
	openEnvironment func() (BatchEnvironment, error)
	now             func() time.Time
}

func NewBatchRunner(opts BatchRunnerOptions) (*BatchRunner, error) {
	if opts.OpenEnvironment == nil {
		return nil, fmt.Errorf("backup batch environment factory is nil")
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	return &BatchRunner{openEnvironment: opts.OpenEnvironment, now: now}, nil
}

func (r *BatchRunner) BackupPlans(ctx context.Context, request PlanRequest) (*BatchResult, error) {
	return r.run(ctx, "backup", request)
}

func (r *BatchRunner) SyncPlans(ctx context.Context, request PlanRequest) (*BatchResult, error) {
	return r.run(ctx, "sync", request)
}

func (r *BatchRunner) run(ctx context.Context, operation string, request PlanRequest) (*BatchResult, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	environment, err := r.openEnvironment()
	if err != nil {
		return nil, err
	}
	if environment == nil {
		return nil, fmt.Errorf("backup batch environment is nil")
	}
	plans, err := selectPlans(environment, request.Plans)
	if err != nil {
		return nil, err
	}
	result := &BatchResult{Operation: operation, Runs: make([]PlanRun, 0, len(plans))}
	var causes []error
	for _, plan := range plans {
		if err := ctx.Err(); err != nil {
			causes = append(causes, err)
			result.Failures = append(result.Failures, newPlanFailure(plan, err))
			break
		}
		service, err := environment.PlanService(plan)
		if err != nil {
			causes = append(causes, err)
			result.Failures = append(result.Failures, newPlanFailure(plan, err))
			continue
		}
		if service == nil {
			err = fmt.Errorf("backup plan service for %q is nil", plan.Name)
			causes = append(causes, err)
			result.Failures = append(result.Failures, newPlanFailure(plan, err))
			continue
		}
		var run *Result
		if operation == "sync" {
			run, err = service.SyncWithOptions(ctx, request.Message, SyncOptions{
				MergeConflicts: request.MergeConflicts,
			})
		} else {
			run, err = service.Backup(ctx, request.Message)
		}
		if closer, ok := service.(interface{ Close() error }); ok {
			err = errors.Join(err, closer.Close())
		}
		if run != nil {
			result.Runs = append(result.Runs, PlanRun{
				PlanID: plan.ID, PlanName: plan.Name, BucketID: plan.BucketID, Branch: plan.Branch, Result: run,
			})
		}
		if err != nil {
			causes = append(causes, err)
			result.Failures = append(result.Failures, newPlanFailure(plan, err))
		}
	}
	result.CompletedAt = r.now().UTC()
	return result, NewBatchError(operation, result.Failures, causes)
}

func selectPlans(catalog PlanCatalog, selectors []string) ([]Plan, error) {
	if len(selectors) == 0 {
		values, err := catalog.List()
		if err != nil {
			return nil, err
		}
		if len(values) == 0 {
			return nil, fmt.Errorf("no backup plans are configured; run `malt backup bind <local-path> --bucket <bucket>`")
		}
		return values, nil
	}
	result := make([]Plan, 0, len(selectors))
	seen := map[string]struct{}{}
	for _, selector := range selectors {
		value, err := catalog.Get(selector)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[value.ID]; ok {
			continue
		}
		seen[value.ID] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func newPlanFailure(plan Plan, err error) PlanFailure {
	failure := PlanFailure{
		PlanID: plan.ID, PlanName: plan.Name, BucketID: plan.BucketID, Branch: plan.Branch,
		Conflict: errors.Is(err, ErrBackupConflict), Error: err.Error(),
	}
	var rootErr *UnacceptedRootError
	if errors.As(err, &rootErr) {
		failure.TrustAlias = rootErr.Alias
		failure.ObservedRoot = rootErr.Observed.String()
		failure.CandidateRecorded = rootErr.CandidateRecorded
		if rootErr.Accepted.Defined() {
			failure.AcceptedRoot = rootErr.Accepted.String()
		}
	}
	var conflictErr *ConflictError
	if errors.As(err, &conflictErr) {
		failure.ConflictBranch = conflictErr.Branch
		if conflictErr.Push.Result.Branch != nil {
			failure.ConflictBranch = conflictErr.Push.Result.Branch.Name
		}
		failure.MergeAvailable = true
	}
	var mergeErr *ManualMergeError
	if errors.As(err, &mergeErr) {
		checkout := mergeErr.Checkout
		failure.Checkout = &checkout
		failure.MergeAvailable = false
	}
	return failure
}

var _ PlanRunner = (*BatchRunner)(nil)

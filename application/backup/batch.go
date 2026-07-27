package backup

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type PlanRequest struct {
	Plans          []string `json:"plans,omitempty"`
	Message        string   `json:"message,omitempty"`
	MergeConflicts bool     `json:"merge_conflicts,omitempty"`
}

type PlanRun struct {
	PlanID   string  `json:"plan_id"`
	PlanName string  `json:"plan_name"`
	BucketID string  `json:"bucket_id"`
	Branch   string  `json:"branch"`
	Result   *Result `json:"result,omitempty"`
}

type PlanFailure struct {
	PlanID            string            `json:"plan_id"`
	PlanName          string            `json:"plan_name"`
	BucketID          string            `json:"bucket_id"`
	Branch            string            `json:"branch"`
	Conflict          bool              `json:"conflict,omitempty"`
	ConflictBranch    string            `json:"conflict_branch,omitempty"`
	MergeAvailable    bool              `json:"merge_available,omitempty"`
	Checkout          *ConflictCheckout `json:"checkout,omitempty"`
	TrustAlias        string            `json:"trust_alias,omitempty"`
	ObservedRoot      string            `json:"observed_root,omitempty"`
	AcceptedRoot      string            `json:"accepted_root,omitempty"`
	CandidateRecorded bool              `json:"candidate_recorded,omitempty"`
	Error             string            `json:"error"`
}

type BatchResult struct {
	Operation   string        `json:"operation"`
	Runs        []PlanRun     `json:"runs"`
	Failures    []PlanFailure `json:"failures,omitempty"`
	CompletedAt time.Time     `json:"completed_at"`
}

type PlanRunner interface {
	BackupPlans(context.Context, PlanRequest) (*BatchResult, error)
	SyncPlans(context.Context, PlanRequest) (*BatchResult, error)
}

type BatchError struct {
	Operation string
	Failures  []PlanFailure
	causes    []error
}

func NewBatchError(operation string, failures []PlanFailure, causes []error) error {
	if len(failures) == 0 {
		return nil
	}
	return &BatchError{
		Operation: strings.TrimSpace(operation),
		Failures:  append([]PlanFailure(nil), failures...),
		causes:    append([]error(nil), causes...),
	}
}

func (e *BatchError) Error() string {
	if e == nil {
		return ""
	}
	conflicts := 0
	for _, failure := range e.Failures {
		if failure.Conflict {
			conflicts++
		}
	}
	if conflicts == len(e.Failures) {
		return fmt.Sprintf("%s completed with %d backup conflict(s)", e.Operation, conflicts)
	}
	if conflicts != 0 {
		return fmt.Sprintf("%s completed with %d failure(s), including %d backup conflict(s)", e.Operation, len(e.Failures), conflicts)
	}
	return fmt.Sprintf("%s completed with %d failure(s)", e.Operation, len(e.Failures))
}

func (e *BatchError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return append([]error(nil), e.causes...)
}

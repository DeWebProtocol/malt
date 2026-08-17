package capability

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dewebprotocol/malt-client/internal/bucketbranch"
	cid "github.com/ipfs/go-cid"
)

// ObservedHead is an untrusted remote observation for one dataset branch.
// JSON tags intentionally retain the existing Bucket persistence contract.
type ObservedHead struct {
	DatasetID string    `json:"bucket_id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	State     string    `json:"state"`
	CommitID  string    `json:"commit_id,omitempty"`
	Root      string    `json:"root,omitempty"`
	Revision  uint64    `json:"revision"`
	CreatedBy string    `json:"created_by,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Commit struct {
	ID           string    `json:"id"`
	DatasetID    string    `json:"bucket_id"`
	Root         string    `json:"root"`
	Parents      []string  `json:"parents,omitempty"`
	BaseRoot     string    `json:"base_root,omitempty"`
	Author       string    `json:"author"`
	Credential   string    `json:"credential,omitempty"`
	ChangeSetCID string    `json:"change_set_cid,omitempty"`
	Message      string    `json:"message,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type Conflict struct {
	Coordinate string `json:"coordinate"`
	Base       string `json:"base,omitempty"`
	Local      string `json:"local,omitempty"`
	Remote     string `json:"remote,omitempty"`
}

type ApplyRequest struct {
	OperationID   string `json:"push_id"`
	Branch        string `json:"branch,omitempty"`
	BaseCommit    string `json:"base_commit,omitempty"`
	BaseRoot      string `json:"base_root,omitempty"`
	CandidateRoot string `json:"candidate_root"`
	BaseRevision  uint64 `json:"base_revision"`
	ChangeSetCID  string `json:"change_set_cid,omitempty"`
	Message       string `json:"message,omitempty"`
}

type ApplyResult struct {
	Status    string        `json:"status"`
	Head      ObservedHead  `json:"head"`
	Candidate Commit        `json:"candidate"`
	Commit    Commit        `json:"commit"`
	Branch    *ObservedHead `json:"branch,omitempty"`
	MergeBase string        `json:"merge_base,omitempty"`
	Conflicts []Conflict    `json:"conflicts,omitempty"`
}

func NormalizeBranch(raw string) (string, error) {
	return bucketbranch.NormalizeSelector(raw)
}

// NormalizeApplyRequest validates and canonicalizes a semantic dataset write
// before any transport performs the external side effect.
func NormalizeApplyRequest(selectedBranch string, request ApplyRequest) (ApplyRequest, error) {
	request.OperationID = strings.TrimSpace(request.OperationID)
	request.BaseCommit = strings.TrimSpace(request.BaseCommit)
	request.BaseRoot = strings.TrimSpace(request.BaseRoot)
	request.CandidateRoot = strings.TrimSpace(request.CandidateRoot)
	request.ChangeSetCID = strings.TrimSpace(request.ChangeSetCID)
	request.Message = strings.TrimSpace(request.Message)
	branch, err := NormalizeBranch(request.Branch)
	if err != nil {
		return ApplyRequest{}, err
	}
	selectedBranch, err = NormalizeBranch(selectedBranch)
	if err != nil {
		return ApplyRequest{}, err
	}
	if request.Branch != "" && branch != selectedBranch {
		return ApplyRequest{}, fmt.Errorf("dataset apply branch %q does not match selected branch %q", branch, selectedBranch)
	}
	request.Branch = ""
	if selectedBranch != "main" {
		request.Branch = selectedBranch
	}
	if request.OperationID == "" {
		return ApplyRequest{}, fmt.Errorf("dataset apply operation ID is empty")
	}
	candidate, err := cid.Parse(request.CandidateRoot)
	if err != nil {
		return ApplyRequest{}, fmt.Errorf("invalid candidate root: %w", err)
	}
	request.CandidateRoot = candidate.String()
	if (request.BaseCommit == "") != (request.BaseRoot == "") || (request.BaseCommit == "") != (request.BaseRevision == 0) {
		return ApplyRequest{}, fmt.Errorf("dataset base commit, root, and non-zero revision must be supplied together")
	}
	if request.BaseRoot != "" {
		base, err := cid.Parse(request.BaseRoot)
		if err != nil {
			return ApplyRequest{}, fmt.Errorf("invalid base root: %w", err)
		}
		request.BaseRoot = base.String()
	}
	if request.ChangeSetCID != "" {
		changeSet, err := cid.Parse(request.ChangeSetCID)
		if err != nil {
			return ApplyRequest{}, fmt.Errorf("invalid change-set CID: %w", err)
		}
		request.ChangeSetCID = changeSet.String()
	}
	return request, nil
}

// ValidateBinding rejects a capability that is bound to another logical
// dataset or branch before a write can be attempted.
func ValidateBinding(datasetID, branch string, binding DatasetBinding) error {
	branch, err := NormalizeBranch(branch)
	if err != nil {
		return err
	}
	boundBranch, err := NormalizeBranch(binding.Branch)
	if err != nil {
		return fmt.Errorf("remote dataset binding has invalid branch: %w", err)
	}
	if strings.TrimSpace(datasetID) == "" || binding.DatasetID != datasetID || boundBranch != branch {
		return fmt.Errorf("remote dataset binding %q/%q does not match %q/%q", binding.DatasetID, boundBranch, datasetID, branch)
	}
	return nil
}

func ValidateObservedHead(datasetID, branch string, value ObservedHead) error {
	branch, err := NormalizeBranch(branch)
	if err != nil {
		return err
	}
	wantName, wantKind := "main", "main"
	if branch != "main" {
		wantName, wantKind = "heads/"+branch, "explicit"
	}
	if strings.TrimSpace(datasetID) == "" || value.DatasetID != datasetID || value.Name != wantName || value.Kind != wantKind || value.State != "open" {
		return fmt.Errorf("remote returned an invalid dataset %s head", branch)
	}
	if value.CommitID == "" {
		if value.Root != "" || value.Revision != 0 {
			return fmt.Errorf("remote returned an invalid empty dataset head")
		}
		return nil
	}
	if value.Root == "" || value.Revision == 0 {
		return fmt.Errorf("remote returned an invalid dataset head tuple")
	}
	if _, err := cid.Parse(value.Root); err != nil {
		return fmt.Errorf("remote returned an invalid dataset head root")
	}
	return nil
}

// ValidateApplyResult binds an untrusted result to the logical dataset and
// exact idempotent request. It does not accept Result.Head.Root locally.
func ValidateApplyResult(datasetID string, request ApplyRequest, value ApplyResult) error {
	var err error
	request, err = NormalizeApplyRequest(request.Branch, request)
	if err != nil {
		return err
	}
	if err := ValidateObservedHead(datasetID, request.Branch, value.Head); err != nil {
		return err
	}
	if err := validateCommit(datasetID, value.Candidate); err != nil {
		return fmt.Errorf("remote returned an invalid dataset candidate: %w", err)
	}
	if err := validateCommit(datasetID, value.Commit); err != nil {
		return fmt.Errorf("remote returned an invalid final dataset commit: %w", err)
	}
	if value.Candidate.Root != request.CandidateRoot {
		return fmt.Errorf("remote returned a dataset candidate for a different root")
	}
	if value.Candidate.BaseRoot != request.BaseRoot {
		return fmt.Errorf("remote returned a dataset candidate for a different base root")
	}
	if value.Candidate.ChangeSetCID != request.ChangeSetCID || value.Candidate.Message != request.Message {
		return fmt.Errorf("remote returned a dataset candidate for different apply metadata")
	}
	if !candidateParentsMatchRequest(value.Candidate, request, value.Status == "branched") {
		return fmt.Errorf("remote returned a dataset candidate with inconsistent parents")
	}

	switch value.Status {
	case "fast_forward":
		if value.Branch != nil || len(value.Conflicts) != 0 || value.MergeBase != "" || value.Head.CommitID == "" {
			return fmt.Errorf("remote returned an inconsistent fast-forward dataset apply")
		}
		if !equalCommit(value.Candidate, value.Commit) || value.Commit.Root != request.CandidateRoot {
			return fmt.Errorf("remote returned a fast-forward result for a different candidate")
		}
		if !refPointsToCommit(value.Head, value.Commit) {
			return fmt.Errorf("remote returned a fast-forward head that does not point to the final commit")
		}
	case "merged":
		if value.Branch != nil || len(value.Conflicts) != 0 || value.Candidate.ID == value.Commit.ID || value.Head.CommitID == "" {
			return fmt.Errorf("remote returned an inconsistent merged dataset apply")
		}
		if value.MergeBase != request.BaseRoot {
			return fmt.Errorf("remote returned a merge result for a different base")
		}
		if len(value.Commit.Parents) != 2 || value.Commit.Parents[0] == "" || value.Commit.Parents[0] == value.Candidate.ID || value.Commit.Parents[1] != value.Candidate.ID {
			return fmt.Errorf("remote returned a merge commit with inconsistent parents")
		}
		if value.Commit.BaseRoot == "" {
			return fmt.Errorf("remote returned a merge commit without its remote base root")
		}
		if !refPointsToCommit(value.Head, value.Commit) {
			return fmt.Errorf("remote returned a merged head that does not point to the final commit")
		}
	case "branched":
		if value.Branch == nil || !equalCommit(value.Candidate, value.Commit) || len(value.Conflicts) == 0 || value.MergeBase != request.BaseRoot {
			return fmt.Errorf("remote returned an inconsistent conflicted dataset apply")
		}
		if err := validateConflictRef(datasetID, *value.Branch); err != nil {
			return err
		}
		if !refPointsToCommit(*value.Branch, value.Candidate) {
			return fmt.Errorf("remote returned a conflict branch that does not preserve the candidate")
		}
	default:
		return fmt.Errorf("remote returned unsupported dataset apply status %q", value.Status)
	}
	return nil
}

func validateCommit(datasetID string, value Commit) error {
	if value.ID == "" || value.DatasetID != datasetID {
		return fmt.Errorf("commit identity does not match the selected dataset")
	}
	if _, err := cid.Parse(value.Root); err != nil {
		return fmt.Errorf("invalid commit root")
	}
	if value.BaseRoot != "" {
		if _, err := cid.Parse(value.BaseRoot); err != nil {
			return fmt.Errorf("invalid commit base root")
		}
	}
	if value.ChangeSetCID != "" {
		if _, err := cid.Parse(value.ChangeSetCID); err != nil {
			return fmt.Errorf("invalid commit change-set CID")
		}
	}
	seen := make(map[string]struct{}, len(value.Parents))
	for _, parent := range value.Parents {
		if parent == "" || parent == value.ID {
			return fmt.Errorf("invalid commit parent")
		}
		if _, exists := seen[parent]; exists {
			return fmt.Errorf("duplicate commit parent")
		}
		seen[parent] = struct{}{}
	}
	return nil
}

func candidateParentsMatchRequest(candidate Commit, request ApplyRequest, allowMissing bool) bool {
	if request.BaseCommit == "" {
		return len(candidate.Parents) == 0
	}
	if len(candidate.Parents) == 1 && candidate.Parents[0] == request.BaseCommit {
		return true
	}
	return allowMissing && len(candidate.Parents) == 0
}

func validateConflictRef(datasetID string, value ObservedHead) error {
	nameParts := strings.Split(value.Name, "/")
	if value.DatasetID != datasetID || value.Kind != "conflict" || value.State != "open" || len(nameParts) != 3 || nameParts[0] != "conflicts" || nameParts[1] == "" || nameParts[2] == "" {
		return fmt.Errorf("remote returned an invalid dataset conflict ref")
	}
	if value.CommitID == "" || value.Root == "" || value.Revision == 0 {
		return fmt.Errorf("remote returned an empty dataset conflict ref")
	}
	if _, err := cid.Parse(value.Root); err != nil {
		return fmt.Errorf("remote returned an invalid dataset conflict ref root")
	}
	return nil
}

func refPointsToCommit(ref ObservedHead, commit Commit) bool {
	return ref.DatasetID == commit.DatasetID && ref.CommitID == commit.ID && ref.Root == commit.Root
}

func equalCommit(left, right Commit) bool {
	return left.ID == right.ID && left.DatasetID == right.DatasetID && left.Root == right.Root &&
		slices.Equal(left.Parents, right.Parents) && left.BaseRoot == right.BaseRoot &&
		left.Author == right.Author && left.Credential == right.Credential &&
		left.ChangeSetCID == right.ChangeSetCID && left.Message == right.Message && left.CreatedAt.Equal(right.CreatedAt)
}

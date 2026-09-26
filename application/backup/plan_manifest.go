package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dewebprotocol/malt-client/bucketsync"
	encryptedfs "github.com/dewebprotocol/malt-client/unixfs/encrypted"
	cid "github.com/ipfs/go-cid"
)

type planManifest struct {
	Version  int               `json:"version"`
	PlanID   string            `json:"plan_id"`
	PlanName string            `json:"plan_name"`
	Branch   string            `json:"branch"`
	Bindings []manifestBinding `json:"bindings"`
}

type manifestBinding struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	PathName string `json:"path_name"`
}

func (s *PlanService) manifestData() ([]byte, error) {
	manifest := planManifest{
		Version: 1, PlanID: s.plan.ID, PlanName: s.plan.Name, Branch: s.plan.Branch,
		Bindings: make([]manifestBinding, len(s.plan.Bindings)),
	}
	for i, binding := range s.plan.Bindings {
		manifest.Bindings[i] = manifestBinding{ID: binding.ID, Name: binding.Name, PathName: binding.PathName}
	}
	return json.Marshal(manifest)
}

func (s *PlanService) keyResolver() encryptedfs.KeyResolver {
	return func(epoch uint32) ([32]byte, error) {
		return s.keys.BucketKey(epoch, s.plan.BucketID)
	}
}

func (s *PlanService) loadDataset(ctx context.Context, root cid.Cid) (*PlanDataset, error) {
	return s.filesystem.LoadDataset(ctx, root, s.plan.BucketID, s.plan.Branch, s.keyResolver())
}

func planManifestFromDataset(manifest encryptedfs.DatasetManifest) planManifest {
	result := planManifest{
		Version: 1, PlanID: manifest.PlanID, PlanName: manifest.DatasetName,
		Branch: manifest.Branch, Bindings: make([]manifestBinding, len(manifest.Bindings)),
	}
	for index, binding := range manifest.Bindings {
		result.Bindings[index] = manifestBinding{
			ID: binding.ID, Name: binding.Name, PathName: binding.PathName,
		}
	}
	return result
}

func validateDatasetForPlan(manifest encryptedfs.DatasetManifest, plan Plan) error {
	if err := validateDatasetIdentity(manifest, plan); err != nil {
		return err
	}
	return validatePlanManifest(planManifestFromDataset(manifest), plan)
}

func validateDatasetIdentity(manifest encryptedfs.DatasetManifest, plan Plan) error {
	if manifest.DatasetID != plan.BucketID || manifest.PlanID != plan.ID || manifest.Branch != plan.Branch {
		return fmt.Errorf("remote encrypted filesystem does not match the selected local plan")
	}
	return nil
}

func (s *PlanService) manifestFingerprint() (string, error) {
	data, err := s.manifestData()
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}

func (s *PlanService) acceptedObservedRoot(ctx context.Context, localCandidate cid.Cid) (cid.Cid, error) {
	if s.roots == nil {
		return cid.Undef, fmt.Errorf("backup plan trusted-root policy is not configured")
	}
	workspace, err := s.sync.Pull(ctx)
	if err != nil {
		return cid.Undef, err
	}
	for _, stash := range workspace.Stashes {
		if stash.Status == "pending" {
			return cid.Undef, fmt.Errorf("%w in plan %s", ErrPendingWorkspace, s.plan.Name)
		}
	}
	head := workspace.Remote
	if strings.TrimSpace(head.Root) == "" {
		head = workspace.Base
	}
	observed, err := cid.Parse(strings.TrimSpace(head.Root))
	if err != nil {
		return cid.Undef, fmt.Errorf("decode observed backup branch root: %w", err)
	}
	alias := PlanRootAlias(s.plan.BucketID, s.plan.Branch)
	if err := s.roots.ObserveHead(
		alias, observationSource(s.plan.BucketID), s.plan.BucketID, s.plan.Branch,
		head.CommitID, observed, head.Revision,
	); err != nil {
		return cid.Undef, fmt.Errorf("record remote backup head for %s: %w", alias, err)
	}
	accepted, err := s.roots.AcceptedRoot(alias)
	if err != nil {
		return cid.Undef, &UnacceptedRootError{
			Plan: s.plan.Name, Alias: alias, Observed: observed,
			CandidateRecorded: localCandidate.Defined() && observed.Equals(localCandidate), Cause: err,
		}
	}
	if observed.Equals(accepted) {
		return accepted, nil
	}
	return cid.Undef, &UnacceptedRootError{
		Plan: s.plan.Name, Alias: alias, Observed: observed, Accepted: accepted,
		CandidateRecorded: localCandidate.Defined() && observed.Equals(localCandidate),
	}
}

func observationSource(datasetID string) string {
	return "dataset:" + strings.TrimSpace(datasetID)
}

func validatePlanManifest(manifest planManifest, local Plan) error {
	if manifest.Version != 1 || manifest.PlanID != local.ID || manifest.PlanName != local.Name ||
		manifest.Branch != local.Branch || len(manifest.Bindings) == 0 {
		return fmt.Errorf("remote backup plan manifest does not match the selected local plan")
	}
	localBindings := make(map[string]Binding, len(local.Bindings))
	for _, binding := range local.Bindings {
		localBindings[binding.ID] = binding
	}
	seenNames := map[string]struct{}{}
	for _, binding := range manifest.Bindings {
		localBinding, ok := localBindings[binding.ID]
		if !ok || binding.Name != localBinding.Name || binding.PathName != localBinding.PathName {
			return fmt.Errorf("remote backup plan binding %q does not match local plan metadata", binding.Name)
		}
		if err := validatePathName(binding.PathName); err != nil {
			return err
		}
		if _, ok := seenNames[binding.PathName]; ok {
			return fmt.Errorf("remote backup plan has duplicate path names")
		}
		seenNames[binding.PathName] = struct{}{}
	}
	if len(manifest.Bindings) != len(localBindings) {
		return fmt.Errorf("remote backup plan binding count does not match local plan metadata")
	}
	return nil
}

func validateDiscoveredPlanManifest(manifest planManifest, branch string) error {
	if manifest.Version != 1 || manifest.PlanID == "" || manifest.PlanName == "" ||
		manifest.Branch != branch || len(manifest.Bindings) == 0 {
		return fmt.Errorf("remote backup plan manifest is incomplete or targets another branch")
	}
	if err := validateOpaqueID(manifest.PlanID, "plan"); err != nil {
		return err
	}
	if err := validateDisplayName(manifest.PlanName, "plan"); err != nil {
		return err
	}
	seenIDs := map[string]struct{}{}
	seenNames := map[string]struct{}{}
	seenPaths := map[string]struct{}{}
	for _, binding := range manifest.Bindings {
		if err := validateOpaqueID(binding.ID, "binding"); err != nil {
			return err
		}
		if err := validateDisplayName(binding.Name, "binding"); err != nil {
			return err
		}
		if err := validatePathName(binding.PathName); err != nil {
			return err
		}
		if _, ok := seenIDs[binding.ID]; ok {
			return fmt.Errorf("remote backup plan has duplicate binding IDs")
		}
		if _, ok := seenNames[binding.Name]; ok {
			return fmt.Errorf("remote backup plan has duplicate binding names")
		}
		if _, ok := seenPaths[binding.PathName]; ok {
			return fmt.Errorf("remote backup plan has duplicate path names")
		}
		seenIDs[binding.ID] = struct{}{}
		seenNames[binding.Name] = struct{}{}
		seenPaths[binding.PathName] = struct{}{}
	}
	return nil
}

type ConflictError struct {
	Plan   string
	Branch string
	Push   bucketsync.PushOutcome
}

func (e *ConflictError) Error() string {
	branch := ""
	if e.Push.Result.Branch != nil {
		branch = e.Push.Result.Branch.Name
	}
	if branch == "" {
		branch = e.Branch
	}
	if branch == "" {
		return fmt.Sprintf("%v for plan %s", ErrBackupConflict, e.Plan)
	}
	return fmt.Sprintf("%v for plan %s; local candidate was preserved at %s", ErrBackupConflict, e.Plan, branch)
}

func (e *ConflictError) Unwrap() error { return ErrBackupConflict }

func firstBranchedStash(workspace bucketsync.Workspace) *bucketsync.Stash {
	for i := range workspace.Stashes {
		if workspace.Stashes[i].Status == "branched" {
			value := workspace.Stashes[i]
			return &value
		}
	}
	return nil
}

type UnacceptedRootError struct {
	Plan              string
	Alias             string
	Observed          cid.Cid
	Accepted          cid.Cid
	CandidateRecorded bool
	Cause             error
}

func (e *UnacceptedRootError) Error() string {
	if e.CandidateRecorded {
		if e.Accepted.Defined() {
			return fmt.Sprintf(
				"remote root %s for plan %s differs from accepted root %s; it matches a locally verified candidate—inspect it, run `malt root accept %s %s`, then rerun",
				e.Observed, e.Plan, e.Accepted, e.Alias, e.Observed,
			)
		}
		return fmt.Sprintf(
			"remote root %s for plan %s matches a locally verified bootstrap candidate; inspect it, run `malt root accept %s %s`, then rerun",
			e.Observed, e.Plan, e.Alias, e.Observed,
		)
	}
	if !e.Accepted.Defined() {
		return fmt.Sprintf(
			"remote root %s for plan %s is not locally accepted; inspect it, run `malt root accept-observed %s %s`, then rerun",
			e.Observed, e.Plan, e.Alias, e.Observed,
		)
	}
	return fmt.Sprintf(
		"remote root %s for plan %s differs from accepted root %s; it was recorded as an observation—inspect it, run `malt root accept-observed %s %s`, then rerun",
		e.Observed, e.Plan, e.Accepted, e.Alias, e.Observed,
	)
}

func (e *UnacceptedRootError) Unwrap() error { return ErrUnacceptedRoot }

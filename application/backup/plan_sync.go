package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dewebprotocol/malt-client/bucketsync"
	"github.com/dewebprotocol/malt-client/internal/filelock"
	encryptedfs "github.com/dewebprotocol/malt-client/unixfs/encrypted"
	cid "github.com/ipfs/go-cid"
)

type SyncOptions struct {
	MergeConflicts bool
}

// Sync preserves local changes before pulling. It leaves same-binding
// conflicts unresolved until the caller explicitly elects to merge them.
func (s *PlanService) Sync(ctx context.Context, message string) (*Result, error) {
	return s.SyncWithOptions(ctx, message, SyncOptions{})
}

// SyncWithOptions preserves and pushes local changes, optionally performs a
// conservative plaintext three-way merge for a branched candidate, and then
// installs only a locally accepted final branch root.
func (s *PlanService) SyncWithOptions(ctx context.Context, message string, opts SyncOptions) (*Result, error) {
	result, err := s.Backup(ctx, message)
	if err != nil {
		if !errors.Is(err, ErrBackupConflict) {
			return result, err
		}
		root, trustErr := s.acceptedObservedRootWithLock(ctx)
		if trustErr != nil {
			return result, trustErr
		}
		if !opts.MergeConflicts {
			return result, err
		}
		if mergeErr := s.mergeBranchedCandidate(ctx, root); mergeErr != nil {
			return result, mergeErr
		}
		result, err = s.Backup(ctx, message)
		if err != nil {
			return result, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := filelock.Acquire(s.lockPath, 10*time.Second)
	if err != nil {
		return result, err
	}
	defer func() { _ = unlock() }()
	localCandidate := cid.Undef
	if result != nil && strings.TrimSpace(result.CandidateRoot) != "" {
		localCandidate, err = cid.Parse(result.CandidateRoot)
		if err != nil {
			return result, fmt.Errorf("decode locally verified backup candidate: %w", err)
		}
	}
	root, err := s.acceptedObservedRoot(ctx, localCandidate)
	if err != nil {
		return result, err
	}
	if err := s.installBindings(ctx, root, result.BindingFingerprints); err != nil {
		return result, err
	}
	fingerprints, err := s.bindingFingerprints(ctx)
	if err != nil {
		return result, err
	}
	now := time.Now().UTC()
	if result == nil {
		result = &Result{}
	}
	result.PlanID, result.PlanName, result.Branch = s.plan.ID, s.plan.Name, s.plan.Branch
	result.Source = s.plan.ID
	result.SourceFingerprint = combinedFingerprint(fingerprints)
	result.BindingFingerprints = fingerprints
	result.CompletedAt = now
	if err := s.history.RecordResult(s.plan.ID, *result); err != nil {
		return result, err
	}
	return result, nil
}

func (s *PlanService) acceptedObservedRootWithLock(ctx context.Context) (cid.Cid, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := filelock.Acquire(s.lockPath, 10*time.Second)
	if err != nil {
		return cid.Undef, err
	}
	defer func() { _ = unlock() }()
	return s.acceptedObservedRoot(ctx, cid.Undef)
}

func (s *PlanService) mergeBranchedCandidate(ctx context.Context, remoteRoot cid.Cid) (resultErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := filelock.Acquire(s.lockPath, 10*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	if existing, err := s.history.Conflict(); err != nil {
		return err
	} else if existing != nil {
		return &ManualMergeError{Checkout: *existing}
	}
	workspace, err := s.sync.Status()
	if err != nil {
		return err
	}
	if workspace.Remote.Root != remoteRoot.String() {
		return fmt.Errorf("accepted conflict root %s is not the latest observed branch root %s", remoteRoot, workspace.Remote.Root)
	}
	var stash *bucketsync.Stash
	for i := range workspace.Stashes {
		if workspace.Stashes[i].Status != "branched" {
			continue
		}
		if stash != nil {
			return fmt.Errorf("plan %s has multiple unresolved conflict stashes", s.plan.Name)
		}
		value := workspace.Stashes[i]
		stash = &value
	}
	if stash == nil {
		return fmt.Errorf("plan %s has no branched candidate to merge", s.plan.Name)
	}
	localRoot, err := cid.Parse(stash.CandidateRoot)
	if err != nil {
		return err
	}
	baseRoot, err := rootCID(stash.Base.Root)
	if err != nil {
		return err
	}
	conflictRoot := s.lockPath + ".conflicts"
	if err := os.MkdirAll(conflictRoot, 0o700); err != nil {
		return err
	}
	checkoutTemp, err := os.MkdirTemp(conflictRoot, ".checkout-*")
	if err != nil {
		return err
	}
	keepCheckoutTemp := false
	defer func() {
		if !keepCheckoutTemp {
			_ = os.RemoveAll(checkoutTemp)
		}
	}()

	entries := make([]installTransactionEntry, 0, len(s.plan.Bindings))
	cleanupEntries := true
	defer func() {
		if cleanupEntries {
			resultErr = errors.Join(resultErr, cleanupInstallError(nil, entries))
		}
	}()
	bindingConflicts := make([]BindingConflict, 0)
	for _, binding := range s.plan.Bindings {
		versions := filepath.Join(checkoutTemp, safeID(binding.ID))
		basePath := filepath.Join(versions, "base")
		localPath := filepath.Join(versions, "local")
		remotePath := filepath.Join(versions, "remote")
		mergedPath := filepath.Join(versions, "merged")
		if err := os.MkdirAll(versions, 0o700); err != nil {
			return err
		}
		if err := s.restoreBindingVersion(ctx, baseRoot, binding, basePath, true); err != nil {
			return err
		}
		if err := s.restoreBindingVersion(ctx, localRoot, binding, localPath, false); err != nil {
			return err
		}
		if err := s.restoreBindingVersion(ctx, remoteRoot, binding, remotePath, true); err != nil {
			return err
		}
		if err := copyPlaintextTree(ctx, localPath, mergedPath); err != nil {
			return fmt.Errorf("prepare manual merge workspace for %s: %w", binding.Name, err)
		}
		unchanged, err := samePlaintextTree(ctx, binding.Source, localPath)
		if err != nil {
			return err
		}
		entry, err := prepareInstallEntry(binding.Name, binding.Source, ".malt-sync-")
		if err != nil {
			return err
		}
		expectedFingerprint, err := FingerprintSource(ctx, binding.Source)
		if err != nil {
			return cleanupInstallError(err, []installTransactionEntry{entry})
		}
		entry.ExpectedFingerprint = expectedFingerprint
		entry.HadCurrent = true
		entries = append(entries, entry)
		if !unchanged {
			bindingConflicts = append(bindingConflicts, BindingConflict{
				BindingID: binding.ID, BindingName: binding.Name,
				Paths: []string{"<working tree changed after the local snapshot>"},
			})
			continue
		}
		conflicts, err := mergePlaintextTrees(ctx, basePath, localPath, remotePath, entry.Next)
		if err != nil {
			return fmt.Errorf("merge binding %s: %w", binding.Name, err)
		}
		if len(conflicts) != 0 {
			bindingConflicts = append(bindingConflicts, BindingConflict{
				BindingID: binding.ID, BindingName: binding.Name, Paths: conflicts,
			})
		}
	}
	if len(bindingConflicts) != 0 {
		if err := cleanupInstallStaging(entries); err != nil {
			return fmt.Errorf("clean prepared conflict installation: %w", err)
		}
		cleanupEntries = false
		checkoutPath := filepath.Join(conflictRoot, safeID(stash.ID))
		if _, err := os.Lstat(checkoutPath); err == nil {
			return fmt.Errorf("conflict checkout path already exists: %s", checkoutPath)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.Rename(checkoutTemp, checkoutPath); err != nil {
			return err
		}
		keepCheckoutTemp = true
		checkout := ConflictCheckout{
			PlanID: s.plan.ID, StashID: stash.ID, Branch: stash.Branch,
			BaseRoot: stash.Base.Root, LocalRoot: stash.CandidateRoot, RemoteRoot: remoteRoot.String(),
			Path: checkoutPath, Bindings: bindingConflicts, CreatedAt: time.Now().UTC(),
		}
		if err := s.history.SetConflictCheckout(checkout); err != nil {
			_ = os.RemoveAll(checkoutPath)
			keepCheckoutTemp = false
			return err
		}
		return &ManualMergeError{Checkout: checkout}
	}
	cleanupEntries = false
	if err := (installer{planID: s.plan.ID}).installPrepared(ctx, s.syncTransactionPath(), entries); err != nil {
		return err
	}
	if pending, err := s.history.Pending(); err != nil {
		return err
	} else if pending != nil {
		if pending.StashID != stash.ID || pending.Result.CandidateRoot != stash.CandidateRoot {
			return fmt.Errorf("pending backup journal does not match merged conflict stash")
		}
		if err := s.history.ClearPending(stash.CandidateRoot); err != nil {
			return err
		}
	}
	if _, err := s.sync.ResolveBranched(stash.ID, stash.CandidateRoot); err != nil {
		return err
	}
	return nil
}

func (s *PlanService) restoreBindingVersion(
	ctx context.Context,
	root cid.Cid,
	binding Binding,
	destination string,
	allowMissing bool,
) error {
	if !root.Defined() {
		if allowMissing {
			return os.MkdirAll(destination, 0o700)
		}
		return fmt.Errorf("binding %s snapshot root is undefined", binding.Name)
	}
	dataset, err := s.loadDataset(ctx, root)
	if allowMissing && errors.Is(err, encryptedfs.ErrNotFound) {
		return os.MkdirAll(destination, 0o700)
	}
	if err == nil {
		err = s.filesystem.RestoreBinding(ctx, dataset, binding.ID, destination, s.keyResolver())
	}
	if allowMissing && errors.Is(err, encryptedfs.ErrNotFound) {
		return os.MkdirAll(destination, 0o700)
	}
	if err != nil {
		return fmt.Errorf("restore encrypted filesystem binding %s at %s: %w", binding.Name, root, err)
	}
	return nil
}

type ManualMergeError struct {
	Checkout ConflictCheckout
}

func (e *ManualMergeError) Error() string {
	return fmt.Sprintf(
		"plan %s has plaintext conflicts checked out at %s; edit each binding's merged tree using its base/local/remote trees, then run `malt conflict resolve %s --manual`",
		e.Checkout.PlanID, e.Checkout.Path, e.Checkout.PlanID,
	)
}

func (e *ManualMergeError) Unwrap() error { return ErrBackupConflict }

type ConflictStatus struct {
	PlanID   string             `json:"plan_id"`
	PlanName string             `json:"plan_name"`
	BucketID string             `json:"bucket_id"`
	Branch   string             `json:"branch"`
	Stashes  []bucketsync.Stash `json:"stashes,omitempty"`
	Checkout *ConflictCheckout  `json:"checkout,omitempty"`
}

func (s *PlanService) ConflictStatus() (ConflictStatus, error) {
	workspace, err := s.sync.Status()
	if err != nil {
		return ConflictStatus{}, err
	}
	status := ConflictStatus{
		PlanID: s.plan.ID, PlanName: s.plan.Name, BucketID: s.plan.BucketID, Branch: s.plan.Branch,
	}
	for _, stash := range workspace.Stashes {
		if stash.Status == "branched" {
			status.Stashes = append(status.Stashes, stash)
		}
	}
	checkout, err := s.history.Conflict()
	if err != nil {
		return ConflictStatus{}, err
	}
	status.Checkout = checkout
	return status, nil
}

type ConflictResolution string

const (
	ConflictManual     ConflictResolution = "manual"
	ConflictKeepLocal  ConflictResolution = "keep-local"
	ConflictKeepRemote ConflictResolution = "keep-remote"
)

func (s *PlanService) ResolveConflict(
	ctx context.Context,
	resolution ConflictResolution,
	message string,
) (*Result, error) {
	switch resolution {
	case ConflictManual, ConflictKeepLocal, ConflictKeepRemote:
	default:
		return nil, fmt.Errorf("unsupported backup conflict resolution %q", resolution)
	}
	if resolution == ConflictManual {
		if err := s.installManualConflictCheckout(ctx); err != nil {
			return nil, err
		}
	}
	if resolution == ConflictKeepRemote {
		root, err := s.acceptedObservedRootWithLock(ctx)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		unlock, err := filelock.Acquire(s.lockPath, 10*time.Second)
		if err != nil {
			s.mu.Unlock()
			return nil, err
		}
		fingerprints, fingerprintErr := s.bindingFingerprints(ctx)
		if fingerprintErr == nil {
			fingerprintErr = s.installBindings(ctx, root, fingerprints)
		}
		_ = unlock()
		s.mu.Unlock()
		if fingerprintErr != nil {
			return nil, fingerprintErr
		}
	}
	if err := s.clearConflictState(); err != nil {
		return nil, err
	}
	if resolution == ConflictKeepRemote {
		fingerprints, err := s.bindingFingerprints(ctx)
		if err != nil {
			return nil, err
		}
		manifestFingerprint, err := s.manifestFingerprint()
		if err != nil {
			return nil, err
		}
		result := &Result{
			PlanID: s.plan.ID, PlanName: s.plan.Name, Branch: s.plan.Branch,
			Source: s.plan.ID, Profile: encryptedfs.ProfileID,
			SourceFingerprint:   combinedFingerprint(fingerprints),
			BindingFingerprints: fingerprints, ManifestFingerprint: manifestFingerprint,
			CompletedAt: time.Now().UTC(),
		}
		if err := s.history.RecordResult(s.plan.ID, *result); err != nil {
			return result, err
		}
		return result, nil
	}
	return s.Backup(ctx, message)
}

func (s *PlanService) installManualConflictCheckout(ctx context.Context) (resultErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := filelock.Acquire(s.lockPath, 10*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	checkout, err := s.history.Conflict()
	if err != nil {
		return err
	}
	if checkout == nil {
		return fmt.Errorf("plan %s has no manual conflict checkout", s.plan.Name)
	}
	conflictRoot := s.lockPath + ".conflicts"
	inside, err := pathWithin(conflictRoot, checkout.Path)
	if err != nil || !inside || filepath.Clean(checkout.Path) == filepath.Clean(conflictRoot) {
		return fmt.Errorf("unsafe backup conflict checkout path")
	}
	entries := make([]installTransactionEntry, 0, len(s.plan.Bindings))
	cleanupEntries := true
	defer func() {
		if cleanupEntries {
			resultErr = errors.Join(resultErr, cleanupInstallError(nil, entries))
		}
	}()
	for _, binding := range s.plan.Bindings {
		versions := filepath.Join(checkout.Path, safeID(binding.ID))
		localPath := filepath.Join(versions, "local")
		mergedPath := filepath.Join(versions, "merged")
		currentIsLocal, err := samePlaintextTree(ctx, binding.Source, localPath)
		if err != nil {
			return err
		}
		currentIsMerged, err := samePlaintextTree(ctx, binding.Source, mergedPath)
		if err != nil {
			return err
		}
		if currentIsMerged {
			continue
		}
		if !currentIsLocal {
			return fmt.Errorf(
				"binding %s changed after conflict checkout; preserve those edits and reconcile them into %s before retrying",
				binding.Name, mergedPath,
			)
		}
		entry, err := prepareInstallEntry(binding.Name, binding.Source, ".malt-sync-")
		if err != nil {
			return err
		}
		entry.HadCurrent = true
		entry.ExpectedFingerprint, err = FingerprintSource(ctx, binding.Source)
		if err != nil {
			return cleanupInstallError(err, []installTransactionEntry{entry})
		}
		entries = append(entries, entry)
		if err := copyPlaintextTree(ctx, mergedPath, entry.Next); err != nil {
			return fmt.Errorf("prepare resolved binding %s: %w", binding.Name, err)
		}
	}
	if len(entries) == 0 {
		return nil
	}
	cleanupEntries = false
	if err := (installer{planID: s.plan.ID}).installPrepared(ctx, s.syncTransactionPath(), entries); err != nil {
		return err
	}
	return nil
}

func (s *PlanService) clearConflictState() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := filelock.Acquire(s.lockPath, 10*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	workspace, err := s.sync.Status()
	if err != nil {
		return err
	}
	stash := firstBranchedStash(workspace)
	if stash == nil {
		return fmt.Errorf("plan %s has no unresolved conflict", s.plan.Name)
	}
	checkout, err := s.history.Conflict()
	if err != nil {
		return err
	}
	if checkout != nil {
		conflictRoot := s.lockPath + ".conflicts"
		inside, err := pathWithin(conflictRoot, checkout.Path)
		if err != nil || !inside || filepath.Clean(checkout.Path) == filepath.Clean(conflictRoot) {
			return fmt.Errorf("unsafe backup conflict checkout path")
		}
		if err := s.history.ClearConflictCheckout(stash.ID); err != nil {
			return err
		}
		if err := os.RemoveAll(checkout.Path); err != nil {
			return fmt.Errorf("conflict was resolved but checkout cleanup failed at %s: %w", checkout.Path, err)
		}
	}
	pending, err := s.history.Pending()
	if err != nil {
		return err
	}
	if pending != nil {
		if pending.StashID != stash.ID || pending.Result.CandidateRoot != stash.CandidateRoot {
			return fmt.Errorf("pending backup does not match the selected conflict")
		}
		if err := s.history.ClearPending(stash.CandidateRoot); err != nil {
			return err
		}
	}
	if _, err := s.sync.ResolveBranched(stash.ID, stash.CandidateRoot); err != nil {
		return err
	}
	return nil
}

func (s *PlanService) installBindings(ctx context.Context, root cid.Cid, expectedFingerprints map[string]string) (resultErr error) {
	journalPath := s.syncTransactionPath()
	if err := (installer{planID: s.plan.ID}).recoverInstallTransaction(journalPath); err != nil {
		return err
	}
	entries := make([]installTransactionEntry, 0, len(s.plan.Bindings))
	cleanupStaging := true
	defer func() {
		if cleanupStaging {
			resultErr = errors.Join(resultErr, cleanupInstallError(nil, entries))
		}
	}()

	dataset, err := s.loadDataset(ctx, root)
	if err != nil {
		return fmt.Errorf("load encrypted filesystem for synchronization: %w", err)
	}
	if err := validateDatasetForPlan(dataset.manifest, s.plan); err != nil {
		return err
	}
	for _, binding := range s.plan.Bindings {
		entry, err := prepareInstallEntry(binding.Name, binding.Source, ".malt-sync-")
		if err != nil {
			return err
		}
		entry.ExpectedFingerprint = expectedFingerprints[binding.ID]
		entries = append(entries, entry)
		parent, err := openInstallRoot(entry)
		if err != nil {
			return err
		}
		if err := parent.root.Mkdir(parent.next, 0o700); err != nil {
			_ = parent.Close()
			return err
		}
		nextRoot, err := parent.root.OpenRoot(parent.next)
		if err != nil {
			_ = parent.Close()
			return err
		}
		_ = parent.Close()
		restoreErr := s.filesystem.RestoreBindingRoot(ctx, dataset, binding.ID, nextRoot, s.keyResolver())
		closeErr := nextRoot.Close()
		if restoreErr != nil || closeErr != nil {
			if restoreErr == nil {
				restoreErr = closeErr
			}
			return fmt.Errorf("prepare synchronized binding %s: %w", binding.Name, restoreErr)
		}
	}

	for i := range entries {
		info, err := os.Lstat(entries[i].Destination)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fmt.Errorf("binding destination is not a safe directory: %s", entries[i].Destination)
			}
			entries[i].HadCurrent = true
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}

	cleanupStaging = false
	return (installer{planID: s.plan.ID}).installPrepared(ctx, journalPath, entries)
}

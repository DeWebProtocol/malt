package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dewebprotocol/malt-client/internal/filelock"
	encryptedfs "github.com/dewebprotocol/malt-client/unixfs/encrypted"
	cid "github.com/ipfs/go-cid"
)

// RestoreTo restores the entire remote plan into one destination. Every
// binding is placed below its encrypted manifest path name; callers cannot
// select a remote subpath.
func (s *PlanService) RestoreTo(ctx context.Context, destination string, overwrite bool) error {
	return s.restoreTo(ctx, destination, overwrite, nil)
}

// RestoreBranchTo restores a complete Bucket branch using only its encrypted
// manifest and returns the reconstructed local Plan for cross-device import.
func (s *PlanService) RestoreBranchTo(ctx context.Context, destination string, overwrite bool) (Plan, error) {
	var restored Plan
	if err := s.restoreTo(ctx, destination, overwrite, &restored); err != nil {
		return Plan{}, err
	}
	return restored, nil
}

// RecordRestoredBaseline records the exact plaintext installed by a
// branch-only restore as the reconstructed Plan's local baseline. Without this
// step, the first backup on a new device would needlessly re-encrypt and
// republish every unchanged binding and manifest.
func (s *PlanService) RecordRestoredBaseline(ctx context.Context) (*Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := filelock.Acquire(s.lockPath, 10*time.Second)
	if err != nil {
		return nil, err
	}
	defer func() { _ = unlock() }()
	if s.roots == nil {
		return nil, fmt.Errorf("backup plan trusted-root policy is not configured")
	}
	root, err := s.roots.AcceptedRoot(PlanRootAlias(s.plan.BucketID, s.plan.Branch))
	if err != nil {
		return nil, err
	}
	fingerprints, err := s.bindingFingerprints(ctx)
	if err != nil {
		return nil, err
	}
	manifestFingerprint, err := s.manifestFingerprint()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	result := &Result{
		PlanID: s.plan.ID, PlanName: s.plan.Name, Branch: s.plan.Branch,
		Source: s.plan.ID, Profile: encryptedfs.ProfileID, CandidateRoot: root.String(),
		SourceFingerprint:   combinedFingerprint(fingerprints),
		BindingFingerprints: fingerprints, ManifestFingerprint: manifestFingerprint,
		CompletedAt: now,
	}
	if err := s.history.RecordResult(s.plan.ID, *result); err != nil {
		return result, err
	}
	return result, nil
}

func (s *PlanService) restoreTo(ctx context.Context, destination string, overwrite bool, restored *Plan) (resultErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := filelock.Acquire(s.lockPath, 10*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	if err := (installer{planID: s.plan.ID}).recoverInstallTransaction(s.restoreTransactionPath()); err != nil {
		return err
	}
	root, err := s.acceptedObservedRoot(ctx, cid.Undef)
	if err != nil {
		return err
	}
	dataset, err := s.loadDataset(ctx, root)
	if err != nil {
		return fmt.Errorf("load encrypted filesystem for restore: %w", err)
	}
	manifest := planManifestFromDataset(dataset.manifest)
	if destination == "" {
		return fmt.Errorf("restore destination is empty")
	}
	destination, err = filepath.Abs(destination)
	if err != nil {
		return fmt.Errorf("resolve restore destination: %w", err)
	}
	protected := append(append([]string(nil), s.protected...), s.restoreProtected...)
	if err := validateRestoreDestination(destination, protected, s.plan.Bindings); err != nil {
		return err
	}
	entry, err := prepareInstallEntry(s.plan.Name, destination, ".malt-restore-")
	if err != nil {
		return err
	}
	cleanupStaging := true
	defer func() {
		if cleanupStaging {
			resultErr = errors.Join(resultErr, cleanupInstallError(nil, []installTransactionEntry{entry}))
		}
	}()
	if len(s.plan.Bindings) == 0 {
		if restored == nil {
			return fmt.Errorf("branch-only restore must return its reconstructed backup plan")
		}
		if err := validateDiscoveredPlanManifest(manifest, s.plan.Branch); err != nil {
			return err
		}
	} else if err := validatePlanManifest(manifest, s.plan); err != nil {
		return err
	}
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
	if err := parent.Close(); err != nil {
		_ = nextRoot.Close()
		return err
	}
	for _, binding := range manifest.Bindings {
		target := filepath.FromSlash(binding.PathName)
		if err := nextRoot.Mkdir(target, 0o700); err != nil {
			_ = nextRoot.Close()
			return fmt.Errorf("create rooted restore binding %s: %w", binding.Name, err)
		}
		bindingRoot, err := nextRoot.OpenRoot(target)
		if err != nil {
			_ = nextRoot.Close()
			return err
		}
		restoreErr := s.filesystem.RestoreBindingRoot(ctx, dataset, binding.ID, bindingRoot, s.keyResolver())
		closeErr := bindingRoot.Close()
		if restoreErr != nil || closeErr != nil {
			_ = nextRoot.Close()
			if restoreErr == nil {
				restoreErr = closeErr
			}
			return fmt.Errorf("restore binding %s: %w", binding.Name, restoreErr)
		}
	}
	if err := nextRoot.Close(); err != nil {
		return err
	}
	current := false
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("restore destination must not be a symlink")
		}
		if !overwrite {
			return fmt.Errorf("restore destination already exists: %s", destination)
		}
		current = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	cleanupStaging = false
	entry.HadCurrent = current
	if err := (installer{planID: s.plan.ID}).installPrepared(ctx, s.restoreTransactionPath(), []installTransactionEntry{entry}); err != nil {
		return err
	}
	if restored != nil {
		now := time.Now().UTC()
		restoredBindings := make([]Binding, len(manifest.Bindings))
		for i, binding := range manifest.Bindings {
			restoredBindings[i] = Binding{
				ID: binding.ID, Name: binding.Name,
				Source:   filepath.Join(destination, binding.PathName),
				PathName: binding.PathName, CreatedAt: now,
			}
		}
		*restored = Plan{
			ID: manifest.PlanID, Name: manifest.PlanName,
			BucketID: s.plan.BucketID, BucketName: s.plan.BucketName, Branch: manifest.Branch,
			Bindings: restoredBindings, Enabled: true, CreatedAt: now, UpdatedAt: now,
		}
	}
	return nil
}

func validateRestoreDestination(destination string, protected []string, bindings []Binding) error {
	if destination == "" {
		return fmt.Errorf("restore destination is empty")
	}
	destination, err := filepath.Abs(destination)
	if err != nil {
		return err
	}
	volumeRoot := filepath.VolumeName(destination) + string(filepath.Separator)
	if destination == filepath.Clean(volumeRoot) {
		return fmt.Errorf("restore destination must not be a filesystem root")
	}
	if home, err := os.UserHomeDir(); err == nil {
		overlap, err := bindingSourcesOverlap(home, destination)
		if err != nil {
			return err
		}
		if overlap && destination == filepath.Clean(home) {
			return fmt.Errorf("restore destination must not replace the user home directory")
		}
	}
	for _, candidate := range protected {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		overlap, err := bindingSourcesOverlap(destination, candidate)
		if err != nil {
			return err
		}
		if overlap {
			return fmt.Errorf("restore destination %s overlaps protected MALT runtime state %s", destination, candidate)
		}
	}
	for _, binding := range bindings {
		overlap, err := bindingSourcesOverlap(destination, binding.Source)
		if err != nil {
			return err
		}
		if overlap {
			return fmt.Errorf("restore destination %s overlaps binding %s at %s", destination, binding.Name, binding.Source)
		}
	}
	return nil
}

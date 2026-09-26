package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dewebprotocol/malt-client/bucketsync"
	"github.com/dewebprotocol/malt-client/internal/filelock"
	encryptedfs "github.com/dewebprotocol/malt-client/unixfs/encrypted"
	"github.com/dewebprotocol/malt-core/derivation"
	"github.com/dewebprotocol/malt-core/maltcid"
	cid "github.com/ipfs/go-cid"
)

func (s *PlanService) Backup(ctx context.Context, message string) (backupResult *Result, resultErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := filelock.Acquire(s.lockPath, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("lock backup plan %s: %w", s.plan.Name, err)
	}
	defer func() { _ = unlock() }()

	if err := s.filesystem.RecoverSnapshots(ctx, s.snapshotDirectory()); err != nil {
		return nil, fmt.Errorf("recover local encrypted filesystem snapshots: %w", err)
	}
	if err := (installer{planID: s.plan.ID}).recoverInstallTransaction(s.syncTransactionPath()); err != nil {
		return nil, err
	}
	if pending, err := s.history.Pending(); err != nil {
		return nil, err
	} else if pending != nil {
		return s.retryPending(ctx, *pending)
	}
	before, err := s.bindingFingerprints(ctx)
	if err != nil {
		return nil, err
	}
	last, lastManifest, err := s.lastFingerprints()
	if err != nil {
		return nil, err
	}
	manifestFingerprint, err := s.manifestFingerprint()
	if err != nil {
		return nil, err
	}
	changed := changedBindings(s.plan.Bindings, before, last)
	manifestChanged := manifestFingerprint != lastManifest
	if len(changed) == 0 && !manifestChanged {
		workspace, err := s.sync.Status()
		if err != nil {
			return nil, err
		}
		if stash := firstBranchedStash(workspace); stash != nil {
			result := &Result{
				PlanID: s.plan.ID, PlanName: s.plan.Name, Branch: s.plan.Branch,
				Source: s.plan.ID, Profile: encryptedfs.ProfileID,
				Base: stash.Base, CandidateRoot: stash.CandidateRoot,
			}
			return result, &ConflictError{Plan: s.plan.Name, Branch: stash.Branch}
		}
		now := time.Now().UTC()
		result := &Result{
			PlanID: s.plan.ID, PlanName: s.plan.Name, Branch: s.plan.Branch,
			Source: s.plan.ID, Profile: encryptedfs.ProfileID,
			SourceFingerprint: combinedFingerprint(before), BindingFingerprints: before,
			ManifestFingerprint: manifestFingerprint, Skipped: true, CompletedAt: now,
		}
		if err := s.history.RecordResult(s.plan.ID, *result); err != nil {
			return result, err
		}
		return result, nil
	}

	workspace, err := s.sync.Status()
	if err != nil {
		return nil, err
	}
	if stash := firstBranchedStash(workspace); stash != nil {
		return nil, &ConflictError{Plan: s.plan.Name, Branch: stash.Branch}
	}
	if !workspace.Initialized {
		workspace, err = s.sync.Pull(ctx)
		if err != nil {
			return nil, err
		}
	}
	if len(workspace.Stashes) != 0 {
		return nil, fmt.Errorf("%w in plan %s; resolve existing branch work first", ErrPendingWorkspace, s.plan.Name)
	}
	baseCID, err := rootCID(workspace.Base.Root)
	if err != nil {
		return nil, err
	}
	alias := PlanRootAlias(s.plan.BucketID, s.plan.Branch)
	accepted, acceptedErr := s.roots.AcceptedRoot(alias)
	acceptedBase := baseCID.Defined() && acceptedErr == nil && accepted.Equals(baseCID)
	needsUnchangedBindings := len(changed) != len(s.plan.Bindings)
	if needsUnchangedBindings && !acceptedBase {
		return nil, &UnacceptedRootError{
			Plan: s.plan.Name, Alias: alias, Observed: baseCID, Accepted: accepted, Cause: acceptedErr,
		}
	}
	backend := maltcid.BackendKindUnknown
	if baseCID.Defined() {
		descriptor, _, parseErr := maltcid.ParseRoot(baseCID)
		if parseErr != nil || descriptor.Layout != maltcid.Prefix || descriptor.DerivationProfile != uint8(derivation.SHA256) {
			return nil, fmt.Errorf("backup plan base is not a supported Prefix Root with byte-label inputs")
		}
		profile, profileErr := maltcid.Profile(descriptor.Profile)
		if profileErr != nil {
			return nil, profileErr
		}
		backend = profile.Algorithm
	} else {
		backend, err = s.filesystem.DefaultBackend(ctx)
		if err != nil {
			return nil, fmt.Errorf("select encrypted filesystem commitment backend: %w", err)
		}
		if backend != maltcid.BackendKindKZG && backend != maltcid.BackendKindIPA {
			return nil, fmt.Errorf("encrypted filesystem commitment backend %q is unsupported", backend)
		}
	}
	base, err := s.sync.CurrentBase(baseCID)
	if err != nil {
		return nil, err
	}
	var baseDataset *PlanDataset
	if needsUnchangedBindings || (!manifestChanged && acceptedBase) {
		if !baseCID.Defined() {
			return nil, fmt.Errorf("encrypted filesystem base root is required to reuse unchanged data")
		}
		baseDataset, err = s.loadDataset(ctx, baseCID)
		if err != nil {
			return nil, fmt.Errorf("load encrypted filesystem base root %s: %w", baseCID, err)
		}
		if err := validateDatasetIdentity(baseDataset.manifest, s.plan); err != nil {
			return nil, err
		}
		if err := validateDatasetForPlan(baseDataset.manifest, s.plan); err != nil {
			return nil, err
		}
	}
	epoch := s.keys.ActiveEpoch()
	bucketKey, err := s.keys.BucketKey(epoch, s.plan.BucketID)
	if err != nil {
		return nil, err
	}
	indexKey, err := s.keys.BucketKey(encryptedfs.NamespaceKeyEpoch, s.plan.BucketID)
	if err != nil {
		return nil, fmt.Errorf("load encrypted filesystem namespace key: %w", err)
	}
	snapshot, err := s.filesystem.BeginSnapshot(ctx, backend, s.snapshotDirectory())
	if err != nil {
		return nil, fmt.Errorf("begin local encrypted filesystem snapshot: %w", err)
	}
	defer func() {
		if err := snapshot.Close(); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("clean local encrypted filesystem snapshot: %w", err))
		}
	}()
	preparedByID := make(map[string]encryptedfs.PreparedBinding, len(changed))
	var encryptedBytes int64
	for _, binding := range changed {
		if err := ValidateSource(binding.Source, s.protected); err != nil {
			return nil, fmt.Errorf("binding %s: %w", binding.Name, err)
		}
		sourceRoot, displayName, err := openPinnedSource(binding.Source)
		if err != nil {
			return nil, fmt.Errorf("pin binding %s: %w", binding.Name, err)
		}
		defer sourceRoot.Close()
		pinnedBefore, err := fingerprintPinnedSource(ctx, sourceRoot, displayName)
		if err != nil {
			return nil, fmt.Errorf("fingerprint pinned binding %s: %w", binding.Name, err)
		}
		if pinnedBefore != before[binding.ID] {
			return nil, fmt.Errorf("binding %s changed before its encrypted MALT-native snapshot was pinned; retry", binding.Name)
		}
		prepared, err := snapshot.PrepareBinding(ctx, encryptedfs.BindingSource{
			DatasetID: s.plan.BucketID, DatasetName: s.plan.Name, Branch: s.plan.Branch,
			BindingID: binding.ID, BindingName: binding.Name, PathName: binding.PathName,
			Source: binding.Source, Root: sourceRoot,
			Epoch: epoch, BucketKey: bucketKey, IndexKey: indexKey,
		})
		if err != nil {
			return nil, fmt.Errorf("build local encrypted MALT-native binding %s: %w", binding.Name, err)
		}
		if prepared.SourceFingerprint != pinnedBefore {
			return nil, fmt.Errorf("binding %s changed while its bytes were encrypted; retry", binding.Name)
		}
		after, err := fingerprintPinnedSource(ctx, sourceRoot, displayName)
		if err != nil {
			return nil, err
		}
		if after != before[binding.ID] {
			return nil, fmt.Errorf("binding %s changed while its encrypted MALT-native snapshot was being created; retry", binding.Name)
		}
		preparedByID[binding.ID] = prepared
		encryptedBytes += prepared.EncryptedBytes
	}
	prepared := make([]encryptedfs.PreparedBinding, 0, len(s.plan.Bindings))
	for _, binding := range s.plan.Bindings {
		if value, ok := preparedByID[binding.ID]; ok {
			prepared = append(prepared, value)
			continue
		}
		baseBinding, ok := baseDataset.Binding(binding.ID)
		if !ok {
			return nil, fmt.Errorf("encrypted filesystem base root has no unchanged binding %q", binding.Name)
		}
		prepared = append(prepared, encryptedfs.PreparedBinding{
			Manifest: encryptedfs.BindingManifest{
				ID: binding.ID, Name: binding.Name, PathName: binding.PathName,
				Token: baseBinding.Manifest.Token,
			},
			Root: baseBinding.Root,
		})
	}
	request := PlanDatasetBuildRequest{
		Request: encryptedfs.DatasetBuildRequest{
			DatasetID: s.plan.BucketID, PlanID: s.plan.ID, DatasetName: s.plan.Name,
			Branch: s.plan.Branch, Epoch: epoch, BucketKey: bucketKey, IndexKey: indexKey,
			Bindings: prepared,
		},
	}
	if !manifestChanged && baseDataset != nil {
		request.ReuseManifest = baseDataset
	}
	built, err := snapshot.BuildDataset(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("build encrypted MALT-native dataset: %w", err)
	}
	candidate := built.Root
	encryptedBytes += built.EncryptedBytes
	if !candidate.Defined() {
		return nil, fmt.Errorf("backup plan produced no candidate root")
	}
	if err := snapshot.Publish(ctx); err != nil {
		return nil, fmt.Errorf("publish locally verified encrypted MALT-native snapshot: %w", err)
	}
	candidateBase := cid.Undef
	if acceptedErr == nil && accepted.Defined() {
		candidateBase = accepted
	}
	if err := s.roots.ObserveCandidate(alias, candidate, candidateBase, "encrypted-backup:"+s.plan.ID); err != nil {
		return nil, fmt.Errorf("record locally verified encrypted filesystem candidate %s: %w", candidate, err)
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = strings.TrimSpace(s.plan.Message)
	}
	if message == "" {
		message = "encrypted MALT-native backup"
	}
	result := &Result{
		PlanID: s.plan.ID, PlanName: s.plan.Name, Branch: s.plan.Branch,
		Source: s.plan.ID, Profile: encryptedfs.ProfileID, KeyEpoch: epoch,
		EncryptedBytes: encryptedBytes, SourceFingerprint: combinedFingerprint(before),
		BindingFingerprints: cloneFingerprints(before), ChangedBindings: bindingNames(changed),
		ManifestFingerprint: manifestFingerprint, Base: base, CandidateRoot: candidate.String(),
	}
	candidateBaseText := ""
	if candidateBase.Defined() {
		candidateBaseText = candidateBase.String()
	}
	pending := PendingBackup{
		BucketID: s.plan.BucketID, PlanID: s.plan.ID, Message: message,
		CandidateBase: candidateBaseText, CandidateRecorded: true,
		Result: *result, CreatedAt: time.Now().UTC(),
	}
	if err := s.history.SetPending(pending); err != nil {
		return result, fmt.Errorf("journal backup plan candidate %s: %w", candidate, err)
	}
	stash, err := s.sync.Stage(candidate, base, cid.Undef, message)
	if err != nil {
		return result, fmt.Errorf("stage backup plan candidate %s: %w", candidate, err)
	}
	if err := s.history.MarkPendingStaged(candidate.String(), stash); err != nil {
		return result, fmt.Errorf("freeze backup plan push %s: %w", candidate, err)
	}
	push, err := s.sync.Push(ctx, candidate, cid.Undef, message)
	if err != nil {
		return result, fmt.Errorf("push backup plan candidate %s: %w", candidate, err)
	}
	result.Push = push
	result.CompletedAt = time.Now().UTC()
	if push.Result.Status == "branched" {
		if err := s.history.MarkPendingConflict(candidate.String(), *result); err != nil {
			return result, fmt.Errorf("journal backup conflict %s: %w", candidate, err)
		}
		return result, &ConflictError{Plan: s.plan.Name, Push: push}
	}
	if err := s.history.CompletePending(candidate.String(), s.plan.ID, *result); err != nil {
		return result, fmt.Errorf("complete backup plan candidate %s: %w", candidate, err)
	}
	return result, nil
}

func (s *PlanService) retryPending(ctx context.Context, pending PendingBackup) (*Result, error) {
	if pending.Result.Profile != encryptedfs.ProfileID {
		result := pending.Result
		return &result, fmt.Errorf(
			"%w; pending backup uses unsupported profile %q",
			ErrPendingWorkspace, pending.Result.Profile,
		)
	}
	if pending.BucketID != s.plan.BucketID || pending.PlanID != s.plan.ID || pending.Result.PlanID != s.plan.ID {
		return nil, fmt.Errorf("%w; pending work belongs to another backup plan", ErrPendingWorkspace)
	}
	candidate, err := cid.Parse(pending.Result.CandidateRoot)
	if err != nil {
		return nil, err
	}
	if !pending.CandidateRecorded {
		result := pending.Result
		return &result, fmt.Errorf("%w; encrypted filesystem pending backup has no durable local-candidate record", ErrPendingWorkspace)
	}
	candidateBase, err := rootCID(pending.CandidateBase)
	if err != nil {
		return nil, fmt.Errorf("decode pending backup candidate base: %w", err)
	}
	alias := PlanRootAlias(s.plan.BucketID, s.plan.Branch)
	accepted, acceptedErr := s.roots.AcceptedRoot(alias)
	if acceptedErr != nil || !accepted.Equals(candidate) {
		found, err := s.roots.HasCandidate(alias, candidate, candidateBase)
		if err != nil {
			return nil, fmt.Errorf("validate locally verified pending candidate %s: %w", candidate, err)
		}
		if !found {
			return nil, fmt.Errorf("%w; durable trust state has no exact pending candidate %s", ErrPendingWorkspace, candidate)
		}
	}
	result := pending.Result
	result.RetriedPending = true
	workspace, err := s.sync.Status()
	if err != nil {
		return &result, err
	}
	stash, found, err := pendingStash(workspace, pending, candidate.String())
	if err != nil {
		return &result, err
	}
	if !workspace.Initialized {
		return &result, fmt.Errorf("%w; plan workspace is not initialized", ErrPendingWorkspace)
	}
	if pending.StashID != "" && !found {
		stash, err = s.sync.RestorePending(bucketsync.Stash{
			ID: pending.StashID, PushID: pending.PushID, CandidateRoot: candidate.String(),
			Base: result.Base, Message: pending.Message, RequestFrozen: true,
			Status: "pending", CreatedAt: pending.CreatedAt,
		})
		if err != nil {
			return &result, err
		}
		found = true
	}
	if found && stash.Status == "branched" {
		result.ReconciledPending = true
		if result.Push.Result.Status != "branched" {
			result.Push.Workspace = workspace
		}
		return &result, &ConflictError{Plan: s.plan.Name, Branch: stash.Branch, Push: result.Push}
	}
	if !found {
		stash, err = s.sync.Stage(candidate, result.Base, cid.Undef, pending.Message)
		if err != nil {
			return &result, err
		}
	}
	if err := s.history.MarkPendingStaged(candidate.String(), stash); err != nil {
		return &result, err
	}
	push, err := s.sync.Push(ctx, candidate, cid.Undef, pending.Message)
	if err != nil {
		return &result, err
	}
	result.Push = push
	result.CompletedAt = time.Now().UTC()
	if push.Result.Status == "branched" {
		if err := s.history.MarkPendingConflict(candidate.String(), result); err != nil {
			return &result, err
		}
		return &result, &ConflictError{Plan: s.plan.Name, Push: push}
	}
	if err := s.history.CompletePending(candidate.String(), s.plan.ID, result); err != nil {
		return &result, err
	}
	return &result, nil
}

func (s *PlanService) bindingFingerprints(ctx context.Context) (map[string]string, error) {
	result := make(map[string]string, len(s.plan.Bindings))
	for _, binding := range s.plan.Bindings {
		if err := ValidateSource(binding.Source, s.protected); err != nil {
			return nil, fmt.Errorf("binding %s: %w", binding.Name, err)
		}
		fingerprint, err := FingerprintSource(ctx, binding.Source)
		if err != nil {
			return nil, fmt.Errorf("fingerprint binding %s: %w", binding.Name, err)
		}
		result[binding.ID] = fingerprint
	}
	return result, nil
}

func (s *PlanService) lastFingerprints() (map[string]string, string, error) {
	states, err := s.history.Snapshot()
	if err != nil {
		return nil, "", err
	}
	state := states[s.plan.ID]
	if state.LastResult == nil {
		return map[string]string{}, "", nil
	}
	if state.LastResult.Profile != encryptedfs.ProfileID {
		return nil, "", fmt.Errorf("backup history uses unsupported profile %q", state.LastResult.Profile)
	}
	return cloneFingerprints(state.LastResult.BindingFingerprints), state.LastResult.ManifestFingerprint, nil
}

func rootCID(raw string) (cid.Cid, error) {
	if strings.TrimSpace(raw) == "" {
		return cid.Undef, nil
	}
	value, err := cid.Parse(raw)
	if err != nil {
		return cid.Undef, fmt.Errorf("decode backup base root: %w", err)
	}
	return value, nil
}

func changedBindings(bindings []Binding, current, previous map[string]string) []Binding {
	result := make([]Binding, 0, len(bindings))
	for _, binding := range bindings {
		if current[binding.ID] != previous[binding.ID] {
			result = append(result, binding)
		}
	}
	return result
}

func bindingNames(values []Binding) []string {
	result := make([]string, len(values))
	for i := range values {
		result[i] = values[i].Name
	}
	sort.Strings(result)
	return result
}

func combinedFingerprint(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	hash := sha256.New()
	for _, key := range keys {
		_, _ = hash.Write([]byte(key))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(values[key]))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func cloneFingerprints(values map[string]string) map[string]string {
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func safeID(value string) string {
	return strings.NewReplacer("/", "_", "\\", "_", ":", "_").Replace(value)
}

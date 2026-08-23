package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dewebprotocol/malt-client/bucketsync"
	"github.com/dewebprotocol/malt-client/internal/durablefile"
	"github.com/dewebprotocol/malt-client/internal/filelock"
	"github.com/dewebprotocol/malt-client/internal/securefile"
	encryptedfs "github.com/dewebprotocol/malt-client/unixfs/encrypted"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

const installTransactionVersion = 2

type PlanRootPolicy interface {
	AcceptedRoot(alias string) (cid.Cid, error)
	ObserveCandidate(alias string, candidateRoot, baseRoot cid.Cid, source string) error
}

type planHeadObserver interface {
	ObserveHead(alias, source, datasetID, branch, commitID string, root cid.Cid, revision uint64) error
}

type planCandidateInspector interface {
	HasCandidate(alias string, candidateRoot, baseRoot cid.Cid) (bool, error)
}

type PlanServiceOptions struct {
	Plan             Plan
	LockPath         string
	Keys             KeySource
	Sync             Sync
	Filesystem       PlanFilesystem
	History          *History
	Roots            PlanRootPolicy
	Protected        []string
	RestoreProtected []string
}

// PlanService publishes and synchronizes one Bucket branch. Different
// bindings occupy different opaque MALT Map tokens, allowing the Gateway to
// merge independent binding updates without learning their path names while
// same-binding changes conflict.
type PlanService struct {
	mu               sync.Mutex
	plan             Plan
	lockPath         string
	keys             KeySource
	sync             Sync
	filesystem       PlanFilesystem
	history          *History
	roots            PlanRootPolicy
	protected        []string
	restoreProtected []string
	release          func() error
}

func NewPlanService(opts PlanServiceOptions) (*PlanService, error) {
	return newPlanService(opts, nil)
}

// NewPlanServiceWithRelease composes one plan service with an owned runtime
// resource release and gives the local runtime deterministic transport cleanup.
// The encrypted-filesystem migration intentionally changes the pre-release
// PlanServiceOptions capability set.
func NewPlanServiceWithRelease(opts PlanServiceOptions, release func() error) (*PlanService, error) {
	return newPlanService(opts, release)
}

func newPlanService(opts PlanServiceOptions, release func() error) (*PlanService, error) {
	if err := validatePlan(opts.Plan); err != nil {
		return nil, err
	}
	if opts.LockPath == "" {
		return nil, fmt.Errorf("backup plan lock is required")
	}
	if opts.Keys == nil || opts.Sync == nil || opts.Filesystem == nil || opts.History == nil || opts.Roots == nil {
		return nil, fmt.Errorf("backup plan keys, synchronization, encrypted filesystem, history, and trusted-root policy are required")
	}
	return &PlanService{
		plan: clonePlan(opts.Plan), lockPath: opts.LockPath,
		keys: opts.Keys, sync: opts.Sync, filesystem: opts.Filesystem, history: opts.History,
		roots:            opts.Roots,
		protected:        append([]string(nil), opts.Protected...),
		restoreProtected: append([]string(nil), opts.RestoreProtected...),
		release:          release,
	}, nil
}

// Close releases transport resources owned by this configured service after
// all plan operations have quiesced. It is idempotent.
func (s *PlanService) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release == nil {
		return nil
	}
	if err := s.release(); err != nil {
		return err
	}
	s.release = nil
	return nil
}

// Recover completes or rolls back interrupted sync/restore transactions before
// the daemon begins accepting new work.
func (s *PlanService) Recover() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := filelock.Acquire(s.lockPath, 10*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	if err := s.recoverInstallTransaction(s.syncTransactionPath()); err != nil {
		return err
	}
	return s.recoverInstallTransaction(s.restoreTransactionPath())
}

// RecoverPlanTransactions performs startup recovery without constructing
// network, key, or materialization dependencies. Recovery needs only the
// authenticated local journal path and plan identity.
func RecoverPlanTransactions(plan Plan, lockPath string) error {
	if strings.TrimSpace(plan.ID) == "" || lockPath == "" {
		return fmt.Errorf("backup plan ID and operation lock path are required for recovery")
	}
	service := &PlanService{plan: clonePlan(plan), lockPath: lockPath}
	return service.Recover()
}

// RecoverTransactionJournals scans the owner-only Plan history directory so
// branch-only restores that crashed before Plan registration are recovered as
// well as already registered Plans.
func RecoverTransactionJournals(directory string) error {
	if directory == "" {
		return fmt.Errorf("backup transaction journal directory is empty")
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".operation.lock.sync-transaction.json") ||
			strings.HasSuffix(name, ".operation.lock.restore-transaction.json") {
			paths = append(paths, filepath.Join(directory, name))
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := recoverTransactionJournal(path); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

func recoverTransactionJournal(journalPath string) error {
	suffix := ""
	for _, candidate := range []string{".sync-transaction.json", ".restore-transaction.json"} {
		if strings.HasSuffix(journalPath, candidate) {
			suffix = candidate
			break
		}
	}
	if suffix == "" {
		return fmt.Errorf("unrecognized backup transaction journal")
	}
	transaction, found, err := readInstallTransaction(journalPath)
	if err != nil || !found {
		return err
	}
	lockPath := strings.TrimSuffix(journalPath, suffix)
	service := &PlanService{plan: Plan{ID: transaction.PlanID}, lockPath: lockPath}
	service.mu.Lock()
	defer service.mu.Unlock()
	unlock, err := filelock.Acquire(lockPath, 10*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	return service.recoverInstallTransaction(journalPath)
}

// PlanRootAlias is the deterministic cross-device trust-store alias for one
// complete Bucket branch. A Plan is unique per Bucket and writable branch.
func PlanRootAlias(bucketID, branch string) string {
	return "backup:" + strings.TrimSpace(bucketID) + ":" + strings.TrimSpace(branch)
}

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
	if err := s.recoverInstallTransaction(s.syncTransactionPath()); err != nil {
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
		backend = maltcid.BackendKindOf(baseCID)
		if maltcid.SemanticKindOf(baseCID) != maltcid.SemanticKindMap ||
			(backend != maltcid.BackendKindKZG && backend != maltcid.BackendKindIPA) {
			return nil, fmt.Errorf("backup plan base is not a supported typed MALT Map")
		}
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

func (s *PlanService) retryPending(ctx context.Context, pending PendingBackup) (*Result, error) {
	if pending.Result.Profile != encryptedfs.ProfileID {
		result := pending.Result
		return &result, fmt.Errorf(
			"%w; pending backup uses removed profile %q; use the previous MALT runtime to complete or discard that pending publication before upgrading",
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
		inspector, ok := s.roots.(planCandidateInspector)
		if !ok {
			return nil, fmt.Errorf("%w; trusted-root policy cannot validate pending candidate provenance", ErrPendingWorkspace)
		}
		found, err := inspector.HasCandidate(alias, candidate, candidateBase)
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
	if err := s.installPrepared(ctx, s.syncTransactionPath(), entries); err != nil {
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
	if err := s.installPrepared(ctx, s.syncTransactionPath(), entries); err != nil {
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
	if err := s.recoverInstallTransaction(s.restoreTransactionPath()); err != nil {
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
	if err := s.installPrepared(ctx, s.restoreTransactionPath(), []installTransactionEntry{entry}); err != nil {
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
	observer, ok := s.roots.(planHeadObserver)
	if !ok {
		return cid.Undef, fmt.Errorf("backup plan trusted-root policy does not support remote head observations")
	}
	if err := observer.ObserveHead(
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
	if state.LastResult == nil || state.LastResult.Profile != encryptedfs.ProfileID {
		return map[string]string{}, "", nil
	}
	return cloneFingerprints(state.LastResult.BindingFingerprints), state.LastResult.ManifestFingerprint, nil
}

const (
	installStatePrepared  = "prepared"
	installStateCommitted = "committed"

	installPhasePrepared  = "prepared"
	installPhasePreserved = "preserved"
	installPhaseInstalled = "installed"
)

type installTransaction struct {
	Version int                       `json:"version"`
	PlanID  string                    `json:"plan_id"`
	State   string                    `json:"state"`
	Entries []installTransactionEntry `json:"entries"`
}

type installTransactionEntry struct {
	Name                 string `json:"name"`
	Destination          string `json:"destination"`
	Staging              string `json:"staging"`
	Next                 string `json:"next"`
	Rollback             string `json:"rollback"`
	ParentPin            string `json:"parent_pin"`
	ParentToken          string `json:"parent_token"`
	ExpectedFingerprint  string `json:"expected_fingerprint,omitempty"`
	OriginalFingerprint  string `json:"original_fingerprint,omitempty"`
	InstalledFingerprint string `json:"installed_fingerprint"`
	HadCurrent           bool   `json:"had_current"`
	Phase                string `json:"phase"`
}

type installRoot struct {
	root        *os.Root
	destination string
	staging     string
	next        string
	rollback    string
	quarantine  string
}

func prepareInstallEntry(name, destination, stagingPrefix string) (installTransactionEntry, error) {
	if destination == "" {
		return installTransactionEntry{}, fmt.Errorf("filesystem installation destination is empty")
	}
	destination, err := filepath.Abs(destination)
	if err != nil {
		return installTransactionEntry{}, fmt.Errorf("resolve filesystem installation destination: %w", err)
	}
	parent := filepath.Dir(destination)
	if err := mkdirAllDurable(parent, 0o700); err != nil {
		return installTransactionEntry{}, err
	}
	root, _, err := openPinnedSource(parent)
	if err != nil {
		return installTransactionEntry{}, fmt.Errorf("pin filesystem installation parent: %w", err)
	}
	defer root.Close()
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return installTransactionEntry{}, err
	}
	id := hex.EncodeToString(random)
	stagingName := stagingPrefix + id[:24]
	parentPin := ".malt-install-pin-" + id[24:]
	if err := root.Mkdir(stagingName, 0o700); err != nil {
		return installTransactionEntry{}, err
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		_ = root.RemoveAll(stagingName)
		return installTransactionEntry{}, err
	}
	token := hex.EncodeToString(tokenBytes)
	pin, err := root.OpenFile(parentPin, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = root.RemoveAll(stagingName)
		return installTransactionEntry{}, err
	}
	_, writeErr := pin.Write([]byte(token))
	syncErr := pin.Sync()
	closeErr := pin.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = root.Remove(parentPin)
		_ = root.RemoveAll(stagingName)
		return installTransactionEntry{}, err
	}
	if err := syncRoot(root); err != nil {
		_ = root.Remove(parentPin)
		_ = root.RemoveAll(stagingName)
		return installTransactionEntry{}, err
	}
	staging := filepath.Join(parent, stagingName)
	return installTransactionEntry{
		Name: name, Destination: destination, Staging: staging,
		Next: filepath.Join(staging, "next"), Rollback: filepath.Join(staging, "previous"),
		ParentPin: parentPin, ParentToken: token, Phase: installPhasePrepared,
	}, nil
}

// mkdirAllDurable creates a directory chain and persists each newly reachable
// directory plus its parent entry before installation state is journaled.
func mkdirAllDurable(path string, perm os.FileMode) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	missing := make([]string, 0)
	current := abs
	for {
		info, statErr := os.Stat(current)
		if statErr == nil {
			if !info.IsDir() {
				return fmt.Errorf("filesystem installation parent is not a directory: %s", current)
			}
			break
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return statErr
		}
		current = parent
	}
	if len(missing) == 0 {
		return nil
	}
	if err := os.MkdirAll(abs, perm); err != nil {
		return err
	}
	for _, directory := range missing {
		if err := durablefile.SyncParent(filepath.Join(directory, ".malt-directory-sync")); err != nil {
			return fmt.Errorf("persist created filesystem directory %s: %w", directory, err)
		}
		if err := durablefile.SyncParent(directory); err != nil {
			return fmt.Errorf("persist created filesystem directory entry %s: %w", directory, err)
		}
	}
	return nil
}

func openInstallRoot(entry installTransactionEntry) (*installRoot, error) {
	parent := filepath.Dir(entry.Destination)
	root, _, err := openPinnedSource(parent)
	if err != nil {
		return nil, err
	}
	closeWith := func(err error) (*installRoot, error) {
		_ = root.Close()
		return nil, err
	}
	pin, err := root.ReadFile(entry.ParentPin)
	if err != nil {
		return closeWith(fmt.Errorf("filesystem installation parent pin is unavailable: %w", err))
	}
	if string(pin) != entry.ParentToken {
		return closeWith(fmt.Errorf("filesystem installation parent identity changed"))
	}
	staging := filepath.Base(entry.Staging)
	return &installRoot{
		root: root, destination: filepath.Base(entry.Destination), staging: staging,
		next: filepath.Join(staging, "next"), rollback: filepath.Join(staging, "previous"),
		quarantine: staging + "-recovery",
	}, nil
}

func (r *installRoot) Close() error {
	if r == nil || r.root == nil {
		return nil
	}
	return r.root.Close()
}

func syncRoot(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(syncErr, closeErr)
}

// syncInstallTree persists a prepared installation subtree bottom-up. Symlink
// entries have no independently fsync-able handle; their containing directory
// is synced after every child has been visited.
func syncInstallTree(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if info.IsDir() {
		childRoot, err := root.OpenRoot(name)
		if err != nil {
			return err
		}
		opened, err := childRoot.Open(".")
		if err != nil {
			_ = childRoot.Close()
			return err
		}
		openedInfo, statErr := opened.Stat()
		if statErr == nil && (!openedInfo.IsDir() || !os.SameFile(info, openedInfo)) {
			statErr = fmt.Errorf("prepared installation directory changed before it was persisted: %s", name)
		}
		entries, readErr := opened.ReadDir(-1)
		closeErr := opened.Close()
		if err := errors.Join(statErr, readErr, closeErr); err != nil {
			_ = childRoot.Close()
			return err
		}
		for _, entry := range entries {
			if err := syncInstallTree(childRoot, entry.Name()); err != nil {
				_ = childRoot.Close()
				return err
			}
		}
		syncErr := syncRoot(childRoot)
		closeRootErr := childRoot.Close()
		return errors.Join(syncErr, closeRootErr)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("prepared installation contains unsupported file type: %s", name)
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	openedInfo, statErr := file.Stat()
	if statErr == nil && (!openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo)) {
		statErr = fmt.Errorf("prepared installation file changed before it was persisted: %s", name)
	}
	var syncErr error
	if statErr == nil {
		syncErr = file.Sync()
	}
	closeErr := file.Close()
	return errors.Join(statErr, syncErr, closeErr)
}

func (s *PlanService) syncTransactionPath() string {
	return s.lockPath + ".sync-transaction.json"
}

func (s *PlanService) snapshotDirectory() string {
	return s.lockPath + ".encrypted-snapshots"
}

func (s *PlanService) restoreTransactionPath() string {
	return s.lockPath + ".restore-transaction.json"
}

func (s *PlanService) installBindings(ctx context.Context, root cid.Cid, expectedFingerprints map[string]string) (resultErr error) {
	journalPath := s.syncTransactionPath()
	if err := s.recoverInstallTransaction(journalPath); err != nil {
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
	return s.installPrepared(ctx, journalPath, entries)
}

func (s *PlanService) installPrepared(ctx context.Context, journalPath string, entries []installTransactionEntry) error {
	failBeforeJournal := func(operationErr error) error {
		return cleanupInstallError(operationErr, entries)
	}
	for i := range entries {
		parent, err := openInstallRoot(entries[i])
		if err != nil {
			return failBeforeJournal(fmt.Errorf("pin prepared installation %s: %w", entries[i].Name, err))
		}
		if entries[i].HadCurrent {
			fingerprint, err := plaintextContentFingerprintRoot(ctx, parent.root, parent.destination)
			if err != nil {
				_ = parent.Close()
				return failBeforeJournal(fmt.Errorf("fingerprint original %s: %w", entries[i].Name, err))
			}
			entries[i].OriginalFingerprint = fingerprint
		}
		fingerprint, err := plaintextContentFingerprintRoot(ctx, parent.root, parent.next)
		if err != nil {
			_ = parent.Close()
			return failBeforeJournal(fmt.Errorf("fingerprint prepared %s: %w", entries[i].Name, err))
		}
		entries[i].InstalledFingerprint = fingerprint
		if err := syncInstallTree(parent.root, parent.staging); err != nil {
			_ = parent.Close()
			return failBeforeJournal(fmt.Errorf("persist prepared %s: %w", entries[i].Name, err))
		}
		if err := syncRoot(parent.root); err != nil {
			_ = parent.Close()
			return failBeforeJournal(fmt.Errorf("persist prepared installation parent for %s: %w", entries[i].Name, err))
		}
		if err := parent.Close(); err != nil {
			return failBeforeJournal(err)
		}
	}
	transaction := installTransaction{
		Version: installTransactionVersion, PlanID: s.plan.ID,
		State: installStatePrepared, Entries: append([]installTransactionEntry(nil), entries...),
	}
	if err := validateInstallTransaction(transaction); err != nil {
		return failBeforeJournal(err)
	}
	if err := writeInstallTransaction(journalPath, transaction); err != nil {
		return failBeforeJournal(fmt.Errorf("journal prepared filesystem installation: %w", err))
	}
	fail := func(operationErr error) error {
		recoveryErr := s.recoverInstallTransaction(journalPath)
		if recoveryErr != nil {
			return fmt.Errorf("%v; automatic rollback also failed: %w", operationErr, recoveryErr)
		}
		return operationErr
	}
	for i := range transaction.Entries {
		entry := &transaction.Entries[i]
		parent, err := openInstallRoot(*entry)
		if err != nil {
			return fail(fmt.Errorf("pin installation parent for %s: %w", entry.Name, err))
		}
		if entry.HadCurrent {
			if entry.ExpectedFingerprint != "" {
				actual, err := fingerprintRootedDirectory(ctx, parent.root, parent.destination, filepath.Base(entry.Destination))
				if err != nil {
					_ = parent.Close()
					return fail(fmt.Errorf("recheck %s before installation: %w", entry.Name, err))
				}
				if actual != entry.ExpectedFingerprint {
					_ = parent.Close()
					return fail(fmt.Errorf("%s changed after backup; its synchronized snapshot was not installed", entry.Name))
				}
			}
			currentFingerprint, err := plaintextContentFingerprintRoot(ctx, parent.root, parent.destination)
			if err != nil {
				_ = parent.Close()
				return fail(fmt.Errorf("fingerprint %s before installation: %w", entry.Name, err))
			}
			if currentFingerprint != entry.OriginalFingerprint {
				_ = parent.Close()
				return fail(fmt.Errorf("%s changed while synchronization was being prepared; its snapshot was not installed", entry.Name))
			}
			info, err := parent.root.Lstat(parent.destination)
			if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				_ = parent.Close()
				return fail(fmt.Errorf("binding destination changed during installation: %s", entry.Destination))
			}
			if err := parent.root.Rename(parent.destination, parent.rollback); err != nil {
				_ = parent.Close()
				return fail(fmt.Errorf("preserve %s before installation: %w", entry.Name, err))
			}
			if err := syncRoot(parent.root); err != nil {
				_ = parent.Close()
				return fail(fmt.Errorf("persist preserved %s: %w", entry.Name, err))
			}
			entry.Phase = installPhasePreserved
			if err := writeInstallTransaction(journalPath, transaction); err != nil {
				_ = parent.Close()
				return fail(fmt.Errorf("journal preserved %s: %w", entry.Name, err))
			}
		} else if _, err := parent.root.Lstat(parent.destination); !errors.Is(err, os.ErrNotExist) {
			_ = parent.Close()
			return fail(fmt.Errorf("binding destination appeared during installation: %s", entry.Destination))
		}
		if err := parent.root.Rename(parent.next, parent.destination); err != nil {
			_ = parent.Close()
			return fail(fmt.Errorf("install %s: %w", entry.Name, err))
		}
		if err := syncRoot(parent.root); err != nil {
			_ = parent.Close()
			return fail(fmt.Errorf("persist installed %s: %w", entry.Name, err))
		}
		if err := parent.Close(); err != nil {
			return fail(fmt.Errorf("close installation parent for %s: %w", entry.Name, err))
		}
		entry.Phase = installPhaseInstalled
		if err := writeInstallTransaction(journalPath, transaction); err != nil {
			return fail(fmt.Errorf("journal installed %s: %w", entry.Name, err))
		}
	}
	transaction.State = installStateCommitted
	if err := writeInstallTransaction(journalPath, transaction); err != nil {
		return fail(fmt.Errorf("commit filesystem installation journal: %w", err))
	}
	if err := finalizeCommittedInstall(journalPath, transaction); err != nil {
		return fmt.Errorf("finalize committed filesystem installation: %w", err)
	}
	return nil
}

func (s *PlanService) recoverInstallTransaction(journalPath string) error {
	transaction, found, err := readInstallTransaction(journalPath)
	if err != nil || !found {
		return err
	}
	if transaction.PlanID != s.plan.ID {
		return fmt.Errorf("filesystem installation journal belongs to another backup plan")
	}
	if transaction.Version != installTransactionVersion {
		return fmt.Errorf("filesystem installation journal uses legacy path-based format v%d; recover it with the previous MALT runtime before upgrading", transaction.Version)
	}
	if err := validateInstallTransaction(transaction); err != nil {
		return fmt.Errorf("unsafe filesystem installation journal: %w", err)
	}
	if transaction.State == installStateCommitted {
		return finalizeCommittedInstall(journalPath, transaction)
	}
	var quarantined []string
	for i := len(transaction.Entries) - 1; i >= 0; i-- {
		entry := transaction.Entries[i]
		parent, err := openInstallRoot(entry)
		if err != nil {
			return fmt.Errorf("pin recovery parent for %s: %w", entry.Name, err)
		}
		destinationExists, err := pathExistsRoot(parent.root, parent.destination)
		if err != nil {
			_ = parent.Close()
			return err
		}
		rollbackExists, err := pathExistsRoot(parent.root, parent.rollback)
		if err != nil {
			_ = parent.Close()
			return err
		}
		nextExists, err := pathExistsRoot(parent.root, parent.next)
		if err != nil {
			_ = parent.Close()
			return err
		}
		quarantinePath := entry.Staging + "-recovery"
		quarantineExists, err := pathExistsRoot(parent.root, parent.quarantine)
		if err != nil {
			_ = parent.Close()
			return err
		}
		if entry.HadCurrent {
			if rollbackExists {
				if quarantineExists {
					quarantined = append(quarantined, quarantinePath)
				}
				if destinationExists {
					quarantine, err := preserveChangedInstallation(entry, quarantinePath, parent)
					if err != nil {
						_ = parent.Close()
						return err
					}
					if quarantine != "" {
						quarantined = append(quarantined, quarantine)
					}
				}
				if err := parent.root.Rename(parent.rollback, parent.destination); err != nil {
					_ = parent.Close()
					return fmt.Errorf("restore preserved %s: %w", entry.Name, err)
				}
				if err := syncRoot(parent.root); err != nil {
					_ = parent.Close()
					return err
				}
				_ = parent.Close()
				continue
			}
			if destinationExists {
				fingerprint, err := plaintextContentFingerprintRoot(context.Background(), parent.root, parent.destination)
				if err != nil {
					_ = parent.Close()
					return err
				}
				if fingerprint == entry.OriginalFingerprint {
					if quarantineExists {
						quarantined = append(quarantined, quarantinePath)
					}
					_ = parent.Close()
					continue
				}
			}
			if quarantineExists {
				_ = parent.Close()
				return fmt.Errorf("cannot safely recover original %s while recovery quarantine exists at %s", entry.Name, quarantinePath)
			}
			if !destinationExists || !nextExists || entry.Phase != installPhasePrepared {
				_ = parent.Close()
				return fmt.Errorf("cannot safely recover original %s from interrupted installation", entry.Name)
			}
			_ = parent.Close()
			continue
		}
		if destinationExists {
			if nextExists && entry.Phase == installPhasePrepared {
				_ = parent.Close()
				return fmt.Errorf("cannot remove unexpected destination while recovering %s", entry.Name)
			}
			quarantine, err := preserveChangedInstallation(entry, quarantinePath, parent)
			if err != nil {
				_ = parent.Close()
				return err
			}
			if quarantine != "" {
				quarantined = append(quarantined, quarantine)
			}
		} else if quarantineExists {
			quarantined = append(quarantined, quarantinePath)
		}
		_ = parent.Close()
	}
	if err := removeInstallStaging(transaction.Entries); err != nil {
		return err
	}
	if err := removeInstallTransaction(journalPath); err != nil {
		return err
	}
	if err := removeInstallPins(transaction.Entries); err != nil {
		return err
	}
	if len(quarantined) != 0 {
		sort.Strings(quarantined)
		return &RecoveryQuarantineError{Paths: quarantined}
	}
	return nil
}

func preserveChangedInstallation(entry installTransactionEntry, quarantinePath string, parent *installRoot) (string, error) {
	fingerprint, err := plaintextContentFingerprintRoot(context.Background(), parent.root, parent.destination)
	if err != nil {
		return "", err
	}
	if fingerprint == entry.InstalledFingerprint {
		if err := parent.root.RemoveAll(parent.destination); err != nil {
			return "", fmt.Errorf("remove interrupted installation %s: %w", entry.Name, err)
		}
		return "", syncRoot(parent.root)
	}
	if _, err := parent.root.Lstat(parent.quarantine); err == nil {
		return "", fmt.Errorf("recovery quarantine already exists for %s: %s", entry.Name, quarantinePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := parent.root.Rename(parent.destination, parent.quarantine); err != nil {
		return "", fmt.Errorf("quarantine edits made after interrupted installation of %s: %w", entry.Name, err)
	}
	if err := syncRoot(parent.root); err != nil {
		return "", err
	}
	return quarantinePath, nil
}

type RecoveryQuarantineError struct {
	Paths []string
}

func (e *RecoveryQuarantineError) Error() string {
	return fmt.Sprintf(
		"edits made after an interrupted sync were preserved for manual recovery at %s",
		strings.Join(e.Paths, ", "),
	)
}

func validateInstallTransaction(transaction installTransaction) error {
	if transaction.Version != installTransactionVersion || strings.TrimSpace(transaction.PlanID) == "" ||
		(transaction.State != installStatePrepared && transaction.State != installStateCommitted) ||
		len(transaction.Entries) == 0 {
		return fmt.Errorf("filesystem installation journal is incomplete")
	}
	destinations := map[string]struct{}{}
	stagingPaths := map[string]struct{}{}
	for _, entry := range transaction.Entries {
		if strings.TrimSpace(entry.Name) == "" || !filepath.IsAbs(entry.Destination) || !filepath.IsAbs(entry.Staging) ||
			entry.Next != filepath.Join(entry.Staging, "next") || entry.Rollback != filepath.Join(entry.Staging, "previous") ||
			filepath.Dir(entry.Destination) != filepath.Dir(entry.Staging) ||
			filepath.Base(entry.Destination) == "." || filepath.Base(entry.Destination) == string(filepath.Separator) ||
			filepath.Base(entry.ParentPin) != entry.ParentPin || !strings.HasPrefix(entry.ParentPin, ".malt-install-pin-") ||
			len(entry.ParentToken) != 64 || strings.Trim(entry.ParentToken, "0123456789abcdef") != "" ||
			(!strings.HasPrefix(filepath.Base(entry.Staging), ".malt-sync-") &&
				!strings.HasPrefix(filepath.Base(entry.Staging), ".malt-restore-")) ||
			(entry.Phase != installPhasePrepared && entry.Phase != installPhasePreserved && entry.Phase != installPhaseInstalled) {
			return fmt.Errorf("filesystem installation entry %q is invalid", entry.Name)
		}
		if entry.InstalledFingerprint == "" || (entry.HadCurrent && entry.OriginalFingerprint == "") ||
			(!entry.HadCurrent && entry.OriginalFingerprint != "") {
			return fmt.Errorf("filesystem installation entry %q lacks recovery fingerprints", entry.Name)
		}
		if _, ok := destinations[entry.Destination]; ok {
			return fmt.Errorf("duplicate filesystem installation destination")
		}
		if _, ok := stagingPaths[entry.Staging]; ok {
			return fmt.Errorf("duplicate filesystem installation staging path")
		}
		destinations[entry.Destination] = struct{}{}
		stagingPaths[entry.Staging] = struct{}{}
	}
	return nil
}

func writeInstallTransaction(path string, transaction installTransaction) error {
	if err := validateInstallTransaction(transaction); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(transaction, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.CreateTemp(filepath.Dir(path), ".install-transaction-*.json")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := securefile.Secure(name); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return durablefile.SyncParent(path)
}

func readInstallTransaction(path string) (installTransaction, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return installTransaction{}, false, nil
	}
	if err != nil {
		return installTransaction{}, false, err
	}
	if err := securefile.Secure(path); err != nil {
		return installTransaction{}, false, err
	}
	var transaction installTransaction
	if err := json.Unmarshal(data, &transaction); err != nil {
		return installTransaction{}, false, fmt.Errorf("decode filesystem installation journal: %w", err)
	}
	return transaction, true, nil
}

func finalizeCommittedInstall(journalPath string, transaction installTransaction) error {
	for _, entry := range transaction.Entries {
		parent, err := openInstallRoot(entry)
		if err != nil {
			return fmt.Errorf("pin committed installation parent for %s: %w", entry.Name, err)
		}
		info, err := parent.root.Lstat(parent.destination)
		closeErr := parent.Close()
		if err != nil || closeErr != nil {
			return fmt.Errorf("committed installation destination is unavailable: %s: %w", entry.Destination, errors.Join(err, closeErr))
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("committed installation destination is unavailable: %s", entry.Destination)
		}
	}
	if err := removeInstallStaging(transaction.Entries); err != nil {
		return err
	}
	if err := removeInstallTransaction(journalPath); err != nil {
		return err
	}
	return removeInstallPins(transaction.Entries)
}

func removeInstallTransaction(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return durablefile.SyncParent(path)
}

func cleanupInstallStaging(entries []installTransactionEntry) error {
	if err := removeInstallStaging(entries); err != nil {
		return err
	}
	return removeInstallPins(entries)
}

func cleanupInstallError(operationErr error, entries []installTransactionEntry) error {
	cleanupErr := cleanupInstallStaging(entries)
	if cleanupErr == nil {
		return operationErr
	}
	cleanupErr = fmt.Errorf("clean prepared filesystem installation: %w", cleanupErr)
	return errors.Join(operationErr, cleanupErr)
}

func removeInstallStaging(entries []installTransactionEntry) error {
	for _, entry := range entries {
		parent, err := openPinnedInstallParent(entry)
		if err != nil {
			return fmt.Errorf("pin installation cleanup parent for %s: %w", entry.Name, err)
		}
		removeErr := parent.RemoveAll(filepath.Base(entry.Staging))
		var syncErr error
		if removeErr == nil {
			syncErr = syncRoot(parent)
		}
		closeErr := parent.Close()
		if err := errors.Join(removeErr, syncErr, closeErr); err != nil {
			return fmt.Errorf("remove installation staging for %s: %w", entry.Name, err)
		}
	}
	return nil
}

func removeInstallPins(entries []installTransactionEntry) error {
	for _, entry := range entries {
		parent, err := openPinnedInstallParent(entry)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("pin installation marker parent for %s: %w", entry.Name, err)
		}
		removeErr := parent.Remove(entry.ParentPin)
		var syncErr error
		if removeErr == nil {
			syncErr = syncRoot(parent)
		}
		closeErr := parent.Close()
		if err := errors.Join(removeErr, syncErr, closeErr); err != nil {
			return fmt.Errorf("remove installation parent marker for %s: %w", entry.Name, err)
		}
	}
	return nil
}

func openPinnedInstallParent(entry installTransactionEntry) (*os.Root, error) {
	root, _, err := openPinnedSource(filepath.Dir(entry.Destination))
	if err != nil {
		return nil, err
	}
	pin, err := root.ReadFile(entry.ParentPin)
	if err != nil || string(pin) != entry.ParentToken {
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("filesystem installation parent identity changed")
	}
	return root, nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func pathExistsRoot(root *os.Root, path string) (bool, error) {
	_, err := root.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
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

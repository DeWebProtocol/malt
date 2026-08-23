package backup

import (
	"context"
	"crypto/hmac"
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

	clientadd "github.com/dewebprotocol/malt-client/application/add"
	"github.com/dewebprotocol/malt-client/bucketsync"
	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	"github.com/dewebprotocol/malt-client/internal/durablefile"
	"github.com/dewebprotocol/malt-client/internal/filelock"
	"github.com/dewebprotocol/malt-client/internal/securefile"
	"github.com/dewebprotocol/malt-client/unixfs"
	cid "github.com/ipfs/go-cid"
)

const (
	remoteManifestPrefix = "malt-backup-manifest"
	remoteManifestPath   = remoteManifestPrefix + "/manifest"
	remoteBindingPrefix  = "malt-backup-binding-"
)

const installTransactionVersion = 1

type PlanRootPolicy interface {
	AcceptedRoot(alias string) (cid.Cid, error)
	ObserveCandidate(alias string, candidateRoot, baseRoot cid.Cid, source string) error
}

type planHeadObserver interface {
	ObserveHead(alias, source, datasetID, branch, commitID string, root cid.Cid, revision uint64) error
}

type PlanMaterializer interface {
	MaterializeManifest(context.Context, string, cid.Cid) (*clientadd.Result, error)
	MaterializeBinding(context.Context, string, string, cid.Cid) (*clientadd.Result, error)
}

type AddPlanMaterializer struct {
	// Gateway is retained for source compatibility. Its type is a deprecated
	// alias of clientadd.Materializer; new composition should use
	// NewAddPlanMaterializer so it does not name a network topology.
	Gateway clientadd.Gateway
	CAS     clientadd.CAS
}

// NewAddPlanMaterializer composes a transport-neutral graph materializer and
// immutable block capability while preserving the legacy two-field struct.
func NewAddPlanMaterializer(graph clientadd.Materializer, blocks clientadd.CAS) AddPlanMaterializer {
	return AddPlanMaterializer{Gateway: graph, CAS: blocks}
}

func (m AddPlanMaterializer) MaterializeManifest(ctx context.Context, archivePath string, base cid.Cid) (*clientadd.Result, error) {
	return m.materialize(ctx, archivePath, remoteManifestPrefix, base)
}

func (m AddPlanMaterializer) MaterializeBinding(ctx context.Context, archivePath, bindingID string, base cid.Cid) (*clientadd.Result, error) {
	if _, err := remoteBindingPath(bindingID); err != nil {
		return nil, err
	}
	return m.materialize(ctx, archivePath, remoteBindingPrefix+bindingID, base)
}

func (m AddPlanMaterializer) materialize(ctx context.Context, archivePath, prefix string, base cid.Cid) (*clientadd.Result, error) {
	root := ""
	if base.Defined() {
		root = base.String()
	}
	execution, err := clientadd.Run(ctx, nil, m.Gateway, m.CAS, clientadd.Request{
		Inputs: []string{archivePath},
		Root:   root,
		Options: clientadd.Options{
			Prefix: prefix,
			Target: clientadd.TargetMALT,
			Model:  clientadd.ModelUnixFS,
			Layout: clientadd.LayoutHybrid,
		},
	})
	if err != nil {
		return nil, err
	}
	return execution.Result, nil
}

type PlanServiceOptions struct {
	Plan             Plan
	TempDir          string
	LockPath         string
	Keys             KeySource
	Sync             Sync
	Materializer     PlanMaterializer
	History          *History
	Remote           unixfs.Remote
	Blocks           unixfs.BlockGetter
	Roots            PlanRootPolicy
	Protected        []string
	RestoreProtected []string
}

// PlanService publishes and synchronizes one Bucket branch. Different
// bindings occupy different internal MALT paths, allowing the Gateway to
// merge independent binding updates while same-binding changes conflict.
type PlanService struct {
	mu               sync.Mutex
	plan             Plan
	tempDir          string
	lockPath         string
	keys             KeySource
	sync             Sync
	materializer     PlanMaterializer
	history          *History
	remote           unixfs.Remote
	blocks           unixfs.BlockGetter
	roots            PlanRootPolicy
	protected        []string
	restoreProtected []string
	release          func() error
}

func NewPlanService(opts PlanServiceOptions) (*PlanService, error) {
	return newPlanService(opts, nil)
}

// NewPlanServiceWithRelease composes one plan service with an owned runtime
// resource release. It keeps PlanServiceOptions source-compatible for existing
// embedders while giving the local runtime deterministic transport cleanup.
func NewPlanServiceWithRelease(opts PlanServiceOptions, release func() error) (*PlanService, error) {
	return newPlanService(opts, release)
}

func newPlanService(opts PlanServiceOptions, release func() error) (*PlanService, error) {
	if err := validatePlan(opts.Plan); err != nil {
		return nil, err
	}
	if strings.TrimSpace(opts.TempDir) == "" || strings.TrimSpace(opts.LockPath) == "" {
		return nil, fmt.Errorf("backup plan staging directory and lock are required")
	}
	if opts.Keys == nil || opts.Sync == nil || opts.Materializer == nil || opts.History == nil {
		return nil, fmt.Errorf("backup plan keys, synchronization, materializer, and history are required")
	}
	return &PlanService{
		plan: clonePlan(opts.Plan), tempDir: opts.TempDir, lockPath: opts.LockPath,
		keys: opts.Keys, sync: opts.Sync, materializer: opts.Materializer, history: opts.History,
		remote: opts.Remote, blocks: opts.Blocks, roots: opts.Roots,
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
	if strings.TrimSpace(plan.ID) == "" || strings.TrimSpace(lockPath) == "" {
		return fmt.Errorf("backup plan ID and operation lock path are required for recovery")
	}
	service := &PlanService{plan: clonePlan(plan), lockPath: lockPath}
	return service.Recover()
}

// RecoverTransactionJournals scans the owner-only Plan history directory so
// branch-only restores that crashed before Plan registration are recovered as
// well as already registered Plans.
func RecoverTransactionJournals(directory string) error {
	directory = strings.TrimSpace(directory)
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

func (s *PlanService) Backup(ctx context.Context, message string) (*Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := filelock.Acquire(s.lockPath, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("lock backup plan %s: %w", s.plan.Name, err)
	}
	defer func() { _ = unlock() }()

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
				Source: s.plan.ID, RemotePath: remoteBindingPrefix,
				Base: stash.Base, CandidateRoot: stash.CandidateRoot,
			}
			return result, &ConflictError{Plan: s.plan.Name, Branch: stash.Branch}
		}
		now := time.Now().UTC()
		result := &Result{
			PlanID: s.plan.ID, PlanName: s.plan.Name, Branch: s.plan.Branch,
			Source: s.plan.ID, RemotePath: remoteBindingPrefix,
			SourceFingerprint: combinedFingerprint(before), BindingFingerprints: before,
			ManifestFingerprint: manifestFingerprint, Skipped: true, CompletedAt: now,
		}
		if err := s.history.RecordResult(s.plan.ID, *result); err != nil {
			return result, err
		}
		return result, nil
	}

	stagingRoot, err := s.planStagingRoot()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create backup plan staging root: %w", err)
	}
	staging, err := os.MkdirTemp(stagingRoot, "plan-"+safeID(s.plan.ID)+"-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)

	epoch := s.keys.ActiveEpoch()
	bucketKey, err := s.keys.BucketKey(epoch, s.plan.BucketID)
	if err != nil {
		return nil, err
	}
	type stagedBinding struct {
		binding Binding
		path    string
		info    ArchiveInfo
	}
	staged := make([]stagedBinding, 0, len(changed))
	var encryptedBytes int64
	manifestArchive := ""
	if manifestChanged {
		var manifestInfo ArchiveInfo
		manifestArchive, manifestInfo, err = s.createManifestArchive(ctx, staging, epoch, bucketKey)
		if err != nil {
			return nil, err
		}
		encryptedBytes += manifestInfo.Bytes
	}
	for _, binding := range changed {
		if err := ValidateSource(binding.Source, s.protected); err != nil {
			return nil, fmt.Errorf("binding %s: %w", binding.Name, err)
		}
		dir := filepath.Join(staging, safeID(binding.ID))
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		archivePath := filepath.Join(dir, "snapshot")
		key := deriveBindingKey(bucketKey, s.plan.Branch, binding.ID)
		info, err := CreateBindingArchive(ctx, binding.Source, archivePath, epoch, key)
		if err != nil {
			return nil, fmt.Errorf("archive binding %s: %w", binding.Name, err)
		}
		after, err := FingerprintSource(ctx, binding.Source)
		if err != nil {
			return nil, err
		}
		if after != before[binding.ID] {
			return nil, fmt.Errorf("binding %s changed while its encrypted snapshot was being created; retry", binding.Name)
		}
		staged = append(staged, stagedBinding{binding: binding, path: archivePath, info: info})
		encryptedBytes += info.Bytes
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
	base, err := s.sync.CurrentBase(baseCID)
	if err != nil {
		return nil, err
	}
	candidate := baseCID
	if manifestArchive != "" {
		materializedManifest, err := s.materializer.MaterializeManifest(ctx, manifestArchive, candidate)
		if err != nil {
			return nil, fmt.Errorf("materialize backup plan manifest: %w", err)
		}
		if materializedManifest == nil {
			return nil, fmt.Errorf("backup plan manifest materializer returned an empty result")
		}
		candidate, err = cid.Parse(materializedManifest.NewRoot)
		if err != nil {
			return nil, fmt.Errorf("decode backup plan manifest candidate root: %w", err)
		}
	}
	for _, item := range staged {
		materialized, err := s.materializer.MaterializeBinding(ctx, item.path, item.binding.ID, candidate)
		if err != nil {
			return nil, fmt.Errorf("materialize binding %s: %w", item.binding.Name, err)
		}
		if materialized == nil {
			return nil, fmt.Errorf("binding %s materializer returned an empty result", item.binding.Name)
		}
		candidate, err = cid.Parse(materialized.NewRoot)
		if err != nil {
			return nil, fmt.Errorf("decode binding %s candidate root: %w", item.binding.Name, err)
		}
	}
	if !candidate.Defined() {
		return nil, fmt.Errorf("backup plan produced no candidate root")
	}
	message = strings.TrimSpace(message)
	if message == "" {
		message = strings.TrimSpace(s.plan.Message)
	}
	if message == "" {
		message = "encrypted backup " + s.plan.Name
	}
	result := &Result{
		PlanID: s.plan.ID, PlanName: s.plan.Name, Branch: s.plan.Branch,
		Source: s.plan.ID, RemotePath: remoteBindingPrefix, KeyEpoch: epoch,
		EncryptedBytes: encryptedBytes, SourceFingerprint: combinedFingerprint(before),
		BindingFingerprints: cloneFingerprints(before), ChangedBindings: bindingNames(changed),
		ManifestFingerprint: manifestFingerprint, Base: base, CandidateRoot: candidate.String(),
	}
	pending := PendingBackup{
		BucketID: s.plan.BucketID, PlanID: s.plan.ID, Message: message,
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
	ID          string `json:"id"`
	Name        string `json:"name"`
	ArchiveName string `json:"archive_name"`
}

func (s *PlanService) createManifestArchive(ctx context.Context, staging string, epoch uint32, bucketKey [32]byte) (string, ArchiveInfo, error) {
	source := filepath.Join(staging, "manifest-source")
	if err := os.MkdirAll(source, 0o700); err != nil {
		return "", ArchiveInfo{}, err
	}
	data, err := s.manifestData()
	if err != nil {
		return "", ArchiveInfo{}, err
	}
	if err := os.WriteFile(filepath.Join(source, "manifest.json"), data, 0o600); err != nil {
		return "", ArchiveInfo{}, err
	}
	archive := filepath.Join(staging, "manifest")
	info, err := CreateBindingArchive(ctx, source, archive, epoch, deriveManifestKey(bucketKey, s.plan.Branch))
	if err != nil {
		return "", ArchiveInfo{}, err
	}
	return archive, info, nil
}

func (s *PlanService) manifestData() ([]byte, error) {
	manifest := planManifest{
		Version: 1, PlanID: s.plan.ID, PlanName: s.plan.Name, Branch: s.plan.Branch,
		Bindings: make([]manifestBinding, len(s.plan.Bindings)),
	}
	for i, binding := range s.plan.Bindings {
		manifest.Bindings[i] = manifestBinding{ID: binding.ID, Name: binding.Name, ArchiveName: binding.ArchiveName}
	}
	return json.Marshal(manifest)
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
	if pending.BucketID != s.plan.BucketID || pending.PlanID != s.plan.ID || pending.Result.PlanID != s.plan.ID {
		return nil, fmt.Errorf("%w; pending work belongs to another backup plan", ErrPendingWorkspace)
	}
	candidate, err := cid.Parse(pending.Result.CandidateRoot)
	if err != nil {
		return nil, err
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
	if s.remote == nil || s.blocks == nil {
		return result, fmt.Errorf("backup plan remote reader is not configured")
	}
	root, err := s.acceptedObservedRoot(ctx)
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
	return s.acceptedObservedRoot(ctx)
}

func (s *PlanService) mergeBranchedCandidate(ctx context.Context, remoteRoot cid.Cid) error {
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
			cleanupInstallStaging(entries)
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
		parent := filepath.Dir(binding.Source)
		staging, err := os.MkdirTemp(parent, ".malt-sync-*")
		if err != nil {
			return err
		}
		expectedFingerprint, err := FingerprintSource(ctx, binding.Source)
		if err != nil {
			_ = os.RemoveAll(staging)
			return err
		}
		entry := installTransactionEntry{
			Name: binding.Name, Destination: binding.Source, Staging: staging,
			Next: filepath.Join(staging, "next"), Rollback: filepath.Join(staging, "previous"),
			ExpectedFingerprint: expectedFingerprint, HadCurrent: true, Phase: installPhasePrepared,
		}
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
		cleanupInstallStaging(entries)
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
	if err := s.installPrepared(ctx, s.syncTransactionPath(), entries); err != nil {
		return err
	}
	cleanupEntries = false
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
	remotePath, err := remoteBindingPath(binding.ID)
	if err != nil {
		return err
	}
	err = fetchAndRestoreBinding(ctx, s.remote, s.blocks, root, remotePath, destination, s.tempDir, func(epoch uint32) ([32]byte, error) {
		bucketKey, err := s.keys.BucketKey(epoch, s.plan.BucketID)
		if err != nil {
			return [32]byte{}, err
		}
		return deriveBindingKey(bucketKey, s.plan.Branch, binding.ID), nil
	})
	if allowMissing && (errors.Is(err, unixfs.ErrNotFound) || errors.Is(err, clientcas.ErrNotFound)) {
		return os.MkdirAll(destination, 0o700)
	}
	if err != nil {
		return fmt.Errorf("restore %s snapshot at %s: %w", binding.Name, root, err)
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
			Source: s.plan.ID, RemotePath: remoteBindingPrefix,
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

func (s *PlanService) installManualConflictCheckout(ctx context.Context) error {
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
			cleanupInstallStaging(entries)
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
		parent := filepath.Dir(binding.Source)
		staging, err := os.MkdirTemp(parent, ".malt-sync-*")
		if err != nil {
			return err
		}
		entry := installTransactionEntry{
			Name: binding.Name, Destination: binding.Source, Staging: staging,
			Next: filepath.Join(staging, "next"), Rollback: filepath.Join(staging, "previous"),
			HadCurrent: true, Phase: installPhasePrepared,
		}
		entry.ExpectedFingerprint, err = FingerprintSource(ctx, binding.Source)
		if err != nil {
			_ = os.RemoveAll(staging)
			return err
		}
		entries = append(entries, entry)
		if err := copyPlaintextTree(ctx, mergedPath, entry.Next); err != nil {
			return fmt.Errorf("prepare resolved binding %s: %w", binding.Name, err)
		}
	}
	if len(entries) == 0 {
		return nil
	}
	if err := s.installPrepared(ctx, s.syncTransactionPath(), entries); err != nil {
		return err
	}
	cleanupEntries = false
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
// binding is placed below its encrypted manifest archive name; callers cannot
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
		Source: s.plan.ID, RemotePath: remoteBindingPrefix, CandidateRoot: root.String(),
		SourceFingerprint:   combinedFingerprint(fingerprints),
		BindingFingerprints: fingerprints, ManifestFingerprint: manifestFingerprint,
		CompletedAt: now,
	}
	if err := s.history.RecordResult(s.plan.ID, *result); err != nil {
		return result, err
	}
	return result, nil
}

func (s *PlanService) restoreTo(ctx context.Context, destination string, overwrite bool, restored *Plan) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := filelock.Acquire(s.lockPath, 10*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	if s.remote == nil || s.blocks == nil {
		return fmt.Errorf("backup plan remote reader is not configured")
	}
	if err := s.recoverInstallTransaction(s.restoreTransactionPath()); err != nil {
		return err
	}
	root, err := s.acceptedObservedRoot(ctx)
	if err != nil {
		return err
	}
	destination, err = filepath.Abs(strings.TrimSpace(destination))
	if err != nil {
		return fmt.Errorf("resolve restore destination: %w", err)
	}
	if destination == "" {
		return fmt.Errorf("restore destination is empty")
	}
	protected := append(append([]string(nil), s.protected...), s.restoreProtected...)
	if err := validateRestoreDestination(destination, protected, s.plan.Bindings); err != nil {
		return err
	}
	parent := filepath.Dir(destination)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".malt-restore-*")
	if err != nil {
		return err
	}
	cleanupStaging := true
	defer func() {
		if cleanupStaging {
			_ = os.RemoveAll(staging)
		}
	}()
	next := filepath.Join(staging, "next")
	manifestDir := filepath.Join(staging, "manifest")
	if err := fetchAndRestoreBinding(ctx, s.remote, s.blocks, root, remoteManifestPath, manifestDir, s.tempDir, func(epoch uint32) ([32]byte, error) {
		bucketKey, err := s.keys.BucketKey(epoch, s.plan.BucketID)
		if err != nil {
			return [32]byte{}, err
		}
		return deriveManifestKey(bucketKey, s.plan.Branch), nil
	}); err != nil {
		return fmt.Errorf("restore backup plan manifest: %w", err)
	}
	data, err := os.ReadFile(filepath.Join(manifestDir, "manifest.json"))
	if err != nil {
		return err
	}
	var manifest planManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("decode backup plan manifest: %w", err)
	}
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
	if err := os.MkdirAll(next, 0o700); err != nil {
		return err
	}
	for _, binding := range manifest.Bindings {
		remotePath, err := remoteBindingPath(binding.ID)
		if err != nil {
			return err
		}
		target := filepath.Join(next, binding.ArchiveName)
		if err := fetchAndRestoreBinding(ctx, s.remote, s.blocks, root, remotePath, target, s.tempDir, func(epoch uint32) ([32]byte, error) {
			bucketKey, err := s.keys.BucketKey(epoch, s.plan.BucketID)
			if err != nil {
				return [32]byte{}, err
			}
			return deriveBindingKey(bucketKey, s.plan.Branch, binding.ID), nil
		}); err != nil {
			return fmt.Errorf("restore binding %s: %w", binding.Name, err)
		}
	}
	rollback := filepath.Join(staging, "previous")
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
	if err := s.installPrepared(ctx, s.restoreTransactionPath(), []installTransactionEntry{{
		Name:        s.plan.Name,
		Destination: destination, Staging: staging, Next: next, Rollback: rollback,
		HadCurrent: current, Phase: installPhasePrepared,
	}}); err != nil {
		return err
	}
	if restored != nil {
		now := time.Now().UTC()
		restoredBindings := make([]Binding, len(manifest.Bindings))
		for i, binding := range manifest.Bindings {
			restoredBindings[i] = Binding{
				ID: binding.ID, Name: binding.Name,
				Source:      filepath.Join(destination, binding.ArchiveName),
				ArchiveName: binding.ArchiveName, CreatedAt: now,
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
	destination, err := filepath.Abs(strings.TrimSpace(destination))
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

func (s *PlanService) acceptedObservedRoot(ctx context.Context) (cid.Cid, error) {
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
			Plan: s.plan.Name, Alias: alias, Observed: observed, Cause: err,
		}
	}
	if observed.Equals(accepted) {
		return accepted, nil
	}
	return cid.Undef, &UnacceptedRootError{
		Plan: s.plan.Name, Alias: alias, Observed: observed, Accepted: accepted,
	}
}

func observationSource(datasetID string) string {
	return "dataset:" + strings.TrimSpace(datasetID)
}

func validatePlanManifest(manifest planManifest, local Plan) error {
	if manifest.Version != 1 || manifest.PlanID != local.ID || manifest.Branch != local.Branch || len(manifest.Bindings) == 0 {
		return fmt.Errorf("remote backup plan manifest does not match the selected local plan")
	}
	localBindings := make(map[string]Binding, len(local.Bindings))
	for _, binding := range local.Bindings {
		localBindings[binding.ID] = binding
	}
	seenNames := map[string]struct{}{}
	for _, binding := range manifest.Bindings {
		localBinding, ok := localBindings[binding.ID]
		if !ok || binding.Name != localBinding.Name || binding.ArchiveName != localBinding.ArchiveName {
			return fmt.Errorf("remote backup plan binding %q does not match local plan metadata", binding.Name)
		}
		if err := validateArchiveName(binding.ArchiveName); err != nil {
			return err
		}
		if _, ok := seenNames[binding.ArchiveName]; ok {
			return fmt.Errorf("remote backup plan has duplicate archive names")
		}
		seenNames[binding.ArchiveName] = struct{}{}
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
	seenArchives := map[string]struct{}{}
	for _, binding := range manifest.Bindings {
		if err := validateOpaqueID(binding.ID, "binding"); err != nil {
			return err
		}
		if err := validateDisplayName(binding.Name, "binding"); err != nil {
			return err
		}
		if err := validateArchiveName(binding.ArchiveName); err != nil {
			return err
		}
		if _, ok := seenIDs[binding.ID]; ok {
			return fmt.Errorf("remote backup plan has duplicate binding IDs")
		}
		if _, ok := seenNames[binding.Name]; ok {
			return fmt.Errorf("remote backup plan has duplicate binding names")
		}
		if _, ok := seenArchives[binding.ArchiveName]; ok {
			return fmt.Errorf("remote backup plan has duplicate archive names")
		}
		seenIDs[binding.ID] = struct{}{}
		seenNames[binding.Name] = struct{}{}
		seenArchives[binding.ArchiveName] = struct{}{}
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
	if !e.Accepted.Defined() {
		return fmt.Sprintf(
			"remote root %s for plan %s is not locally accepted; inspect it, run `malt root accept-observed %s %s`, then rerun",
			e.Observed, e.Plan, e.Alias, e.Observed,
		)
	}
	if e.CandidateRecorded {
		return fmt.Sprintf(
			"remote root %s for plan %s differs from accepted root %s; it was recorded as a candidate—inspect it, run `malt root accept %s %s`, then rerun",
			e.Observed, e.Plan, e.Accepted, e.Alias, e.Observed,
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
	if state.LastResult == nil {
		return map[string]string{}, "", nil
	}
	return cloneFingerprints(state.LastResult.BindingFingerprints), state.LastResult.ManifestFingerprint, nil
}

func (s *PlanService) planStagingRoot() (string, error) {
	candidates := []string{s.tempDir, os.TempDir()}
	for _, candidate := range candidates {
		inside := false
		for _, binding := range s.plan.Bindings {
			value, err := resolvedPathWithin(binding.Source, candidate)
			if err != nil {
				return "", err
			}
			if value {
				inside = true
				break
			}
		}
		if !inside {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("no backup staging root is outside every binding in plan %s", s.plan.Name)
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
	ExpectedFingerprint  string `json:"expected_fingerprint,omitempty"`
	OriginalFingerprint  string `json:"original_fingerprint,omitempty"`
	InstalledFingerprint string `json:"installed_fingerprint"`
	HadCurrent           bool   `json:"had_current"`
	Phase                string `json:"phase"`
}

func (s *PlanService) syncTransactionPath() string {
	return s.lockPath + ".sync-transaction.json"
}

func (s *PlanService) restoreTransactionPath() string {
	return s.lockPath + ".restore-transaction.json"
}

func (s *PlanService) installBindings(ctx context.Context, root cid.Cid, expectedFingerprints map[string]string) error {
	journalPath := s.syncTransactionPath()
	if err := s.recoverInstallTransaction(journalPath); err != nil {
		return err
	}
	entries := make([]installTransactionEntry, 0, len(s.plan.Bindings))
	cleanupStaging := true
	defer func() {
		if cleanupStaging {
			cleanupInstallStaging(entries)
		}
	}()

	bucketKeyCache := map[uint32][32]byte{}
	for _, binding := range s.plan.Bindings {
		remotePath, err := remoteBindingPath(binding.ID)
		if err != nil {
			return err
		}
		parent := filepath.Dir(binding.Source)
		if err := os.MkdirAll(parent, 0o700); err != nil {
			return err
		}
		staging, err := os.MkdirTemp(parent, ".malt-sync-*")
		if err != nil {
			return err
		}
		entry := installTransactionEntry{
			Name: binding.Name, Destination: binding.Source, Staging: staging,
			Next: filepath.Join(staging, "next"), Rollback: filepath.Join(staging, "previous"),
			ExpectedFingerprint: expectedFingerprints[binding.ID], Phase: installPhasePrepared,
		}
		entries = append(entries, entry)
		if err := fetchAndRestoreBinding(ctx, s.remote, s.blocks, root, remotePath, entry.Next, s.tempDir, func(epoch uint32) ([32]byte, error) {
			bucketKey, ok := bucketKeyCache[epoch]
			if !ok {
				bucketKey, err = s.keys.BucketKey(epoch, s.plan.BucketID)
				if err != nil {
					return [32]byte{}, err
				}
				bucketKeyCache[epoch] = bucketKey
			}
			return deriveBindingKey(bucketKey, s.plan.Branch, binding.ID), nil
		}); err != nil {
			return fmt.Errorf("prepare synchronized binding %s: %w", binding.Name, err)
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
	for i := range entries {
		if entries[i].HadCurrent {
			fingerprint, err := plaintextContentFingerprint(ctx, entries[i].Destination)
			if err != nil {
				cleanupInstallStaging(entries)
				return fmt.Errorf("fingerprint original %s: %w", entries[i].Name, err)
			}
			entries[i].OriginalFingerprint = fingerprint
		}
		fingerprint, err := plaintextContentFingerprint(ctx, entries[i].Next)
		if err != nil {
			cleanupInstallStaging(entries)
			return fmt.Errorf("fingerprint prepared %s: %w", entries[i].Name, err)
		}
		entries[i].InstalledFingerprint = fingerprint
	}
	transaction := installTransaction{
		Version: installTransactionVersion, PlanID: s.plan.ID,
		State: installStatePrepared, Entries: append([]installTransactionEntry(nil), entries...),
	}
	if err := validateInstallTransaction(transaction); err != nil {
		cleanupInstallStaging(entries)
		return err
	}
	if err := writeInstallTransaction(journalPath, transaction); err != nil {
		cleanupInstallStaging(entries)
		return fmt.Errorf("journal prepared filesystem installation: %w", err)
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
		if entry.HadCurrent {
			if entry.ExpectedFingerprint != "" {
				actual, err := FingerprintSource(ctx, entry.Destination)
				if err != nil {
					return fail(fmt.Errorf("recheck %s before installation: %w", entry.Name, err))
				}
				if actual != entry.ExpectedFingerprint {
					return fail(fmt.Errorf("%s changed after backup; its synchronized snapshot was not installed", entry.Name))
				}
			}
			currentFingerprint, err := plaintextContentFingerprint(ctx, entry.Destination)
			if err != nil {
				return fail(fmt.Errorf("fingerprint %s before installation: %w", entry.Name, err))
			}
			if currentFingerprint != entry.OriginalFingerprint {
				return fail(fmt.Errorf("%s changed while synchronization was being prepared; its snapshot was not installed", entry.Name))
			}
			info, err := os.Lstat(entry.Destination)
			if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return fail(fmt.Errorf("binding destination changed during installation: %s", entry.Destination))
			}
			if err := os.Rename(entry.Destination, entry.Rollback); err != nil {
				return fail(fmt.Errorf("preserve %s before installation: %w", entry.Name, err))
			}
			if err := syncInstallParents(*entry); err != nil {
				return fail(fmt.Errorf("persist preserved %s: %w", entry.Name, err))
			}
			entry.Phase = installPhasePreserved
			if err := writeInstallTransaction(journalPath, transaction); err != nil {
				return fail(fmt.Errorf("journal preserved %s: %w", entry.Name, err))
			}
		} else if _, err := os.Lstat(entry.Destination); !errors.Is(err, os.ErrNotExist) {
			return fail(fmt.Errorf("binding destination appeared during installation: %s", entry.Destination))
		}
		if err := os.Rename(entry.Next, entry.Destination); err != nil {
			return fail(fmt.Errorf("install %s: %w", entry.Name, err))
		}
		if err := syncInstallParents(*entry); err != nil {
			return fail(fmt.Errorf("persist installed %s: %w", entry.Name, err))
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
	if err := validateInstallTransaction(transaction); err != nil {
		return fmt.Errorf("unsafe filesystem installation journal: %w", err)
	}
	if transaction.State == installStateCommitted {
		return finalizeCommittedInstall(journalPath, transaction)
	}
	var quarantined []string
	for i := len(transaction.Entries) - 1; i >= 0; i-- {
		entry := transaction.Entries[i]
		destinationExists, err := pathExists(entry.Destination)
		if err != nil {
			return err
		}
		rollbackExists, err := pathExists(entry.Rollback)
		if err != nil {
			return err
		}
		nextExists, err := pathExists(entry.Next)
		if err != nil {
			return err
		}
		quarantinePath := entry.Staging + "-recovery"
		quarantineExists, err := pathExists(quarantinePath)
		if err != nil {
			return err
		}
		if entry.HadCurrent {
			if rollbackExists {
				if quarantineExists {
					quarantined = append(quarantined, quarantinePath)
				}
				if destinationExists {
					quarantine, err := preserveChangedInstallation(entry, quarantinePath)
					if err != nil {
						return err
					}
					if quarantine != "" {
						quarantined = append(quarantined, quarantine)
					}
				}
				if err := os.Rename(entry.Rollback, entry.Destination); err != nil {
					return fmt.Errorf("restore preserved %s: %w", entry.Name, err)
				}
				if err := syncInstallParents(entry); err != nil {
					return err
				}
				continue
			}
			if quarantineExists && destinationExists {
				fingerprint, err := plaintextContentFingerprint(context.Background(), entry.Destination)
				if err != nil {
					return err
				}
				if fingerprint == entry.OriginalFingerprint {
					quarantined = append(quarantined, quarantinePath)
					continue
				}
			}
			if !destinationExists || !nextExists || entry.Phase != installPhasePrepared {
				return fmt.Errorf("cannot safely recover original %s from interrupted installation", entry.Name)
			}
			continue
		}
		if destinationExists {
			if nextExists && entry.Phase == installPhasePrepared {
				return fmt.Errorf("cannot remove unexpected destination while recovering %s", entry.Name)
			}
			quarantine, err := preserveChangedInstallation(entry, quarantinePath)
			if err != nil {
				return err
			}
			if quarantine != "" {
				quarantined = append(quarantined, quarantine)
			}
		} else if quarantineExists {
			quarantined = append(quarantined, quarantinePath)
		}
	}
	cleanupInstallStaging(transaction.Entries)
	if err := removeInstallTransaction(journalPath); err != nil {
		return err
	}
	if len(quarantined) != 0 {
		sort.Strings(quarantined)
		return &RecoveryQuarantineError{Paths: quarantined}
	}
	return nil
}

func preserveChangedInstallation(entry installTransactionEntry, quarantinePath string) (string, error) {
	fingerprint, err := plaintextContentFingerprint(context.Background(), entry.Destination)
	if err != nil {
		return "", err
	}
	if fingerprint == entry.InstalledFingerprint {
		if err := os.RemoveAll(entry.Destination); err != nil {
			return "", fmt.Errorf("remove interrupted installation %s: %w", entry.Name, err)
		}
		return "", durablefile.SyncParent(entry.Destination)
	}
	if _, err := os.Lstat(quarantinePath); err == nil {
		return "", fmt.Errorf("recovery quarantine already exists for %s: %s", entry.Name, quarantinePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.Rename(entry.Destination, quarantinePath); err != nil {
		return "", fmt.Errorf("quarantine edits made after interrupted installation of %s: %w", entry.Name, err)
	}
	if err := durablefile.SyncParent(entry.Destination); err != nil {
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
		info, err := os.Lstat(entry.Destination)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("committed installation destination is unavailable: %s", entry.Destination)
		}
		if err := os.RemoveAll(entry.Staging); err != nil {
			return fmt.Errorf("remove installation staging for %s: %w", entry.Name, err)
		}
		if err := durablefile.SyncParent(entry.Destination); err != nil {
			return err
		}
	}
	return removeInstallTransaction(journalPath)
}

func removeInstallTransaction(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return durablefile.SyncParent(path)
}

func cleanupInstallStaging(entries []installTransactionEntry) {
	for _, entry := range entries {
		_ = os.RemoveAll(entry.Staging)
	}
}

func syncInstallParents(entry installTransactionEntry) error {
	if err := durablefile.SyncParent(entry.Destination); err != nil {
		return err
	}
	return durablefile.SyncParent(entry.Rollback)
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

func fetchAndRestoreBinding(
	ctx context.Context,
	remote unixfs.Remote,
	blocks unixfs.BlockGetter,
	root cid.Cid,
	remotePath, destination, tempDir string,
	keyForEpoch func(uint32) ([32]byte, error),
) error {
	reader, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: remote, Blocks: blocks})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(tempDir, "binding-*.malt-backup")
	if err != nil {
		return err
	}
	archivePath := file.Name()
	defer os.Remove(archivePath)
	stat, err := reader.Stat(ctx, root, remotePath)
	if err != nil {
		_ = file.Close()
		return err
	}
	if stat.Kind != unixfs.StagedKindFile {
		_ = file.Close()
		return fmt.Errorf("authenticated binding snapshot is not a file")
	}
	for offset := uint64(0); offset < stat.Size; {
		length := restoreRangeSize
		if remaining := stat.Size - offset; remaining < length {
			length = remaining
		}
		part, err := reader.ReadFileRange(ctx, root, remotePath, offset, length)
		if err != nil {
			_ = file.Close()
			return err
		}
		if uint64(len(part.Body)) != length || part.Offset != offset || part.TotalSize != stat.Size {
			_ = file.Close()
			return fmt.Errorf("verified binding range has inconsistent length")
		}
		if _, err := file.Write(part.Body); err != nil {
			_ = file.Close()
			return err
		}
		offset += length
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return restoreArchive(ctx, archivePath, destination, keyForEpoch, false)
}

func deriveBindingKey(bucketKey [32]byte, branch, bindingID string) [32]byte {
	mac := hmac.New(sha256.New, bucketKey[:])
	_, _ = mac.Write([]byte("malt-backup-binding-v1\x00"))
	_, _ = mac.Write([]byte(branch))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write([]byte(bindingID))
	var result [32]byte
	copy(result[:], mac.Sum(nil))
	return result
}

func deriveManifestKey(bucketKey [32]byte, branch string) [32]byte {
	mac := hmac.New(sha256.New, bucketKey[:])
	_, _ = mac.Write([]byte("malt-backup-manifest-v1\x00"))
	_, _ = mac.Write([]byte(branch))
	var result [32]byte
	copy(result[:], mac.Sum(nil))
	return result
}

func remoteBindingPath(bindingID string) (string, error) {
	if bindingID == "" || strings.ContainsAny(bindingID, `/\`) || strings.ContainsAny(bindingID, " \t\r\n") {
		return "", fmt.Errorf("invalid backup binding ID")
	}
	return remoteBindingPrefix + bindingID + "/snapshot", nil
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

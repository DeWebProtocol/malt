package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	clientadd "github.com/dewebprotocol/malt-client/application/add"
	"github.com/dewebprotocol/malt-client/bucketsync"
	"github.com/dewebprotocol/malt-client/internal/filelock"
	"github.com/dewebprotocol/malt-client/unixfs"
	cid "github.com/ipfs/go-cid"
)

const (
	RemotePath       = "malt-backup/snapshot"
	restoreRangeSize = uint64(4 << 20)
)

var (
	ErrPendingWorkspace = errors.New("Bucket has pending or branched local work")
	ErrProtectedSource  = errors.New("backup source contains MALT client key or state")
)

type KeySource interface {
	ActiveEpoch() uint32
	BucketKey(epoch uint32, bucketID string) ([32]byte, error)
}

type Sync interface {
	Pull(context.Context) (bucketsync.Workspace, error)
	Status() (bucketsync.Workspace, error)
	CurrentBase(cid.Cid) (bucketsync.Head, error)
	Stage(cid.Cid, bucketsync.Head, cid.Cid, string) (bucketsync.Stash, error)
	RestorePending(bucketsync.Stash) (bucketsync.Stash, error)
	Push(context.Context, cid.Cid, cid.Cid, string) (bucketsync.PushOutcome, error)
}

type Materializer interface {
	MaterializeBackup(context.Context, string, cid.Cid) (*clientadd.Result, error)
}

type AddMaterializer struct {
	Gateway clientadd.Gateway
	CAS     clientadd.CAS
}

func (m AddMaterializer) MaterializeBackup(ctx context.Context, archivePath string, base cid.Cid) (*clientadd.Result, error) {
	root := ""
	if base.Defined() {
		root = base.String()
	}
	execution, err := clientadd.Run(ctx, nil, m.Gateway, m.CAS, clientadd.Request{
		Inputs: []string{archivePath},
		Root:   root,
		Options: clientadd.Options{
			Prefix: "malt-backup",
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

type Service struct {
	mu           sync.Mutex
	bucketID     string
	tempDir      string
	lockPath     string
	keys         KeySource
	sync         Sync
	materializer Materializer
	history      *History
	protected    []string
}

type Options struct {
	BucketID     string
	TempDir      string
	LockPath     string
	Keys         KeySource
	Sync         Sync
	Materializer Materializer
	History      *History
	Protected    []string
}

type Request struct {
	Source              string `json:"source"`
	Message             string `json:"message,omitempty"`
	JobName             string `json:"-"`
	ExpectedFingerprint string `json:"-"`
}

type Result struct {
	Source            string                 `json:"source"`
	RemotePath        string                 `json:"remote_path"`
	KeyEpoch          uint32                 `json:"key_epoch"`
	EncryptedBytes    int64                  `json:"encrypted_bytes"`
	SourceFingerprint string                 `json:"source_fingerprint"`
	Base              bucketsync.Head        `json:"base"`
	CandidateRoot     string                 `json:"candidate_root"`
	Push              bucketsync.PushOutcome `json:"push"`
	CompletedAt       time.Time              `json:"completed_at"`
	RetriedPending    bool                   `json:"retried_pending,omitempty"`
	ReconciledPending bool                   `json:"reconciled_pending,omitempty"`
}

func NewService(opts Options) (*Service, error) {
	if strings.TrimSpace(opts.BucketID) == "" || strings.TrimSpace(opts.TempDir) == "" || strings.TrimSpace(opts.LockPath) == "" {
		return nil, fmt.Errorf("backup Bucket ID, staging directory, and operation lock are required")
	}
	if opts.Keys == nil || opts.Sync == nil || opts.Materializer == nil || opts.History == nil {
		return nil, fmt.Errorf("backup keys, synchronization, materializer, and history are required")
	}
	return &Service{
		bucketID: strings.TrimSpace(opts.BucketID), tempDir: opts.TempDir, lockPath: opts.LockPath,
		keys: opts.Keys, sync: opts.Sync, materializer: opts.Materializer, history: opts.History,
		protected: append([]string(nil), opts.Protected...),
	}, nil
}

// Run snapshots local bytes before any remote observation, materializes the
// encrypted archive against the recorded Bucket base, stages the candidate,
// and only then lets Push observe the latest remote head.
func (s *Service) Run(ctx context.Context, request Request) (*Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := filelock.Acquire(s.lockPath, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("lock encrypted backup operation: %w", err)
	}
	defer func() { _ = unlock() }()

	source, err := filepath.Abs(strings.TrimSpace(request.Source))
	if err != nil {
		return nil, fmt.Errorf("resolve backup source: %w", err)
	}
	pending, err := s.history.Pending()
	if err != nil {
		return nil, err
	}
	if pending != nil {
		return s.retryPending(ctx, source, strings.TrimSpace(request.JobName), *pending)
	}
	if err := ValidateSource(source, s.protected); err != nil {
		return nil, err
	}
	stagingRoot, err := selectStagingRoot(source, s.tempDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return nil, fmt.Errorf("create backup staging root: %w", err)
	}
	staging, err := os.MkdirTemp(stagingRoot, "backup-*")
	if err != nil {
		return nil, fmt.Errorf("create backup staging directory: %w", err)
	}
	defer os.RemoveAll(staging)
	archivePath := filepath.Join(staging, "snapshot")
	before := strings.TrimSpace(request.ExpectedFingerprint)
	if before == "" {
		before, err = FingerprintSource(ctx, source)
		if err != nil {
			return nil, err
		}
	}
	epoch := s.keys.ActiveEpoch()
	key, err := s.keys.BucketKey(epoch, s.bucketID)
	if err != nil {
		return nil, err
	}
	archive, err := CreateArchive(ctx, source, archivePath, epoch, key)
	if err != nil {
		return nil, err
	}
	after, err := FingerprintSource(ctx, source)
	if err != nil {
		return nil, err
	}
	if before != after {
		return nil, fmt.Errorf("backup source changed while the encrypted snapshot was being created; retry")
	}

	workspace, err := s.sync.Status()
	if err != nil {
		return nil, err
	}
	if !workspace.Initialized {
		workspace, err = s.sync.Pull(ctx)
		if err != nil {
			return nil, err
		}
	}
	if len(workspace.Stashes) != 0 {
		return nil, fmt.Errorf("%w; resolve existing Bucket work before starting another backup", ErrPendingWorkspace)
	}
	baseCID := cid.Undef
	if workspace.Base.Root != "" {
		baseCID, err = cid.Parse(workspace.Base.Root)
		if err != nil {
			return nil, fmt.Errorf("decode backup base root: %w", err)
		}
	}
	base, err := s.sync.CurrentBase(baseCID)
	if err != nil {
		return nil, err
	}
	materialized, err := s.materializer.MaterializeBackup(ctx, archivePath, baseCID)
	if err != nil {
		return nil, err
	}
	if materialized == nil {
		return nil, fmt.Errorf("backup materializer returned an empty result")
	}
	candidate, err := cid.Parse(materialized.NewRoot)
	if err != nil {
		return nil, fmt.Errorf("decode backup candidate root: %w", err)
	}
	message := strings.TrimSpace(request.Message)
	if message == "" {
		message = "encrypted backup snapshot"
	}
	result := &Result{
		Source: source, RemotePath: RemotePath, KeyEpoch: archive.Epoch,
		EncryptedBytes: archive.Bytes, SourceFingerprint: after,
		Base: base, CandidateRoot: candidate.String(),
	}
	pending = &PendingBackup{
		BucketID: s.bucketID, JobName: strings.TrimSpace(request.JobName), Message: message,
		Result: *result, CreatedAt: time.Now().UTC(),
	}
	if err := s.history.SetPending(*pending); err != nil {
		return result, fmt.Errorf("materialized backup candidate %s but its retry journal could not be recorded: %w", candidate, err)
	}
	stash, err := s.sync.Stage(candidate, base, cid.Undef, message)
	if err != nil {
		return result, fmt.Errorf("backup candidate %s is journaled but could not be staged: %w", candidate, err)
	}
	if err := s.history.MarkPendingStaged(candidate.String(), stash); err != nil {
		return result, fmt.Errorf("backup candidate %s is staged but its frozen push identity could not be journaled: %w", candidate, err)
	}
	push, err := s.sync.Push(ctx, candidate, cid.Undef, message)
	if err != nil {
		return result, fmt.Errorf("backup candidate %s is staged but push failed: %w", candidate, err)
	}
	result.Push = push
	result.CompletedAt = time.Now().UTC()
	if err := s.history.CompletePending(candidate.String(), strings.TrimSpace(request.JobName), *result); err != nil {
		return result, fmt.Errorf("backup candidate %s was pushed but its retry journal could not be completed: %w", candidate, err)
	}
	return result, nil
}

func (s *Service) retryPending(ctx context.Context, source, jobName string, pending PendingBackup) (*Result, error) {
	if pending.BucketID != s.bucketID {
		return nil, fmt.Errorf("%w; pending backup belongs to Bucket %q", ErrPendingWorkspace, pending.BucketID)
	}
	if pending.Result.Source != source {
		return nil, fmt.Errorf("%w; retry pending backup for %s before backing up %s", ErrPendingWorkspace, pending.Result.Source, source)
	}
	if pending.JobName != "" && jobName != "" && pending.JobName != jobName {
		return nil, fmt.Errorf("%w; pending backup belongs to automatic job %q", ErrPendingWorkspace, pending.JobName)
	}
	candidate, err := cid.Parse(pending.Result.CandidateRoot)
	if err != nil {
		return nil, fmt.Errorf("decode pending backup candidate root: %w", err)
	}
	result := pending.Result
	result.RetriedPending = true
	completionJobName := pending.JobName
	if completionJobName == "" {
		completionJobName = jobName
	}
	workspace, err := s.sync.Status()
	if err != nil {
		return &result, fmt.Errorf("inspect pending backup workspace: %w", err)
	}
	stash, found, err := pendingStash(workspace, pending, candidate.String())
	if err != nil {
		return &result, err
	}
	if !workspace.Initialized {
		return &result, fmt.Errorf("%w; cannot restore pending backup push identity in an uninitialized Bucket workspace", ErrPendingWorkspace)
	}
	if pending.StashID != "" && !found {
		stash, err = s.sync.RestorePending(bucketsync.Stash{
			ID: pending.StashID, PushID: pending.PushID, CandidateRoot: candidate.String(),
			Base: result.Base, Message: pending.Message, RequestFrozen: true,
			Status: "pending", CreatedAt: pending.CreatedAt,
		})
		if err != nil {
			return &result, fmt.Errorf("restore pending backup push identity %s: %w", candidate, err)
		}
		found = true
	}
	if found && stash.Status == "branched" {
		result.ReconciledPending = true
		result.Push.Workspace = workspace
		result.CompletedAt = time.Now().UTC()
		if err := s.history.CompletePending(candidate.String(), completionJobName, result); err != nil {
			return &result, fmt.Errorf("reconcile branched backup candidate %s: %w", candidate, err)
		}
		return &result, nil
	}
	if !found {
		stash, err = s.sync.Stage(candidate, result.Base, cid.Undef, pending.Message)
		if err != nil {
			return &result, fmt.Errorf("restage pending backup candidate %s: %w", candidate, err)
		}
	}
	if err := s.history.MarkPendingStaged(candidate.String(), stash); err != nil {
		return &result, fmt.Errorf("journal pending backup push identity %s: %w", candidate, err)
	}
	push, err := s.sync.Push(ctx, candidate, cid.Undef, pending.Message)
	if err != nil {
		return &result, fmt.Errorf("retry pending backup candidate %s: %w", candidate, err)
	}
	result.Push = push
	result.CompletedAt = time.Now().UTC()
	if err := s.history.CompletePending(candidate.String(), completionJobName, result); err != nil {
		return &result, fmt.Errorf("pending backup candidate %s was pushed but its retry journal could not be cleared: %w", candidate, err)
	}
	return &result, nil
}

func selectStagingRoot(source, configured string) (string, error) {
	inside, err := resolvedPathWithin(source, configured)
	if err != nil {
		return "", fmt.Errorf("compare backup source and staging root: %w", err)
	}
	if !inside {
		return configured, nil
	}
	fallback := os.TempDir()
	inside, err = resolvedPathWithin(source, fallback)
	if err != nil {
		return "", fmt.Errorf("compare backup source and fallback staging root: %w", err)
	}
	if inside {
		return "", fmt.Errorf("backup source contains both configured and system staging roots; configure backup.temp_dir on a filesystem outside %s", source)
	}
	return fallback, nil
}

func ValidateSource(source string, protected []string) error {
	for _, candidate := range protected {
		if strings.TrimSpace(candidate) == "" {
			continue
		}
		inside, err := pathWithin(source, candidate)
		if err != nil {
			return fmt.Errorf("compare backup source and protected client path: %w", err)
		}
		if !inside {
			inside, err = resolvedPathWithin(source, candidate)
			if err != nil {
				return fmt.Errorf("resolve backup source and protected client path: %w", err)
			}
		}
		if inside {
			return fmt.Errorf("%w %s; choose a narrower source or move client state to an owner-only directory outside it", ErrProtectedSource, candidate)
		}
	}
	return nil
}

func pendingStash(workspace bucketsync.Workspace, pending PendingBackup, candidate string) (bucketsync.Stash, bool, error) {
	for _, stash := range workspace.Stashes {
		if pending.StashID != "" {
			if stash.ID != pending.StashID {
				continue
			}
			if stash.PushID != pending.PushID || stash.CandidateRoot != candidate ||
				stash.Base != pending.Result.Base || stash.Message != pending.Message ||
				stash.ChangeSetCID != "" || (stash.Status != "pending" && stash.Status != "branched") {
				return bucketsync.Stash{}, false, fmt.Errorf("pending backup stash %s conflicts with its journaled push identity", pending.StashID)
			}
			return stash, true, nil
		}
		if stash.CandidateRoot == candidate && (stash.Status == "pending" || stash.Status == "branched") {
			if stash.Base != pending.Result.Base || stash.Message != pending.Message || stash.ChangeSetCID != "" {
				return bucketsync.Stash{}, false, fmt.Errorf("pending backup candidate %s conflicts with an existing Bucket stash", candidate)
			}
			return stash, true, nil
		}
	}
	return bucketsync.Stash{}, false, nil
}

type RestoreOptions struct {
	Remote      unixfs.Remote
	Blocks      unixfs.BlockGetter
	TrustedRoot cid.Cid
	Destination string
	TempDir     string
	BucketID    string
	Keys        KeySource
	Overwrite   bool
}

func Restore(ctx context.Context, opts RestoreOptions) error {
	if opts.Remote == nil || opts.Blocks == nil || !opts.TrustedRoot.Defined() || opts.Keys == nil {
		return fmt.Errorf("restore transport, CAS, trusted root, and backup keys are required")
	}
	if strings.TrimSpace(opts.BucketID) == "" || strings.TrimSpace(opts.Destination) == "" {
		return fmt.Errorf("restore Bucket ID and destination are required")
	}
	reader, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: opts.Remote, Blocks: opts.Blocks})
	if err != nil {
		return fmt.Errorf("construct verified backup reader: %w", err)
	}
	return restoreVerified(ctx, reader, opts)
}

func restoreVerified(ctx context.Context, reader unixfs.Reader, opts RestoreOptions) error {
	tempDir := strings.TrimSpace(opts.TempDir)
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	if err := os.MkdirAll(tempDir, 0o700); err != nil {
		return err
	}
	file, err := os.CreateTemp(tempDir, "restore-*.malt-backup")
	if err != nil {
		return err
	}
	archivePath := file.Name()
	defer os.Remove(archivePath)
	ok := false
	defer func() {
		if !ok {
			_ = file.Close()
		}
	}()

	stat, err := reader.Stat(ctx, opts.TrustedRoot, RemotePath)
	if err != nil {
		return fmt.Errorf("verify backup snapshot path: %w", err)
	}
	if stat.Kind != unixfs.StagedKindFile {
		return fmt.Errorf("authenticated backup snapshot is not a file")
	}
	for offset := uint64(0); offset < stat.Size; {
		length := restoreRangeSize
		if remaining := stat.Size - offset; remaining < length {
			length = remaining
		}
		part, err := reader.ReadFileRange(ctx, opts.TrustedRoot, RemotePath, offset, length)
		if err != nil {
			return fmt.Errorf("read verified backup range at %d: %w", offset, err)
		}
		if uint64(len(part.Body)) != length || part.Offset != offset || part.TotalSize != stat.Size {
			return fmt.Errorf("verified backup range at %d has inconsistent length or size", offset)
		}
		if _, err := file.Write(part.Body); err != nil {
			return err
		}
		offset += length
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	ok = true
	return restoreArchive(ctx, archivePath, opts.Destination, func(epoch uint32) ([32]byte, error) {
		return opts.Keys.BucketKey(epoch, opts.BucketID)
	}, opts.Overwrite)
}

var _ io.Reader = (*contextReader)(nil)

package staging

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dewebprotocol/malt-client/cache"
	filesystemservice "github.com/dewebprotocol/malt-client/filesystem/service"
	"github.com/dewebprotocol/malt-client/internal/filelock"
	"github.com/dewebprotocol/malt-client/journal"
	"github.com/dewebprotocol/malt-client/unixfs"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

const (
	LocalFsyncProfile         = "malt.local-journal-fsync/v1"
	DefaultMaxStagedFileBytes = uint64(256 << 20)
)

var (
	ErrAlreadyExists = errors.New("filesystem path already exists")
	ErrNotEmpty      = errors.New("filesystem directory is not empty")
	ErrClosed        = errors.New("staged filesystem handle is closed")
	ErrServiceClosed = errors.New("filesystem staging service is closed")
	ErrFileTooLarge  = cache.ErrBodyTooLarge
)

// Base is the verified immutable filesystem below the local dirty overlay.
// Implementations must start reads from View.Root and return only locally
// verified bytes. The staging package has no transport capability.
type Base interface {
	Stat(context.Context, filesystemservice.View, string) (filesystemservice.Info, error)
	ReadDir(context.Context, filesystemservice.View, string) ([]filesystemservice.DirEntry, error)
	ReadFileRange(context.Context, filesystemservice.View, string, uint64, uint64) ([]byte, filesystemservice.Info, error)
}

type cacheStore interface {
	Inspect(cache.Binding) (cache.Entry, error)
	List() ([]cache.Entry, error)
	PutLocal(cache.Binding, []byte, cache.State) (cache.Entry, error)
	ReadLocal(cache.Binding) ([]byte, cache.Entry, error)
	ReadLocalBounded(cache.Binding, uint64) ([]byte, cache.Entry, error)
	ReadLocalRange(cache.Binding, uint64, uint64, uint64) ([]byte, cache.Entry, error)
	VerifyLocal(cache.Binding, uint64) (cache.Entry, error)
	Transition(cache.Binding, cache.State) (cache.Entry, error)
	ReconcileLocalState(cache.Binding, cache.State) (cache.Entry, error)
	ReconcileLocalStateBounded(cache.Binding, cache.State, uint64) (cache.Entry, error)
	Remove(cache.Binding) error
}

type operationJournal interface {
	Append(journal.Intent, journal.Status) (journal.Operation, error)
	List() ([]journal.Operation, error)
	FreezeBatchForUpload([]string) ([]journal.Operation, error)
	MarkBatchConflicted([]string, string) ([]journal.Operation, error)
	CompleteBatch([]string, string) ([]journal.Operation, error)
}

type Options struct {
	Base           Base
	CacheDirectory string
	JournalPath    string
	LeaseTimeout   time.Duration
	// MaxStagedFileBytes bounds the current whole-file staging model. Zero uses
	// DefaultMaxStagedFileBytes. Chunked/sparse staging can replace this bound
	// without changing the verified base capability.
	MaxStagedFileBytes uint64
}

// Service serializes local staging across its cache/journal pair. New acquires
// exclusive process-held leases for both paths before opening either store, so
// reconciliation cannot race the cache-to-journal acknowledgement window.
// Call Close to release those leases.
type Service struct {
	lifecycle sync.RWMutex
	mu        sync.Mutex
	base      Base
	cache     cacheStore
	journal   operationJournal
	release   []func() error
	maxFile   uint64
	closed    bool
}

// FsyncResult explicitly distinguishes local journal durability from remote
// persistence, candidate-root verification, and accepted-root promotion.
type FsyncResult struct {
	Profile         string `json:"profile"`
	MaxSequence     uint64 `json:"max_sequence"`
	LocalDurable    bool   `json:"local_durable"`
	RemotePersisted bool   `json:"remote_persisted"`
	CandidateRoot   string `json:"candidate_root,omitempty"`
	RootAccepted    bool   `json:"root_accepted"`
}

func New(opts Options) (*Service, error) {
	if opts.Base == nil {
		return nil, fmt.Errorf("filesystem staging requires a verified base")
	}
	maxFile := opts.MaxStagedFileBytes
	if maxFile == 0 {
		maxFile = DefaultMaxStagedFileBytes
	}
	if maxFile > uint64(maxInt()) {
		return nil, fmt.Errorf("filesystem staging file limit exceeds local address space")
	}
	cacheDirectory, err := absoluteStatePath("cache directory", opts.CacheDirectory)
	if err != nil {
		return nil, err
	}
	journalPath, err := absoluteStatePath("journal path", opts.JournalPath)
	if err != nil {
		return nil, err
	}
	timeout := opts.LeaseTimeout
	if timeout == 0 {
		timeout = 10 * time.Second
	}
	if timeout < 0 {
		return nil, fmt.Errorf("filesystem staging lease timeout must not be negative")
	}
	lockPaths := []string{
		filepath.Join(cacheDirectory, ".malt-staging.lease"),
		journalPath + ".staging.lease",
	}
	slices.Sort(lockPaths)
	releases := make([]func() error, 0, len(lockPaths))
	for index, lockPath := range lockPaths {
		if index != 0 && lockPath == lockPaths[index-1] {
			continue
		}
		release, err := filelock.Acquire(lockPath, timeout)
		if err != nil {
			return nil, errors.Join(fmt.Errorf("acquire filesystem staging lease %s: %w", lockPath, err), releaseAll(releases))
		}
		releases = append(releases, release)
	}
	cacheStore, err := cache.Open(cacheDirectory)
	if err != nil {
		return nil, errors.Join(err, releaseAll(releases))
	}
	journalStore, err := journal.Open(journalPath)
	if err != nil {
		return nil, errors.Join(err, releaseAll(releases))
	}
	service := &Service{base: opts.Base, cache: cacheStore, journal: journalStore, release: releases, maxFile: maxFile}
	if err := service.Reconcile(context.Background()); err != nil {
		return nil, errors.Join(fmt.Errorf("reconcile filesystem staging: %w", err), service.Close())
	}
	return service, nil
}

// Close releases the exclusive cache/journal leases after all active service
// operations finish. It is idempotent, and a failed lease release remains
// retryable while new operations stay permanently closed. Handles cannot read
// after the first Close attempt.
func (s *Service) Close() error {
	if s == nil {
		return nil
	}
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.closed && len(s.release) == 0 {
		return nil
	}
	s.closed = true
	failed := make([]func() error, 0, len(s.release))
	var failures []error
	for index := len(s.release) - 1; index >= 0; index-- {
		if err := s.release[index](); err != nil {
			failures = append(failures, err)
			failed = append(failed, s.release[index])
		}
	}
	slices.Reverse(failed)
	s.release = failed
	return errors.Join(failures...)
}

func (s *Service) StageWrite(ctx context.Context, view filesystemservice.View, rawPath string, body []byte, offline bool) (journal.Operation, error) {
	if err := s.enter(); err != nil {
		return journal.Operation{}, err
	}
	defer s.leave()
	if err := ctx.Err(); err != nil {
		return journal.Operation{}, err
	}
	canonical, err := canonicalPath(rawPath, false)
	if err != nil {
		return journal.Operation{}, err
	}
	if err := s.requireStagedSize(uint64(len(body))); err != nil {
		return journal.Operation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	operations, err := s.operations(view)
	if err != nil {
		return journal.Operation{}, err
	}
	return s.stageWriteBodyLocked(ctx, view, operations, canonical, body, offline)
}

// StageWriteAt atomically applies one offset write to the latest overlay body
// and durably appends the resulting full raw-CID-bound file intent before
// returning. A zero-length write is a no-op.
func (s *Service) StageWriteAt(ctx context.Context, view filesystemservice.View, rawPath string, offset uint64, data []byte, offline bool) (journal.Operation, error) {
	if err := s.enter(); err != nil {
		return journal.Operation{}, err
	}
	defer s.leave()
	if err := ctx.Err(); err != nil {
		return journal.Operation{}, err
	}
	canonical, err := canonicalPath(rawPath, false)
	if err != nil {
		return journal.Operation{}, err
	}
	if len(data) == 0 {
		return journal.Operation{}, nil
	}
	end := offset + uint64(len(data))
	if end < offset || end > uint64(maxInt()) {
		return journal.Operation{}, fmt.Errorf("filesystem offset write exceeds local address space")
	}
	if err := s.requireStagedSize(end); err != nil {
		return journal.Operation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	operations, err := s.operations(view)
	if err != nil {
		return journal.Operation{}, err
	}
	body, err := s.currentFileBodyLocked(ctx, view, operations, canonical)
	if err != nil {
		return journal.Operation{}, err
	}
	if end > uint64(len(body)) {
		expanded := make([]byte, int(end))
		copy(expanded, body)
		body = expanded
	} else {
		body = append([]byte(nil), body...)
	}
	copy(body[int(offset):int(end)], data)
	return s.stageWriteBodyLocked(ctx, view, operations, canonical, body, offline)
}

// StageTruncate atomically resizes an existing file in the latest overlay.
// Extension bytes are zero-filled. An unchanged size is a no-op.
func (s *Service) StageTruncate(ctx context.Context, view filesystemservice.View, rawPath string, size uint64, offline bool) (journal.Operation, error) {
	if err := s.enter(); err != nil {
		return journal.Operation{}, err
	}
	defer s.leave()
	if err := ctx.Err(); err != nil {
		return journal.Operation{}, err
	}
	canonical, err := canonicalPath(rawPath, false)
	if err != nil {
		return journal.Operation{}, err
	}
	if size > uint64(maxInt()) {
		return journal.Operation{}, fmt.Errorf("filesystem truncate exceeds local address space")
	}
	if err := s.requireStagedSize(size); err != nil {
		return journal.Operation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	operations, err := s.operations(view)
	if err != nil {
		return journal.Operation{}, err
	}
	body, err := s.currentFileBodyLocked(ctx, view, operations, canonical)
	if err != nil {
		return journal.Operation{}, err
	}
	if size == uint64(len(body)) {
		return journal.Operation{}, nil
	}
	resized := make([]byte, int(size))
	copy(resized, body)
	return s.stageWriteBodyLocked(ctx, view, operations, canonical, resized, offline)
}

func (s *Service) currentFileBodyLocked(ctx context.Context, view filesystemservice.View, operations []journal.Operation, canonical string) ([]byte, error) {
	resolved, err := s.resolveAt(ctx, view, operations, len(operations), canonical)
	if err != nil {
		return nil, err
	}
	if resolved.info.IsDir() {
		return nil, unixfs.ErrNotFile
	}
	if resolved.info.Size > uint64(maxInt()) {
		return nil, fmt.Errorf("filesystem file exceeds local address space")
	}
	if err := s.requireStagedSize(resolved.info.Size); err != nil {
		return nil, err
	}
	if resolved.local {
		body, _, err := s.cache.ReadLocalBounded(resolved.binding, s.stagedFileLimit())
		return body, err
	}
	body, _, err := s.base.ReadFileRange(ctx, view, resolved.remotePath, 0, resolved.info.Size)
	if err != nil {
		return nil, err
	}
	if uint64(len(body)) != resolved.info.Size {
		return nil, fmt.Errorf("verified base returned %d bytes for declared size %d", len(body), resolved.info.Size)
	}
	return body, nil
}

func (s *Service) stageWriteBodyLocked(ctx context.Context, view filesystemservice.View, operations []journal.Operation, canonical string, body []byte, offline bool) (journal.Operation, error) {
	if err := s.requireStagedSize(uint64(len(body))); err != nil {
		return journal.Operation{}, err
	}
	if err := s.requireParentDirectory(ctx, view, operations, canonical); err != nil {
		return journal.Operation{}, err
	}
	if info, err := s.statAt(ctx, view, operations, len(operations), canonical); err == nil && info.IsDir() {
		return journal.Operation{}, unixfs.ErrNotFile
	} else if err != nil && !errors.Is(err, unixfs.ErrNotFound) {
		return journal.Operation{}, err
	}
	payload, err := rawCID(body)
	if err != nil {
		return journal.Operation{}, err
	}
	binding := bindingFor(view, payload)
	if err := s.ensureLocalBody(binding, body, offline); err != nil {
		return journal.Operation{}, err
	}
	if err := ctx.Err(); err != nil {
		return journal.Operation{}, err
	}
	intent, err := journal.NewIntent(
		view.DatasetID, view.Branch, view.Root.String(), view.Revision,
		journal.KindWrite, canonical, "", payload.String(), view.EncryptionEpoch,
	)
	if err != nil {
		return journal.Operation{}, err
	}
	return s.journal.Append(intent, initialStatus(offline))
}

func (s *Service) StageMkdir(ctx context.Context, view filesystemservice.View, rawPath string, offline bool) (journal.Operation, error) {
	if err := s.enter(); err != nil {
		return journal.Operation{}, err
	}
	defer s.leave()
	canonical, err := canonicalPath(rawPath, false)
	if err != nil {
		return journal.Operation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	operations, err := s.operations(view)
	if err != nil {
		return journal.Operation{}, err
	}
	if err := s.requireParentDirectory(ctx, view, operations, canonical); err != nil {
		return journal.Operation{}, err
	}
	if _, err := s.statAt(ctx, view, operations, len(operations), canonical); err == nil {
		return journal.Operation{}, ErrAlreadyExists
	} else if !errors.Is(err, unixfs.ErrNotFound) {
		return journal.Operation{}, err
	}
	return s.appendNamespace(ctx, view, journal.KindMkdir, canonical, "", offline)
}

func (s *Service) StageUnlink(ctx context.Context, view filesystemservice.View, rawPath string, offline bool) (journal.Operation, error) {
	if err := s.enter(); err != nil {
		return journal.Operation{}, err
	}
	defer s.leave()
	return s.stageRemove(ctx, view, rawPath, false, offline)
}

func (s *Service) StageRemoveDir(ctx context.Context, view filesystemservice.View, rawPath string, offline bool) (journal.Operation, error) {
	if err := s.enter(); err != nil {
		return journal.Operation{}, err
	}
	defer s.leave()
	return s.stageRemove(ctx, view, rawPath, true, offline)
}

func (s *Service) stageRemove(ctx context.Context, view filesystemservice.View, rawPath string, directory, offline bool) (journal.Operation, error) {
	canonical, err := canonicalPath(rawPath, false)
	if err != nil {
		return journal.Operation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	operations, err := s.operations(view)
	if err != nil {
		return journal.Operation{}, err
	}
	info, err := s.statAt(ctx, view, operations, len(operations), canonical)
	if err != nil {
		return journal.Operation{}, err
	}
	if directory != info.IsDir() {
		if directory {
			return journal.Operation{}, unixfs.ErrNotDirectory
		}
		return journal.Operation{}, unixfs.ErrNotFile
	}
	if directory {
		entries, err := s.readDirAt(ctx, view, operations, canonical)
		if err != nil {
			return journal.Operation{}, err
		}
		if len(entries) != 0 {
			return journal.Operation{}, ErrNotEmpty
		}
	}
	return s.appendNamespace(ctx, view, journal.KindUnlink, canonical, "", offline)
}

func (s *Service) StageRename(ctx context.Context, view filesystemservice.View, rawSource, rawDestination string, offline bool) (journal.Operation, error) {
	if err := s.enter(); err != nil {
		return journal.Operation{}, err
	}
	defer s.leave()
	source, err := canonicalPath(rawSource, false)
	if err != nil {
		return journal.Operation{}, err
	}
	destination, err := canonicalPath(rawDestination, false)
	if err != nil {
		return journal.Operation{}, err
	}
	if source == destination || isWithin(destination, source) {
		return journal.Operation{}, fmt.Errorf("rename destination is equal to or below its source")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	operations, err := s.operations(view)
	if err != nil {
		return journal.Operation{}, err
	}
	sourceInfo, err := s.statAt(ctx, view, operations, len(operations), source)
	if err != nil {
		return journal.Operation{}, err
	}
	if err := s.requireParentDirectory(ctx, view, operations, destination); err != nil {
		return journal.Operation{}, err
	}
	destinationInfo, destinationErr := s.statAt(ctx, view, operations, len(operations), destination)
	if destinationErr == nil {
		if sourceInfo.IsDir() != destinationInfo.IsDir() {
			if sourceInfo.IsDir() {
				return journal.Operation{}, unixfs.ErrNotDirectory
			}
			return journal.Operation{}, unixfs.ErrNotFile
		}
		if destinationInfo.IsDir() {
			entries, err := s.readDirAt(ctx, view, operations, destination)
			if err != nil {
				return journal.Operation{}, err
			}
			if len(entries) != 0 {
				return journal.Operation{}, ErrNotEmpty
			}
		}
	} else if !errors.Is(destinationErr, unixfs.ErrNotFound) {
		return journal.Operation{}, destinationErr
	}
	return s.appendNamespace(ctx, view, journal.KindRename, source, destination, offline)
}

func (s *Service) appendNamespace(ctx context.Context, view filesystemservice.View, kind journal.Kind, operationPath, destination string, offline bool) (journal.Operation, error) {
	if err := ctx.Err(); err != nil {
		return journal.Operation{}, err
	}
	intent, err := journal.NewIntent(
		view.DatasetID, view.Branch, view.Root.String(), view.Revision,
		kind, operationPath, destination, "", view.EncryptionEpoch,
	)
	if err != nil {
		return journal.Operation{}, err
	}
	return s.journal.Append(intent, initialStatus(offline))
}

func (s *Service) Stat(ctx context.Context, view filesystemservice.View, rawPath string) (filesystemservice.Info, error) {
	if err := s.enter(); err != nil {
		return filesystemservice.Info{}, err
	}
	defer s.leave()
	canonical, err := canonicalPath(rawPath, true)
	if err != nil {
		return filesystemservice.Info{}, err
	}
	operations, err := s.operations(view)
	if err != nil {
		return filesystemservice.Info{}, err
	}
	return s.statAt(ctx, view, operations, len(operations), canonical)
}

func (s *Service) ReadDir(ctx context.Context, view filesystemservice.View, rawPath string) ([]filesystemservice.DirEntry, error) {
	if err := s.enter(); err != nil {
		return nil, err
	}
	defer s.leave()
	canonical, err := canonicalPath(rawPath, true)
	if err != nil {
		return nil, err
	}
	operations, err := s.operations(view)
	if err != nil {
		return nil, err
	}
	return s.readDirAt(ctx, view, operations, canonical)
}

func (s *Service) Open(ctx context.Context, view filesystemservice.View, rawPath string) (*Handle, error) {
	if err := s.enter(); err != nil {
		return nil, err
	}
	defer s.leave()
	canonical, err := canonicalPath(rawPath, false)
	if err != nil {
		return nil, err
	}
	operations, err := s.operations(view)
	if err != nil {
		return nil, err
	}
	resolved, err := s.resolveAt(ctx, view, operations, len(operations), canonical)
	if err != nil {
		return nil, err
	}
	if resolved.info.IsDir() {
		return nil, unixfs.ErrNotFile
	}
	return &Handle{service: s, view: view, resolved: resolved}, nil
}

func (s *Service) ReadFileRange(ctx context.Context, view filesystemservice.View, rawPath string, offset, length uint64) ([]byte, filesystemservice.Info, error) {
	handle, err := s.Open(ctx, view, rawPath)
	if err != nil {
		return nil, filesystemservice.Info{}, err
	}
	defer handle.Close()
	body, err := handle.Read(ctx, offset, length)
	return body, handle.Info(), err
}

func (s *Service) Fsync(ctx context.Context, view filesystemservice.View) (FsyncResult, error) {
	if err := s.enter(); err != nil {
		return FsyncResult{}, err
	}
	defer s.leave()
	if err := ctx.Err(); err != nil {
		return FsyncResult{}, err
	}
	operations, err := s.operations(view)
	if err != nil {
		return FsyncResult{}, err
	}
	var sequence uint64
	for _, operation := range operations {
		if operation.Sequence > sequence {
			sequence = operation.Sequence
		}
		if operation.Kind != journal.KindWrite {
			continue
		}
		if _, err := s.cache.VerifyLocal(bindingFromOperation(operation), s.stagedFileLimit()); err != nil {
			return FsyncResult{}, fmt.Errorf("fsync local payload for operation %s: %w", operation.OperationID, err)
		}
	}
	return FsyncResult{
		Profile: LocalFsyncProfile, MaxSequence: sequence, LocalDurable: true,
		RemotePersisted: false, RootAccepted: false,
	}, nil
}

// Reconcile removes local cache bodies that have no journal reference and
// rejects referenced writes whose exact CID-bound body is unavailable. It
// never discards an unresolved journal record.
func (s *Service) Reconcile(ctx context.Context) error {
	if err := s.enter(); err != nil {
		return err
	}
	defer s.leave()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	operations, err := s.journal.List()
	if err != nil {
		return err
	}
	referenced := make(map[cache.Binding]struct{})
	for _, operation := range operations {
		if operation.Kind != journal.KindWrite {
			continue
		}
		binding := bindingFromOperation(operation)
		referenced[binding] = struct{}{}
		// Completed means the remote result is only a recorded candidate. Until
		// the caller selects a new accepted View (and later prunes the record),
		// this overlay still exposes the locally staged bytes. Only a superseded
		// record is excluded from operations(view), so it no longer needs its
		// own body here.
		if operation.Status != journal.StatusSuperseded {
			if _, err := s.cache.VerifyLocal(binding, s.stagedFileLimit()); err != nil {
				return fmt.Errorf("journal operation %s local payload: %w", operation.OperationID, err)
			}
		}
	}
	if err := s.reconcileCacheStates(operations); err != nil {
		return err
	}
	entries, err := s.cache.List()
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		if !isLocalState(entry.State) {
			continue
		}
		if _, ok := referenced[entry.Binding]; ok {
			continue
		}
		if err := s.cache.Remove(entry.Binding); err != nil && !errors.Is(err, cache.ErrMiss) {
			failures = append(failures, fmt.Errorf("remove unreferenced local cache body %s: %w", entry.Binding.CID, err))
		}
	}
	return errors.Join(failures...)
}

func (s *Service) operations(view filesystemservice.View) ([]journal.Operation, error) {
	if err := validateView(view); err != nil {
		return nil, err
	}
	all, err := s.journal.List()
	if err != nil {
		return nil, err
	}
	root := view.Root.String()
	result := make([]journal.Operation, 0, len(all))
	for _, operation := range all {
		if operation.Status == journal.StatusSuperseded || operation.DatasetID != view.DatasetID ||
			operation.Branch != view.Branch || operation.BaseRoot != root ||
			operation.BaseRevision != view.Revision || operation.EncryptionEpoch != view.EncryptionEpoch {
			continue
		}
		result = append(result, operation)
	}
	slices.SortFunc(result, func(left, right journal.Operation) int {
		if left.Sequence < right.Sequence {
			return -1
		}
		if left.Sequence > right.Sequence {
			return 1
		}
		return 0
	})
	return result, nil
}

type resolvedNode struct {
	info       filesystemservice.Info
	local      bool
	remotePath string
	binding    cache.Binding
	createdAt  int
}

func (s *Service) statAt(ctx context.Context, view filesystemservice.View, operations []journal.Operation, limit int, canonical string) (filesystemservice.Info, error) {
	resolved, err := s.resolveAt(ctx, view, operations, limit, canonical)
	if err != nil {
		return filesystemservice.Info{}, err
	}
	return resolved.info, nil
}

func (s *Service) resolveAt(ctx context.Context, view filesystemservice.View, operations []journal.Operation, limit int, canonical string) (resolvedNode, error) {
	if err := ctx.Err(); err != nil {
		return resolvedNode{}, err
	}
	mapped := canonical
	for index := limit - 1; index >= 0; index-- {
		operation := operations[index]
		switch operation.Kind {
		case journal.KindWrite:
			if mapped == operation.Path {
				binding := bindingFromOperation(operation)
				entry, err := s.cache.VerifyLocal(binding, s.stagedFileLimit())
				if err != nil {
					return resolvedNode{}, err
				}
				return resolvedNode{
					local: true, binding: binding,
					info: localInfo(canonical, unixfs.StagedKindFile, binding.CID, uint64(entry.Size)),
				}, nil
			}
			if isStrictDescendant(mapped, operation.Path) {
				return resolvedNode{}, unixfs.ErrNotDirectory
			}
		case journal.KindMkdir:
			if mapped == operation.Path {
				return resolvedNode{
					local: true, createdAt: index + 1,
					info: localInfo(canonical, unixfs.StagedKindDirectory, cid.Undef, 0),
				}, nil
			}
			if isStrictDescendant(mapped, operation.Path) {
				return resolvedNode{}, unixfs.ErrNotFound
			}
		case journal.KindUnlink:
			if isWithin(mapped, operation.Path) {
				return resolvedNode{}, unixfs.ErrNotFound
			}
		case journal.KindRename:
			if isWithin(mapped, operation.Destination) {
				mapped = replacePrefix(mapped, operation.Destination, operation.Path)
				continue
			}
			if isWithin(mapped, operation.Path) {
				return resolvedNode{}, unixfs.ErrNotFound
			}
		}
	}
	info, err := s.base.Stat(ctx, view, mapped)
	if err != nil {
		return resolvedNode{}, err
	}
	info.Path = canonical
	info.Name = path.Base(canonical)
	if canonical == "" {
		info.Name = ""
	}
	return resolvedNode{info: info, remotePath: mapped}, nil
}

func (s *Service) readDirAt(ctx context.Context, view filesystemservice.View, operations []journal.Operation, canonical string) ([]filesystemservice.DirEntry, error) {
	resolved, err := s.resolveAt(ctx, view, operations, len(operations), canonical)
	if err != nil {
		return nil, err
	}
	if !resolved.info.IsDir() {
		return nil, unixfs.ErrNotDirectory
	}
	candidates := make(map[string]struct{})
	if !resolved.local {
		baseEntries, err := s.base.ReadDir(ctx, view, resolved.remotePath)
		if err != nil {
			return nil, err
		}
		for _, entry := range baseEntries {
			original := joinPath(resolved.remotePath, entry.Name)
			current, ok := forwardPath(original, operations)
			if ok {
				addImmediateCandidate(candidates, canonical, current)
			}
		}
	}
	start := resolved.createdAt
	for index := start; index < len(operations); index++ {
		operation := operations[index]
		var targets []string
		switch operation.Kind {
		case journal.KindWrite, journal.KindMkdir:
			targets = []string{operation.Path}
		case journal.KindRename:
			targets = []string{operation.Destination}
		}
		for _, target := range targets {
			current, ok := forwardPath(target, operations[index+1:])
			if ok {
				addImmediateCandidate(candidates, canonical, current)
			}
		}
	}
	names := make([]string, 0, len(candidates))
	for name := range candidates {
		names = append(names, name)
	}
	slices.Sort(names)
	entries := make([]filesystemservice.DirEntry, 0, len(names))
	for _, name := range names {
		info, err := s.statAt(ctx, view, operations, len(operations), joinPath(canonical, name))
		if errors.Is(err, unixfs.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		entries = append(entries, filesystemservice.DirEntry{Name: name, Kind: info.Kind})
	}
	return entries, nil
}

func (s *Service) requireParentDirectory(ctx context.Context, view filesystemservice.View, operations []journal.Operation, canonical string) error {
	parent := path.Dir(canonical)
	if parent == "." {
		parent = ""
	}
	info, err := s.statAt(ctx, view, operations, len(operations), parent)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return unixfs.ErrNotDirectory
	}
	return nil
}

func (s *Service) ensureLocalBody(binding cache.Binding, body []byte, offline bool) error {
	entry, err := s.cache.Inspect(binding)
	if errors.Is(err, cache.ErrMiss) {
		_, err = s.cache.PutLocal(binding, body, cacheState(offline))
		return err
	}
	if err != nil {
		return err
	}
	switch entry.State {
	case cache.StateVerifiedClean, cache.StateUnmaterializedRemote:
		if _, err := s.cache.Transition(binding, cache.StateStale); err != nil {
			return err
		}
		_, err = s.cache.PutLocal(binding, body, cacheState(offline))
		return err
	case cache.StateStale:
		_, err = s.cache.PutLocal(binding, body, cacheState(offline))
		return err
	default:
		existing, _, err := s.cache.ReadLocalBounded(binding, s.stagedFileLimit())
		if err != nil {
			return err
		}
		if !bytes.Equal(existing, body) {
			return fmt.Errorf("local cache body does not match its content identity")
		}
		return nil
	}
}

// Handle is pinned to the immutable base View and the exact local payload CID
// or verified remote path selected when Open succeeded.
type Handle struct {
	mu       sync.Mutex
	service  *Service
	view     filesystemservice.View
	resolved resolvedNode
	closed   bool
}

func (h *Handle) Info() filesystemservice.Info {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.resolved.info
}

func (h *Handle) Read(ctx context.Context, offset, length uint64) ([]byte, error) {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return nil, ErrClosed
	}
	service, view, resolved := h.service, h.view, h.resolved
	h.mu.Unlock()
	if err := service.enter(); err != nil {
		return nil, err
	}
	defer service.leave()
	if resolved.local {
		body, _, err := service.cache.ReadLocalRange(resolved.binding, offset, length, service.stagedFileLimit())
		return body, err
	}
	body, _, err := service.base.ReadFileRange(ctx, view, resolved.remotePath, offset, length)
	return body, err
}

func (h *Handle) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	return nil
}

func validateView(view filesystemservice.View) error {
	if err := validateIdentity("dataset", view.DatasetID); err != nil {
		return err
	}
	if err := validateIdentity("branch", view.Branch); err != nil {
		return err
	}
	if !view.Root.Defined() {
		return fmt.Errorf("filesystem staging View is incomplete")
	}
	return nil
}

func validateIdentity(name, value string) error {
	if value == "" || strings.TrimSpace(value) != value || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return fmt.Errorf("filesystem staging View %s identity is empty or not canonical UTF-8", name)
	}
	return nil
}

func (s *Service) enter() error {
	if s == nil {
		return ErrServiceClosed
	}
	s.lifecycle.RLock()
	if s.closed {
		s.lifecycle.RUnlock()
		return ErrServiceClosed
	}
	return nil
}

func (s *Service) leave() {
	s.lifecycle.RUnlock()
}

func absoluteStatePath(name, value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("filesystem staging %s is empty", name)
	}
	absolute, err := filepath.Abs(filepath.Clean(value))
	if err != nil {
		return "", fmt.Errorf("resolve filesystem staging %s: %w", name, err)
	}
	return absolute, nil
}

func releaseAll(releases []func() error) error {
	var failures []error
	for index := len(releases) - 1; index >= 0; index-- {
		if releases[index] == nil {
			continue
		}
		if err := releases[index](); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func canonicalPath(raw string, allowRoot bool) (string, error) {
	if raw == "" && allowRoot {
		return "", nil
	}
	segments, err := unixfs.ParseCanonicalStagedPath(raw)
	if err != nil {
		return "", err
	}
	if len(segments) == 0 {
		return "", fmt.Errorf("filesystem staging path must not be root")
	}
	return strings.Join(segments, "/"), nil
}

func rawCID(body []byte) (cid.Cid, error) {
	digest, err := mh.Sum(body, mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, digest), nil
}

func bindingFor(view filesystemservice.View, payload cid.Cid) cache.Binding {
	return cache.Binding{
		DatasetID: view.DatasetID, Branch: view.Branch, Root: view.Root,
		Revision: view.Revision, CID: payload, EncryptionEpoch: view.EncryptionEpoch,
	}
}

func bindingFromOperation(operation journal.Operation) cache.Binding {
	root, _ := cid.Parse(operation.BaseRoot)
	payload, _ := cid.Parse(operation.PayloadCID)
	return cache.Binding{
		DatasetID: operation.DatasetID, Branch: operation.Branch, Root: root,
		Revision: operation.BaseRevision, CID: payload, EncryptionEpoch: operation.EncryptionEpoch,
	}
}

func initialStatus(offline bool) journal.Status {
	if offline {
		return journal.StatusOfflineOnly
	}
	return journal.StatusLocalDirty
}

func cacheState(offline bool) cache.State {
	if offline {
		return cache.StateOfflineOnly
	}
	return cache.StateLocalDirty
}

func isLocalState(state cache.State) bool {
	switch state {
	case cache.StateLocalDirty, cache.StateOfflineOnly, cache.StatePendingUpload, cache.StateCandidate, cache.StateConflicted:
		return true
	default:
		return false
	}
}

func localInfo(canonical, kind string, payload cid.Cid, size uint64) filesystemservice.Info {
	name := path.Base(canonical)
	if canonical == "" {
		name = ""
	}
	return filesystemservice.Info{
		Path: canonical, Name: name, Kind: kind, Payload: payload,
		StorageKind: "raw", Size: size,
	}
}

func sliceRange(body []byte, offset, length uint64) []byte {
	if length == 0 || offset >= uint64(len(body)) {
		return []byte{}
	}
	end := offset + length
	if end < offset || end > uint64(len(body)) {
		end = uint64(len(body))
	}
	return append([]byte(nil), body[offset:end]...)
}

func maxInt() int { return int(^uint(0) >> 1) }

func (s *Service) requireStagedSize(size uint64) error {
	limit := s.stagedFileLimit()
	if size > limit {
		return fmt.Errorf("%w: size %d, limit %d", ErrFileTooLarge, size, limit)
	}
	return nil
}

func (s *Service) stagedFileLimit() uint64 {
	if s != nil && s.maxFile != 0 {
		return s.maxFile
	}
	return DefaultMaxStagedFileBytes
}

func isWithin(value, ancestor string) bool {
	return ancestor == "" || value == ancestor || strings.HasPrefix(value, ancestor+"/")
}

func isStrictDescendant(value, ancestor string) bool {
	return value != ancestor && isWithin(value, ancestor)
}

func replacePrefix(value, from, to string) string {
	if value == from {
		return to
	}
	return joinPath(to, strings.TrimPrefix(value, from+"/"))
}

func joinPath(parent, name string) string {
	if parent == "" {
		return name
	}
	if name == "" {
		return parent
	}
	return parent + "/" + name
}

func forwardPath(value string, operations []journal.Operation) (string, bool) {
	current := value
	for _, operation := range operations {
		switch operation.Kind {
		case journal.KindUnlink:
			if isWithin(current, operation.Path) {
				return "", false
			}
		case journal.KindWrite:
			if isStrictDescendant(current, operation.Path) {
				return "", false
			}
		case journal.KindRename:
			if isWithin(current, operation.Path) {
				current = replacePrefix(current, operation.Path, operation.Destination)
			} else if isWithin(current, operation.Destination) {
				return "", false
			}
		}
	}
	return current, true
}

func addImmediateCandidate(candidates map[string]struct{}, parent, value string) {
	if value == parent || !isWithin(value, parent) {
		return
	}
	relative := value
	if parent != "" {
		relative = strings.TrimPrefix(value, parent+"/")
	}
	name, _, _ := strings.Cut(relative, "/")
	if name != "" {
		candidates[name] = struct{}{}
	}
}

var _ interface {
	Info() filesystemservice.Info
	Read(context.Context, uint64, uint64) ([]byte, error)
	Close() error
} = (*Handle)(nil)

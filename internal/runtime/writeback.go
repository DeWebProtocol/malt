package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"sync"

	"github.com/dewebprotocol/malt-client/application"
	writebackapp "github.com/dewebprotocol/malt-client/application/writeback"
	filesystemmount "github.com/dewebprotocol/malt-client/filesystem/mount"
	filesystemservice "github.com/dewebprotocol/malt-client/filesystem/service"
	"github.com/dewebprotocol/malt-client/filesystem/staging"
	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
	truststore "github.com/dewebprotocol/malt-client/trust"
	"github.com/dewebprotocol/malt-client/unixfs"
	unixfsplanner "github.com/dewebprotocol/malt-client/unixfs/planner"
	"github.com/dewebprotocol/malt-core/auth/commitment/ipa"
	"github.com/dewebprotocol/malt-core/auth/commitment/kzg"
	"github.com/dewebprotocol/malt-core/auth/engine"
	"github.com/dewebprotocol/malt-core/auth/input"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

const gatewayWritebackSource = "filesystem verified write-back"

type gatewayWritableRemote interface {
	Get(context.Context, cid.Cid) ([]byte, error)
	Put(context.Context, []byte) (cid.Cid, error)
	PutWithCodec(context.Context, []byte, uint64) (cid.Cid, error)
	AuthenticationCandidate(context.Context, cid.Cid) (*protocol.AuthenticationCandidate, error)
	transportcap.AuthenticationBatch
}

type gatewayWritableBlocks interface {
	Get(context.Context, cid.Cid) ([]byte, error)
	Put(context.Context, []byte) (cid.Cid, error)
	PutWithCodec(context.Context, []byte, uint64) (cid.Cid, error)
}

type writerFactory interface {
	New() (*engine.Engine, error)
}

type authenticationEngineFactory struct {
	once    sync.Once
	schemes []engine.Profile
	err     error
}

// New returns an authentication engine over shared immutable
// commitment parameters. The compact IPA profile changes only local execution
// memory and performance; it does not change roots, proofs, or transcripts.
func (f *authenticationEngineFactory) New() (*engine.Engine, error) {
	if f == nil {
		return nil, fmt.Errorf("authentication engine factory is nil")
	}
	f.once.Do(func() {
		kzgScheme, err := kzg.NewScheme()
		if err != nil {
			f.err = fmt.Errorf("initialize KZG writer: %w", err)
			return
		}
		ipaScheme, err := ipa.NewCommitterScheme(ipa.ProfileCompact)
		if err != nil {
			f.err = fmt.Errorf("initialize IPA writer: %w", err)
			return
		}
		f.schemes = []engine.Profile{kzgScheme, ipaScheme}
	})
	if f.err != nil {
		return nil, f.err
	}
	profiles := engine.NewRegistry()
	for _, scheme := range f.schemes {
		if err := profiles.Register(scheme); err != nil {
			return nil, err
		}
	}
	return engine.New(input.DefaultRegistry(), profiles), nil
}

type gatewayWritableBindingOptions struct {
	Spec               filesystemmount.Spec
	View               filesystemservice.View
	Base               staging.Base
	Remote             gatewayWritableRemote
	Blocks             gatewayWritableBlocks
	Roots              truststore.Policy
	WriterFactory      writerFactory
	StateDirectory     string
	MaxStagedFileBytes uint64
	Source             string
	Release            func() error
}

func newGatewayWritableBinding(ctx context.Context, opts gatewayWritableBindingOptions) (filesystemmount.WritableBinding, error) {
	if ctx == nil {
		return nil, fmt.Errorf("Gateway writable binding context is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	spec, err := filesystemmount.NormalizeSpec(opts.Spec)
	if err != nil {
		return nil, err
	}
	if spec.WritePolicy != filesystemmount.WriteBack || spec.DatasetID != opts.View.DatasetID || spec.Branch != opts.View.Branch || !opts.View.Root.Defined() {
		return nil, fmt.Errorf("Gateway writable binding Spec and View do not match")
	}
	descriptor, _, err := maltcid.ParseRoot(opts.View.Root)
	if err != nil || descriptor.Layout != maltcid.Prefix {
		return nil, fmt.Errorf("Gateway writable binding requires an authenticated Prefix Root")
	}
	if nilInterface(opts.Base) || nilInterface(opts.Remote) || nilInterface(opts.Roots) || nilInterface(opts.WriterFactory) {
		return nil, fmt.Errorf("Gateway writable binding requires base, remote, roots, and writer factory")
	}
	blocks := opts.Blocks
	if nilInterface(blocks) {
		blocks = opts.Remote
	}
	layout, err := unixfs.ParseLayoutKind(string(spec.LayoutPolicy))
	if err != nil {
		return nil, err
	}
	cacheDirectory, journalPath, err := writableStatePaths(opts.StateDirectory, spec.DatasetID, spec.Branch)
	if err != nil {
		return nil, err
	}
	staged, err := staging.New(staging.Options{Base: opts.Base, CacheDirectory: cacheDirectory, JournalPath: journalPath, MaxStagedFileBytes: opts.MaxStagedFileBytes})
	if err != nil {
		return nil, fmt.Errorf("open filesystem write-back staging: %w", err)
	}
	binding := &runtimeWritableBinding{view: opts.View, staged: staged, closing: true, release: opts.Release}
	if err := ensureWritableLayoutState(filepath.Join(filepath.Dir(journalPath), "layout.json"), spec); err != nil {
		return binding, fmt.Errorf("bind filesystem write-back layout: %w", err)
	}
	e, err := opts.WriterFactory.New()
	if err != nil {
		return binding, err
	}
	planner, err := unixfsplanner.New(layout, blocks, opts.Remote, e)
	if err != nil {
		return binding, err
	}
	roots, err := application.NewRoots(opts.Roots)
	if err != nil {
		return binding, err
	}
	source := strings.TrimSpace(opts.Source)
	if source == "" {
		source = gatewayWritebackSource
	}
	replay, err := writebackapp.New(writebackapp.Options{Queue: staged, Payloads: blocks, Remote: opts.Remote,
		Planner: planner, Roots: roots, TrustAlias: spec.TrustAlias, Source: source})
	if err != nil {
		return binding, err
	}
	binding.replay = replay
	binding.closing = false
	return binding, nil
}

func writableStatePaths(root, datasetID, branch string) (string, string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", "", fmt.Errorf("filesystem writable state directory is empty")
	}
	if strings.TrimSpace(datasetID) != datasetID || strings.TrimSpace(branch) != branch || datasetID == "" || branch == "" ||
		strings.ContainsRune(datasetID, '\x00') || strings.ContainsRune(branch, '\x00') {
		return "", "", fmt.Errorf("filesystem writable dataset and branch identities are invalid")
	}
	digest := sha256.Sum256([]byte("malt-filesystem-writeback/v1\x00" + datasetID + "\x00" + branch))
	directory := filepath.Join(filepath.Clean(root), hex.EncodeToString(digest[:]))
	return filepath.Join(directory, "cache"), filepath.Join(directory, "journal.json"), nil
}

type viewFilesystemBase struct {
	filesystem filesystemmount.ViewFilesystem
}

func (b viewFilesystemBase) Stat(ctx context.Context, view filesystemservice.View, path string) (filesystemservice.Info, error) {
	if nilInterface(b.filesystem) {
		return filesystemservice.Info{}, fmt.Errorf("verified filesystem base is nil")
	}
	return b.filesystem.Stat(ctx, view, path)
}

func (b viewFilesystemBase) ReadDir(ctx context.Context, view filesystemservice.View, path string) ([]filesystemservice.DirEntry, error) {
	if nilInterface(b.filesystem) {
		return nil, fmt.Errorf("verified filesystem base is nil")
	}
	return b.filesystem.ReadDir(ctx, view, path)
}

func (b viewFilesystemBase) ReadFileRange(ctx context.Context, view filesystemservice.View, path string, offset, length uint64) ([]byte, filesystemservice.Info, error) {
	if nilInterface(b.filesystem) {
		return nil, filesystemservice.Info{}, fmt.Errorf("verified filesystem base is nil")
	}
	handle, err := b.filesystem.Open(ctx, view, path)
	if err != nil {
		return nil, filesystemservice.Info{}, err
	}
	if handle == nil {
		return nil, filesystemservice.Info{}, fmt.Errorf("verified filesystem returned a nil handle")
	}
	info := handle.Info()
	body, readErr := handle.Read(ctx, offset, length)
	closeErr := handle.Close()
	if readErr != nil || closeErr != nil {
		return nil, filesystemservice.Info{}, errors.Join(readErr, closeErr)
	}
	return body, info, nil
}

type writebackReplayer interface {
	Replay(context.Context, filesystemservice.View) (writebackapp.Result, error)
}

type runtimeWritableBinding struct {
	lifecycle sync.RWMutex
	mutations sync.Mutex
	view      filesystemservice.View
	staged    *staging.Service
	replay    writebackReplayer
	closing   bool
	closed    bool
	release   func() error
}

func (b *runtimeWritableBinding) enter() (func(), error) {
	if b == nil {
		return nil, filesystemservice.ErrClosed
	}
	b.lifecycle.RLock()
	if b.closing || b.closed || b.staged == nil {
		b.lifecycle.RUnlock()
		return nil, filesystemservice.ErrClosed
	}
	return b.lifecycle.RUnlock, nil
}

func (b *runtimeWritableBinding) Stat(ctx context.Context, path string) (filesystemservice.Info, error) {
	leave, err := b.enter()
	if err != nil {
		return filesystemservice.Info{}, err
	}
	defer leave()
	info, err := b.staged.Stat(ctx, b.view, path)
	return info, mapStagingError(err)
}

func (b *runtimeWritableBinding) ReadDir(ctx context.Context, path string) ([]filesystemservice.DirEntry, error) {
	leave, err := b.enter()
	if err != nil {
		return nil, err
	}
	defer leave()
	entries, err := b.staged.ReadDir(ctx, b.view, path)
	return entries, mapStagingError(err)
}

func (b *runtimeWritableBinding) Open(ctx context.Context, path string) (filesystemmount.ReadHandle, error) {
	leave, err := b.enter()
	if err != nil {
		return nil, err
	}
	defer leave()
	handle, err := b.staged.Open(ctx, b.view, path)
	if err != nil {
		return nil, mapStagingError(err)
	}
	if handle == nil {
		return nil, fmt.Errorf("filesystem staging returned a nil handle")
	}
	return &runtimeReadHandle{handle: handle}, nil
}

func (b *runtimeWritableBinding) Create(ctx context.Context, path string) (filesystemservice.Info, error) {
	leave, err := b.enter()
	if err != nil {
		return filesystemservice.Info{}, err
	}
	defer leave()
	b.mutations.Lock()
	defer b.mutations.Unlock()
	if _, err := b.staged.Stat(ctx, b.view, path); err == nil {
		return filesystemservice.Info{}, filesystemmount.ErrAlreadyExists
	} else if !errors.Is(err, unixfs.ErrNotFound) {
		return filesystemservice.Info{}, mapStagingError(err)
	}
	if _, err := b.staged.StageWrite(ctx, b.view, path, nil, false); err != nil {
		return filesystemservice.Info{}, mapStagingError(err)
	}
	info, err := b.staged.Stat(ctx, b.view, path)
	return info, mapStagingError(err)
}

func (b *runtimeWritableBinding) WriteAt(ctx context.Context, path string, offset uint64, data []byte) (filesystemservice.Info, error) {
	leave, err := b.enter()
	if err != nil {
		return filesystemservice.Info{}, err
	}
	defer leave()
	b.mutations.Lock()
	defer b.mutations.Unlock()
	if _, err := b.staged.StageWriteAt(ctx, b.view, path, offset, data, false); err != nil {
		return filesystemservice.Info{}, mapStagingError(err)
	}
	info, err := b.staged.Stat(ctx, b.view, path)
	return info, mapStagingError(err)
}

func (b *runtimeWritableBinding) Truncate(ctx context.Context, path string, size uint64) (filesystemservice.Info, error) {
	leave, err := b.enter()
	if err != nil {
		return filesystemservice.Info{}, err
	}
	defer leave()
	b.mutations.Lock()
	defer b.mutations.Unlock()
	if _, err := b.staged.StageTruncate(ctx, b.view, path, size, false); err != nil {
		return filesystemservice.Info{}, mapStagingError(err)
	}
	info, err := b.staged.Stat(ctx, b.view, path)
	return info, mapStagingError(err)
}

func (b *runtimeWritableBinding) Mkdir(ctx context.Context, path string) (filesystemservice.Info, error) {
	leave, err := b.enter()
	if err != nil {
		return filesystemservice.Info{}, err
	}
	defer leave()
	b.mutations.Lock()
	defer b.mutations.Unlock()
	if _, err := b.staged.StageMkdir(ctx, b.view, path, false); err != nil {
		return filesystemservice.Info{}, mapStagingError(err)
	}
	info, err := b.staged.Stat(ctx, b.view, path)
	return info, mapStagingError(err)
}

func (b *runtimeWritableBinding) Rename(ctx context.Context, source, destination string) error {
	leave, err := b.enter()
	if err != nil {
		return err
	}
	defer leave()
	b.mutations.Lock()
	defer b.mutations.Unlock()
	if source == destination {
		_, err := b.staged.Stat(ctx, b.view, source)
		return mapStagingError(err)
	}
	_, err = b.staged.StageRename(ctx, b.view, source, destination, false)
	return mapStagingError(err)
}

func (b *runtimeWritableBinding) Unlink(ctx context.Context, path string) error {
	leave, err := b.enter()
	if err != nil {
		return err
	}
	defer leave()
	b.mutations.Lock()
	defer b.mutations.Unlock()
	_, err = b.staged.StageUnlink(ctx, b.view, path, false)
	return mapStagingError(err)
}

func (b *runtimeWritableBinding) RemoveDir(ctx context.Context, path string) error {
	leave, err := b.enter()
	if err != nil {
		return err
	}
	defer leave()
	b.mutations.Lock()
	defer b.mutations.Unlock()
	_, err = b.staged.StageRemoveDir(ctx, b.view, path, false)
	return mapStagingError(err)
}

func (b *runtimeWritableBinding) Sync(ctx context.Context) (filesystemmount.SyncResult, error) {
	leave, err := b.enter()
	if err != nil {
		return filesystemmount.SyncResult{}, err
	}
	defer leave()
	b.mutations.Lock()
	defer b.mutations.Unlock()
	local, err := b.staged.Fsync(ctx, b.view)
	result := filesystemmount.SyncResult{
		LocalDurable: local.LocalDurable, RemotePersisted: local.RemotePersisted,
		CandidateRoot: local.CandidateRoot, RootAccepted: false,
	}
	if err != nil {
		return result, mapStagingError(err)
	}
	if local.Profile != staging.LocalFsyncProfile || !local.LocalDurable || local.RootAccepted ||
		(local.RemotePersisted && strings.TrimSpace(local.CandidateRoot) == "") {
		return filesystemmount.SyncResult{}, fmt.Errorf("filesystem staging returned an invalid local fsync result")
	}
	if nilInterface(b.replay) {
		return result, fmt.Errorf("filesystem write-back replayer is nil")
	}
	replayed, err := b.replay.Replay(ctx, b.view)
	if errors.Is(err, staging.ErrNoPendingUpload) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if replayed.Profile != writebackapp.ResultProfile || replayed.RootAccepted {
		return result, fmt.Errorf("filesystem write-back returned an invalid trust result")
	}
	if replayed.NoAuthenticatedChange {
		if replayed.RemotePersisted || replayed.CandidateRoot.Defined() || replayed.CandidateStored {
			return result, fmt.Errorf("filesystem no-change write-back returned a remote candidate claim")
		}
		return filesystemmount.SyncResult{LocalDurable: true}, nil
	}
	if !replayed.RemotePersisted || !replayed.CandidateStored || !validCandidateRoot(b.view.Root, replayed.CandidateRoot) {
		return result, fmt.Errorf("filesystem write-back did not persist and record an exact candidate")
	}
	return filesystemmount.SyncResult{
		LocalDurable: true, RemotePersisted: true,
		CandidateRoot: replayed.CandidateRoot.String(), RootAccepted: false,
	}, nil
}

func validCandidateRoot(base, candidate cid.Cid) bool {
	if !base.Defined() || !candidate.Defined() || candidate.Equals(base) {
		return false
	}
	next, _, err := maltcid.ParseRoot(candidate)
	if err != nil || next.Layout != maltcid.Prefix {
		return false
	}
	old, _, err := maltcid.ParseRoot(base)
	return err == nil && old == next
}

func (b *runtimeWritableBinding) Close() error {
	if b == nil {
		return nil
	}
	b.lifecycle.Lock()
	defer b.lifecycle.Unlock()
	if b.closed {
		return nil
	}
	b.closing = true
	if b.staged == nil {
		return fmt.Errorf("filesystem write-back staging is nil")
	}
	if err := b.staged.Close(); err != nil {
		return err
	}
	if b.release != nil {
		if err := b.release(); err != nil {
			return err
		}
		b.release = nil
	}
	b.closed = true
	return nil
}

type runtimeReadHandle struct {
	handle *staging.Handle
}

func (h *runtimeReadHandle) Info() filesystemservice.Info {
	if h == nil || h.handle == nil {
		return filesystemservice.Info{}
	}
	return h.handle.Info()
}

func (h *runtimeReadHandle) Read(ctx context.Context, offset, length uint64) ([]byte, error) {
	if h == nil || h.handle == nil {
		return nil, filesystemservice.ErrClosed
	}
	body, err := h.handle.Read(ctx, offset, length)
	return body, mapStagingError(err)
}

func (h *runtimeReadHandle) Close() error {
	if h == nil || h.handle == nil {
		return nil
	}
	return mapStagingError(h.handle.Close())
}

func mapStagingError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, staging.ErrAlreadyExists):
		return filesystemmount.ErrAlreadyExists
	case errors.Is(err, staging.ErrNotEmpty):
		return filesystemmount.ErrNotEmpty
	case errors.Is(err, staging.ErrFileTooLarge):
		return filesystemmount.ErrFileTooLarge
	case errors.Is(err, staging.ErrClosed), errors.Is(err, staging.ErrServiceClosed):
		return filesystemservice.ErrClosed
	default:
		return err
	}
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var (
	_ filesystemmount.WritableBinding = (*runtimeWritableBinding)(nil)
	_ filesystemmount.ReadHandle      = (*runtimeReadHandle)(nil)
)

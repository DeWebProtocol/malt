package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	localspool "github.com/dewebprotocol/malt-client/internal/cas/spool"
	"github.com/dewebprotocol/malt-client/internal/durablefile"
	"github.com/dewebprotocol/malt-client/unixfs"
	encryptedfs "github.com/dewebprotocol/malt-client/unixfs/encrypted"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

// PlanFilesystem is the encrypted MALT-native filesystem capability consumed
// by backup/sync/restore. It separates local snapshot preparation from remote
// head observation and never owns accepted-root policy.
type PlanFilesystem interface {
	DefaultBackend(context.Context) (maltcid.BackendKind, error)
	RecoverSnapshots(context.Context, string) error
	BeginSnapshot(context.Context, maltcid.BackendKind, string) (PlanFilesystemSnapshot, error)
	LoadDataset(context.Context, cid.Cid, string, string, encryptedfs.KeyResolver) (*PlanDataset, error)
	RestoreBinding(context.Context, *PlanDataset, string, string, encryptedfs.KeyResolver) error
	RestoreBindingRoot(context.Context, *PlanDataset, string, *os.Root, encryptedfs.KeyResolver) error
}

type PlanFilesystemSnapshot interface {
	PrepareBinding(context.Context, encryptedfs.BindingSource) (encryptedfs.PreparedBinding, error)
	BuildDataset(context.Context, PlanDatasetBuildRequest) (encryptedfs.DatasetBuildResult, error)
	Publish(context.Context) error
	Close() error
}

// PlanDataset is an application-layer immutable snapshot of a verified
// encrypted dataset. The native view remains private so callers cannot forge
// the verifier seal used by reads or manifest reuse.
type PlanDataset struct {
	native      *encryptedfs.DatasetView
	root        cid.Cid
	manifestCID cid.Cid
	manifest    encryptedfs.DatasetManifest
	bindings    []encryptedfs.BindingView
}

type PlanDatasetBuildRequest struct {
	Request       encryptedfs.DatasetBuildRequest
	ReuseManifest *PlanDataset
}

func (d *PlanDataset) Binding(id string) (encryptedfs.BindingView, bool) {
	if d == nil {
		return encryptedfs.BindingView{}, false
	}
	for _, binding := range d.bindings {
		if binding.Manifest.ID == id {
			return binding, true
		}
	}
	return encryptedfs.BindingView{}, false
}

func newPlanDataset(view *encryptedfs.DatasetView) (*PlanDataset, error) {
	root, err := view.VerifiedRoot()
	if err != nil {
		return nil, err
	}
	manifestCID, err := view.VerifiedManifestCID()
	if err != nil {
		return nil, err
	}
	manifest, err := view.VerifiedManifest()
	if err != nil {
		return nil, err
	}
	bindings := make([]encryptedfs.BindingView, 0, len(manifest.Bindings))
	for _, binding := range manifest.Bindings {
		value, ok := view.Binding(binding.ID)
		if !ok {
			return nil, fmt.Errorf("verified encrypted filesystem dataset omitted binding %q", binding.ID)
		}
		bindings = append(bindings, value)
	}
	return &PlanDataset{
		native: view, root: root, manifestCID: manifestCID,
		manifest: manifest, bindings: bindings,
	}, nil
}

type encryptedPlanFilesystem struct {
	graph   PlanFilesystemGraph
	blocks  PlanFilesystemBlocks
	reader  *encryptedfs.Reader
	profile PlanFilesystemProfile
}

type PlanFilesystemBlocks interface {
	encryptedfs.BlockWriter
	unixfs.BlockGetter
}

type PlanFilesystemGraph interface {
	encryptedfs.GraphWriter
}

type PlanFilesystemProfile interface {
	DefaultBackend(context.Context) (maltcid.BackendKind, error)
}

func NewPlanFilesystem(graph PlanFilesystemGraph, remote unixfs.Remote, blocks PlanFilesystemBlocks, profile PlanFilesystemProfile) (PlanFilesystem, error) {
	if graph == nil || remote == nil || blocks == nil || profile == nil {
		return nil, fmt.Errorf("encrypted filesystem graph, read, block, and commitment-profile capabilities are required")
	}
	reader, err := encryptedfs.NewReader(encryptedfs.ReaderOptions{Remote: remote, Blocks: blocks})
	if err != nil {
		return nil, err
	}
	return &encryptedPlanFilesystem{graph: graph, blocks: blocks, reader: reader, profile: profile}, nil
}

func (f *encryptedPlanFilesystem) DefaultBackend(ctx context.Context) (maltcid.BackendKind, error) {
	if f == nil || f.profile == nil {
		return maltcid.BackendKindUnknown, fmt.Errorf("encrypted filesystem commitment profile is nil")
	}
	return f.profile.DefaultBackend(ctx)
}

// RecoverSnapshots removes ciphertext-only snapshot state left by a crashed or
// unsuccessfully cleaned publication. The caller supplies a Plan-exclusive
// namespace and must hold that Plan's cross-process operation lock.
func (f *encryptedPlanFilesystem) RecoverSnapshots(ctx context.Context, directory string) error {
	if f == nil {
		return fmt.Errorf("encrypted filesystem is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return recoverSnapshotDirectory(directory)
}

func (f *encryptedPlanFilesystem) BeginSnapshot(ctx context.Context, backend maltcid.BackendKind, directory string) (PlanFilesystemSnapshot, error) {
	if f == nil || f.graph == nil || f.blocks == nil {
		return nil, fmt.Errorf("encrypted filesystem is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	directory = filepath.Clean(directory)
	if directory == "." || directory == "" {
		return nil, fmt.Errorf("encrypted filesystem snapshot directory is required")
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return nil, err
	}
	if err := durablefile.SyncParent(directory); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(directory, ".encrypted-snapshot-*")
	if err != nil {
		return nil, err
	}
	local, err := localspool.Open(staging)
	if err != nil {
		return nil, errors.Join(err, removeSnapshotDirectory(staging))
	}
	snapshot, err := encryptedfs.NewSnapshot(encryptedfs.SnapshotOptions{
		Backend: backend, LocalBlocks: local, RemoteGraph: f.graph, RemoteBlocks: f.blocks,
	})
	if err != nil {
		return nil, errors.Join(err, removeSnapshotDirectory(staging))
	}
	return &encryptedPlanSnapshot{snapshot: snapshot, directory: staging}, nil
}

func recoverSnapshotDirectory(directory string) error {
	directory = filepath.Clean(directory)
	if directory == "." || directory == "" {
		return fmt.Errorf("encrypted filesystem snapshot directory is required")
	}
	if err := os.RemoveAll(directory); err != nil {
		return fmt.Errorf("remove stale encrypted filesystem snapshots: %w", err)
	}
	if err := durablefile.SyncParent(directory); err != nil {
		return fmt.Errorf("persist stale encrypted filesystem snapshot cleanup: %w", err)
	}
	return nil
}

func removeSnapshotDirectory(directory string) error {
	if err := os.RemoveAll(directory); err != nil {
		return err
	}
	return durablefile.SyncParent(directory)
}

func (f *encryptedPlanFilesystem) LoadDataset(ctx context.Context, root cid.Cid, datasetID, branch string, keys encryptedfs.KeyResolver) (*PlanDataset, error) {
	view, err := f.reader.LoadDataset(ctx, root, datasetID, branch, keys)
	if err != nil {
		return nil, err
	}
	return newPlanDataset(view)
}

func (f *encryptedPlanFilesystem) RestoreBinding(ctx context.Context, dataset *PlanDataset, bindingID, destination string, keys encryptedfs.KeyResolver) error {
	if dataset == nil || dataset.native == nil {
		return fmt.Errorf("encrypted filesystem dataset is not backed by a verified native view")
	}
	return f.reader.RestoreBinding(ctx, dataset.native, bindingID, destination, keys)
}

func (f *encryptedPlanFilesystem) RestoreBindingRoot(ctx context.Context, dataset *PlanDataset, bindingID string, root *os.Root, keys encryptedfs.KeyResolver) error {
	if dataset == nil || dataset.native == nil {
		return fmt.Errorf("encrypted filesystem dataset is not backed by a verified native view")
	}
	return f.reader.RestoreBindingRoot(ctx, dataset.native, bindingID, root, keys)
}

var _ PlanFilesystem = (*encryptedPlanFilesystem)(nil)

type encryptedPlanSnapshot struct {
	snapshot  *encryptedfs.Snapshot
	directory string
	once      sync.Once
	err       error
}

func (s *encryptedPlanSnapshot) PrepareBinding(ctx context.Context, request encryptedfs.BindingSource) (encryptedfs.PreparedBinding, error) {
	return s.snapshot.PrepareBinding(ctx, request)
}

func (s *encryptedPlanSnapshot) BuildDataset(ctx context.Context, request PlanDatasetBuildRequest) (encryptedfs.DatasetBuildResult, error) {
	native := request.Request
	if request.ReuseManifest != nil {
		if request.ReuseManifest.native == nil {
			return encryptedfs.DatasetBuildResult{}, fmt.Errorf("reused encrypted filesystem manifest is not backed by a verified native view")
		}
		native.ReuseManifest = request.ReuseManifest.native
	}
	return s.snapshot.BuildDataset(ctx, native)
}

func (s *encryptedPlanSnapshot) Publish(ctx context.Context) error {
	return s.snapshot.Publish(ctx)
}

func (s *encryptedPlanSnapshot) Close() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		s.err = removeSnapshotDirectory(s.directory)
	})
	return s.err
}

var _ PlanFilesystemSnapshot = (*encryptedPlanSnapshot)(nil)

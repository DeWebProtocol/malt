// Package mount owns durable, daemon-managed lifecycle for platform read-only
// mount adapters. Platform adapters translate syscalls only; trust selection,
// verified reads, and cache policy remain above them.
package mount

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	filesystemservice "github.com/dewebprotocol/malt-client/filesystem/service"
)

var ErrManagerClosed = errors.New("mount manager is closed")

// ReadOnlyFilesystem is the only data capability exposed to a platform mount
// adapter. It is already pinned to one locally selected immutable View.
type ReadOnlyFilesystem interface {
	Stat(context.Context, string) (filesystemservice.Info, error)
	ReadDir(context.Context, string) ([]filesystemservice.DirEntry, error)
	Open(context.Context, string) (ReadHandle, error)
}

type ReadHandle interface {
	Info() filesystemservice.Info
	Read(context.Context, uint64, uint64) ([]byte, error)
	Close() error
}

type Session interface {
	// Unmount must be idempotent because a failed return can leave the local
	// runtime unable to distinguish a completed platform detach from a retryable
	// failure.
	Unmount(context.Context) error
	Done() <-chan error
}

// Adapter is the outermost platform boundary. Mount must reconcile an exact
// stale mount owned by the same Spec or return a recoverable error.
// RecoverUnmount must be idempotent and may clean only that exact mountpoint.
type Adapter interface {
	Name() string
	Mount(context.Context, Spec, ReadOnlyFilesystem) (Session, error)
	RecoverUnmount(context.Context, Spec) error
}

// ViewSelector returns only locally accepted state. Implementations must not
// select an observed remote head as a filesystem authority.
type ViewSelector interface {
	SelectView(context.Context, Spec) (filesystemservice.View, error)
}

type ViewSelectorFunc func(context.Context, Spec) (filesystemservice.View, error)

func (f ViewSelectorFunc) SelectView(ctx context.Context, spec Spec) (filesystemservice.View, error) {
	return f(ctx, spec)
}

type Status struct {
	Spec         Spec   `json:"spec"`
	Desired      bool   `json:"desired"`
	Active       bool   `json:"active"`
	Adapter      string `json:"adapter"`
	SelectedRoot string `json:"selected_root,omitempty"`
	Revision     uint64 `json:"revision,omitempty"`
	LastError    string `json:"last_error,omitempty"`
}

type Options struct {
	Store      *Store
	Selector   ViewSelector
	Filesystem ViewFilesystem
	Adapter    Adapter
}

// ViewFilesystem is implemented by filesystem/service.Service. It remains a
// separate port so lifecycle tests and future local-only projections do not
// require a concrete transport.
type ViewFilesystem interface {
	Stat(context.Context, filesystemservice.View, string) (filesystemservice.Info, error)
	ReadDir(context.Context, filesystemservice.View, string) ([]filesystemservice.DirEntry, error)
	Open(context.Context, filesystemservice.View, string) (*filesystemservice.Handle, error)
}

type liveMount struct {
	session Session
	view    filesystemservice.View
	token   uint64
}

type Manager struct {
	opMu        sync.Mutex
	mu          sync.Mutex
	store       *Store
	selector    ViewSelector
	filesystem  ViewFilesystem
	adapter     Adapter
	live        map[string]liveMount
	errors      map[string]string
	nextToken   uint64
	leaseUnlock func() error
	closed      bool
}

func NewManager(opts Options) (*Manager, error) {
	if opts.Store == nil || opts.Selector == nil || opts.Filesystem == nil || opts.Adapter == nil {
		return nil, fmt.Errorf("mount manager requires store, view selector, filesystem, and adapter")
	}
	if strings.TrimSpace(opts.Adapter.Name()) == "" {
		return nil, fmt.Errorf("mount adapter name is empty")
	}
	leaseUnlock, err := opts.Store.acquireManagerLease()
	if err != nil {
		return nil, fmt.Errorf("acquire mount manager lease: %w", err)
	}
	return &Manager{
		store: opts.Store, selector: opts.Selector, filesystem: opts.Filesystem, adapter: opts.Adapter,
		live: map[string]liveMount{}, errors: map[string]string{}, nextToken: 1, leaseUnlock: leaseUnlock,
	}, nil
}

// Mount persists desired state before platform I/O. A crash after this point
// is repaired by Restore, which retries the exact durable Spec.
func (m *Manager) Mount(ctx context.Context, spec Spec) (Status, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if err := m.ensureOpen(); err != nil {
		return Status{}, err
	}
	return m.mount(ctx, spec)
}

func (m *Manager) mount(ctx context.Context, spec Spec) (Status, error) {
	record, err := m.store.PutDesired(spec)
	if err != nil {
		return Status{}, err
	}
	m.mu.Lock()
	if current, ok := m.live[record.Spec.ID]; ok {
		status := m.statusLocked(record, current, true)
		m.mu.Unlock()
		return status, nil
	}
	m.mu.Unlock()
	view, err := m.selectView(ctx, record.Spec)
	if err != nil {
		m.mu.Lock()
		m.errors[record.Spec.ID] = err.Error()
		status := m.statusLocked(record, liveMount{}, false)
		m.mu.Unlock()
		return status, err
	}
	bound := boundFilesystem{service: m.filesystem, view: view}
	session, err := m.adapter.Mount(ctx, record.Spec, bound)
	if err != nil {
		m.mu.Lock()
		m.errors[record.Spec.ID] = err.Error()
		status := m.statusLocked(record, liveMount{view: view}, false)
		m.mu.Unlock()
		return status, err
	}
	if session == nil || session.Done() == nil {
		if session != nil {
			_ = session.Unmount(context.Background())
		}
		err := fmt.Errorf("mount adapter returned an incomplete session")
		m.mu.Lock()
		m.errors[record.Spec.ID] = err.Error()
		status := m.statusLocked(record, liveMount{view: view}, false)
		m.mu.Unlock()
		return status, err
	}
	m.mu.Lock()
	token := m.nextToken
	m.nextToken++
	live := liveMount{session: session, view: view, token: token}
	m.live[record.Spec.ID] = live
	delete(m.errors, record.Spec.ID)
	status := m.statusLocked(record, live, true)
	m.mu.Unlock()
	go m.monitor(record.Spec.ID, token, session.Done())
	return status, nil
}

// Unmount first persists a pending-unmount tombstone. The record is deleted
// only after the live or recovered platform mount is confirmed unmounted.
func (m *Manager) Unmount(ctx context.Context, id string) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if err := m.ensureOpen(); err != nil {
		return err
	}
	record, err := m.store.MarkPendingUnmount(id)
	if err != nil {
		return err
	}
	m.mu.Lock()
	live, active := m.live[record.Spec.ID]
	m.mu.Unlock()
	if active {
		err = live.session.Unmount(ctx)
	} else {
		err = m.adapter.RecoverUnmount(ctx, record.Spec)
	}
	if err != nil {
		m.mu.Lock()
		m.errors[record.Spec.ID] = err.Error()
		m.mu.Unlock()
		return err
	}
	if err := m.store.DeleteUnmounted(record.Spec.ID); err != nil {
		m.mu.Lock()
		m.errors[record.Spec.ID] = err.Error()
		m.mu.Unlock()
		return err
	}
	m.mu.Lock()
	delete(m.live, record.Spec.ID)
	delete(m.errors, record.Spec.ID)
	m.mu.Unlock()
	return nil
}

// Restore cleans incomplete unmounts first, then recreates every desired
// mount from a fresh locally selected accepted View.
func (m *Manager) Restore(ctx context.Context) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	if err := m.ensureOpen(); err != nil {
		return err
	}
	m.mu.Lock()
	if len(m.live) != 0 {
		m.mu.Unlock()
		return fmt.Errorf("restore requires a fresh manager with no live sessions")
	}
	m.mu.Unlock()
	records, err := m.store.List()
	if err != nil {
		return err
	}
	var failures []error
	for _, record := range records {
		if record.Desired {
			continue
		}
		if err := m.adapter.RecoverUnmount(ctx, record.Spec); err != nil {
			failures = append(failures, fmt.Errorf("recover unmount %s: %w", record.Spec.ID, err))
			continue
		}
		if err := m.store.DeleteUnmounted(record.Spec.ID); err != nil && !errors.Is(err, ErrNotFound) {
			failures = append(failures, fmt.Errorf("finish recovered unmount %s: %w", record.Spec.ID, err))
		}
	}
	for _, record := range records {
		if !record.Desired {
			continue
		}
		if _, err := m.mount(ctx, record.Spec); err != nil {
			failures = append(failures, fmt.Errorf("restore mount %s: %w", record.Spec.ID, err))
		}
	}
	return errors.Join(failures...)
}

// Shutdown stops live platform sessions without changing durable desired
// state, so the next daemon process can Restore them.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	ids := make([]string, 0, len(m.live))
	for id := range m.live {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	liveMounts := make(map[string]liveMount, len(ids))
	for _, id := range ids {
		liveMounts[id] = m.live[id]
	}
	m.mu.Unlock()
	var failures []error
	for _, id := range ids {
		live := liveMounts[id]
		if err := live.session.Unmount(ctx); err != nil {
			failures = append(failures, fmt.Errorf("shutdown mount %s: %w", id, err))
			m.mu.Lock()
			m.errors[id] = err.Error()
			m.mu.Unlock()
			continue
		}
		m.mu.Lock()
		if current, ok := m.live[id]; ok && current.token == live.token {
			delete(m.live, id)
		}
		delete(m.errors, id)
		m.mu.Unlock()
	}
	m.mu.Lock()
	m.closed = true
	leaseUnlock := m.leaseUnlock
	m.leaseUnlock = nil
	m.mu.Unlock()
	if leaseUnlock != nil {
		if err := leaseUnlock(); err != nil {
			failures = append(failures, fmt.Errorf("release mount manager lease: %w", err))
		}
	}
	return errors.Join(failures...)
}

func (m *Manager) List() ([]Status, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, ErrManagerClosed
	}
	m.mu.Unlock()
	records, err := m.store.List()
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	statuses := make([]Status, 0, len(records))
	for _, record := range records {
		live, active := m.live[record.Spec.ID]
		statuses = append(statuses, m.statusLocked(record, live, active))
	}
	return statuses, nil
}

func (m *Manager) ensureOpen() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return ErrManagerClosed
	}
	return nil
}

func (m *Manager) selectView(ctx context.Context, spec Spec) (filesystemservice.View, error) {
	view, err := m.selector.SelectView(ctx, spec)
	if err != nil {
		return filesystemservice.View{}, err
	}
	if view.DatasetID != spec.DatasetID || view.Branch != spec.Branch || view.EncryptionEpoch != spec.EncryptionEpoch || !view.Root.Defined() {
		return filesystemservice.View{}, fmt.Errorf("local view selector returned a mismatched mount identity")
	}
	return view, nil
}

func (m *Manager) statusLocked(record Record, live liveMount, active bool) Status {
	status := Status{Spec: record.Spec, Desired: record.Desired, Active: active, Adapter: m.adapter.Name(), LastError: m.errors[record.Spec.ID]}
	if live.view.Root.Defined() {
		status.SelectedRoot = live.view.Root.String()
		status.Revision = live.view.Revision
	}
	return status
}

func (m *Manager) monitor(id string, token uint64, done <-chan error) {
	err, open := <-done
	if !open || err == nil {
		err = fmt.Errorf("mount session ended")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	live, ok := m.live[id]
	if !ok || live.token != token {
		return
	}
	delete(m.live, id)
	m.errors[id] = err.Error()
}

type boundFilesystem struct {
	service ViewFilesystem
	view    filesystemservice.View
}

func (f boundFilesystem) Stat(ctx context.Context, path string) (filesystemservice.Info, error) {
	return f.service.Stat(ctx, f.view, path)
}

func (f boundFilesystem) ReadDir(ctx context.Context, path string) ([]filesystemservice.DirEntry, error) {
	return f.service.ReadDir(ctx, f.view, path)
}

func (f boundFilesystem) Open(ctx context.Context, path string) (ReadHandle, error) {
	return f.service.Open(ctx, f.view, path)
}

var _ ReadHandle = (*filesystemservice.Handle)(nil)

// Package mount owns durable, daemon-managed lifecycle for platform filesystem
// adapters. Read-only is the default; write-back requires an explicit policy
// and session-owned capability. Platform adapters translate syscalls only;
// trust selection, verification, and persistence policy remain above them.
package mount

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	filesystemservice "github.com/dewebprotocol/malt-client/filesystem/service"
)

var (
	ErrManagerClosed          = errors.New("mount manager is closed")
	ErrWritePolicyUnavailable = errors.New("mount write policy is unavailable")
	ErrWritePolicyViolation   = errors.New("mount write policy was violated")
	ErrMountCleanupPending    = errors.New("mount cleanup is pending")
)

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

// SyncResult keeps local journal durability, verified remote persistence, and
// trusted-root acceptance distinct at the platform boundary.
type SyncResult struct {
	LocalDurable    bool
	RemotePersisted bool
	CandidateRoot   string
	RootAccepted    bool
}

// WritableFilesystem is an optional capability exposed only for an explicitly
// write-back Spec. Every successful mutation is already locally durable when
// it returns. Sync may additionally verify remote candidate persistence. It
// must always report RootAccepted=false because mount I/O has no accepted-root
// promotion capability.
type WritableFilesystem interface {
	ReadOnlyFilesystem
	Create(context.Context, string) (filesystemservice.Info, error)
	WriteAt(context.Context, string, uint64, []byte) (filesystemservice.Info, error)
	Truncate(context.Context, string, uint64) (filesystemservice.Info, error)
	Mkdir(context.Context, string) (filesystemservice.Info, error)
	Rename(context.Context, string, string) error
	Unlink(context.Context, string) error
	RemoveDir(context.Context, string) error
	Sync(context.Context) (SyncResult, error)
}

// WritableBinding owns the state leases and other resources for one exact
// write-back mount. Close must be idempotent and retryable after an error;
// ownership remains with Manager until Close succeeds. Manager, rather than
// the platform adapter, owns its lifetime.
type WritableBinding interface {
	WritableFilesystem
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
// stale mount owned by the same Spec or return a recoverable error. A nil
// Session with an error guarantees that no platform syscall path remains; a
// non-nil Session returned with an error transfers rollback ownership to
// Manager.
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

// WritableViewFilesystem is the application-facing write capability. Manager
// supplies the complete normalized mount policy and immutable locally accepted
// View, then owns the returned binding for exactly one platform session.
type WritableViewFilesystem interface {
	ViewFilesystem
	BindWritable(context.Context, Spec, filesystemservice.View) (WritableBinding, error)
}

type liveMount struct {
	session      Session
	view         filesystemservice.View
	binding      *managedWritableBinding
	token        uint64
	cleanupOnly  bool
	needsUnmount bool
}

type Manager struct {
	opGate      chan struct{}
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
		opGate: make(chan struct{}, 1), live: map[string]liveMount{}, errors: map[string]string{},
		nextToken: 1, leaseUnlock: leaseUnlock,
	}, nil
}

// Mount persists desired state before platform I/O. A crash after this point
// is repaired by Restore, which retries the exact durable Spec.
func (m *Manager) Mount(ctx context.Context, spec Spec) (Status, error) {
	if err := m.acquireOperation(ctx); err != nil {
		return Status{}, err
	}
	defer m.releaseOperation()
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
		if !current.cleanupOnly {
			status := m.statusLocked(record, current, true)
			m.mu.Unlock()
			return status, nil
		}
		m.mu.Unlock()
		if err := m.cleanupLive(ctx, record.Spec.ID, current); err != nil {
			err = fmt.Errorf("%w: %v", ErrMountCleanupPending, err)
			return m.failureStatus(record, current.view, err), err
		}
		m.mu.Lock()
		if latest, ok := m.live[record.Spec.ID]; ok && latest.token == current.token {
			delete(m.live, record.Spec.ID)
		}
		m.mu.Unlock()
	} else {
		m.mu.Unlock()
	}
	view, err := m.selectView(ctx, record.Spec)
	if err != nil {
		return m.failureStatus(record, filesystemservice.View{}, err), err
	}
	var bound ReadOnlyFilesystem = boundFilesystem{service: m.filesystem, view: view}
	var binding *managedWritableBinding
	if record.Spec.WritePolicy == WriteBack {
		writable, ok := m.filesystem.(WritableViewFilesystem)
		if !ok {
			err := fmt.Errorf("%w: filesystem does not implement write-back", ErrWritePolicyUnavailable)
			return m.failureStatus(record, view, err), err
		}
		created, bindErr := writable.BindWritable(ctx, record.Spec, view)
		if isNilInterface(created) {
			created = nil
		}
		if bindErr != nil {
			if created != nil {
				binding = &managedWritableBinding{WritableBinding: created}
				bindErr = errors.Join(bindErr, m.rollbackFailedMount(ctx, record.Spec.ID, view, nil, nil, binding))
			}
			err := fmt.Errorf("%w: bind write-back filesystem: %w", ErrWritePolicyUnavailable, bindErr)
			return m.failureStatus(record, view, err), err
		}
		if created == nil {
			err := fmt.Errorf("%w: binder returned nil write-back filesystem", ErrWritePolicyUnavailable)
			return m.failureStatus(record, view, err), err
		}
		binding = &managedWritableBinding{WritableBinding: created}
		bound = policyWritableFilesystem{WritableFilesystem: binding}
	}
	session, err := m.adapter.Mount(ctx, record.Spec, bound)
	if isNilInterface(session) {
		session = nil
	}
	var done <-chan error
	if session != nil {
		done = session.Done()
	}
	if err != nil {
		err = errors.Join(err, m.rollbackFailedMount(ctx, record.Spec.ID, view, session, done, binding))
		return m.failureStatus(record, view, err), err
	}
	if session == nil || done == nil {
		err := errors.Join(
			fmt.Errorf("mount adapter returned an incomplete session"),
			m.rollbackFailedMount(ctx, record.Spec.ID, view, session, done, binding),
		)
		return m.failureStatus(record, view, err), err
	}
	m.mu.Lock()
	token := m.nextToken
	m.nextToken++
	live := liveMount{session: session, view: view, binding: binding, token: token, needsUnmount: true}
	m.live[record.Spec.ID] = live
	delete(m.errors, record.Spec.ID)
	status := m.statusLocked(record, live, true)
	m.mu.Unlock()
	go m.monitor(record.Spec.ID, token, done)
	return status, nil
}

// Unmount first persists a pending-unmount tombstone. The record is deleted
// only after the live or recovered platform mount is confirmed unmounted.
func (m *Manager) Unmount(ctx context.Context, id string) error {
	if err := m.acquireOperation(ctx); err != nil {
		return err
	}
	defer m.releaseOperation()
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
		err = m.cleanupLive(ctx, record.Spec.ID, live)
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
	if err := m.acquireOperation(ctx); err != nil {
		return err
	}
	defer m.releaseOperation()
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
	if err := m.acquireOperation(ctx); err != nil {
		return err
	}
	defer m.releaseOperation()
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
		err := m.cleanupLive(ctx, id, live)
		if err != nil {
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
	if len(failures) != 0 {
		return errors.Join(failures...)
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

// acquireOperation serializes lifecycle I/O without making shutdown wait on
// an uninterruptible mutex. The operation holding the gate receives the same
// request/daemon context and is expected to stop platform I/O when canceled.
func (m *Manager) acquireOperation(ctx context.Context) error {
	if ctx == nil {
		return fmt.Errorf("mount operation context is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case m.opGate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			m.releaseOperation()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) releaseOperation() { <-m.opGate }

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
	status := Status{Spec: record.Spec, Desired: record.Desired, Active: active && !live.cleanupOnly, Adapter: m.adapter.Name(), LastError: m.errors[record.Spec.ID]}
	if live.view.Root.Defined() {
		status.SelectedRoot = live.view.Root.String()
		status.Revision = live.view.Revision
	}
	return status
}

func (m *Manager) failureStatus(record Record, view filesystemservice.View, err error) Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errors[record.Spec.ID] = err.Error()
	live, ok := m.live[record.Spec.ID]
	if !ok {
		live.view = view
	}
	return m.statusLocked(record, live, ok)
}

func (m *Manager) cleanupLive(ctx context.Context, id string, live liveMount) error {
	if live.session != nil && live.needsUnmount {
		if err := live.session.Unmount(ctx); err != nil {
			return err
		}
		live.needsUnmount = false
		live.cleanupOnly = true
		m.markDetached(id, live)
	}
	return live.binding.close()
}

func (m *Manager) markDetached(id string, live liveMount) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.live[id]; ok && current.token == live.token {
		current.cleanupOnly = true
		current.needsUnmount = false
		m.live[id] = current
	}
}

// rollbackFailedMount keeps cleanup ownership whenever either platform detach
// or binding Close is not confirmed. A later Mount, Unmount, or Shutdown can
// retry without exposing the binding as an active filesystem.
func (m *Manager) rollbackFailedMount(ctx context.Context, id string, view filesystemservice.View, session Session, done <-chan error, binding *managedWritableBinding) error {
	needsUnmount := session != nil
	if needsUnmount {
		if err := session.Unmount(ctx); err != nil {
			m.retainCleanup(id, view, session, done, binding, true)
			return fmt.Errorf("detach failed mount: %w", err)
		}
		needsUnmount = false
	}
	if err := binding.close(); err != nil {
		m.retainCleanup(id, view, session, done, binding, needsUnmount)
		return fmt.Errorf("close failed mount binding: %w", err)
	}
	return nil
}

func (m *Manager) retainCleanup(id string, view filesystemservice.View, session Session, done <-chan error, binding *managedWritableBinding, needsUnmount bool) {
	m.mu.Lock()
	token := m.nextToken
	m.nextToken++
	live := liveMount{
		session: session, view: view, binding: binding, token: token,
		cleanupOnly: true, needsUnmount: needsUnmount,
	}
	m.live[id] = live
	m.mu.Unlock()
	if done != nil {
		go m.monitor(id, token, done)
	}
}

func (m *Manager) monitor(id string, token uint64, done <-chan error) {
	err, open := <-done
	if !open || err == nil {
		err = fmt.Errorf("mount session ended")
	}
	m.mu.Lock()
	live, ok := m.live[id]
	if !ok || live.token != token {
		m.mu.Unlock()
		return
	}
	live.cleanupOnly = true
	live.needsUnmount = false
	m.live[id] = live
	m.errors[id] = err.Error()
	m.mu.Unlock()
	closeErr := live.binding.close()
	m.mu.Lock()
	if current, active := m.live[id]; active && current.token == token {
		if closeErr == nil {
			delete(m.live, id)
		} else {
			m.errors[id] = errors.Join(err, closeErr).Error()
		}
	}
	m.mu.Unlock()
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

type managedWritableBinding struct {
	WritableBinding
	mu     sync.Mutex
	closed bool
}

func (b *managedWritableBinding) close() error {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	if err := b.WritableBinding.Close(); err != nil {
		return err
	}
	b.closed = true
	return nil
}

func isNilInterface(value any) bool {
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

type policyWritableFilesystem struct{ WritableFilesystem }

func (f policyWritableFilesystem) Sync(ctx context.Context) (SyncResult, error) {
	result, err := f.WritableFilesystem.Sync(ctx)
	if result.RootAccepted {
		return SyncResult{}, errors.Join(err, fmt.Errorf("%w: mount sync reported accepted-root promotion", ErrWritePolicyViolation))
	}
	if result.RemotePersisted && (!result.LocalDurable || strings.TrimSpace(result.CandidateRoot) == "") {
		return SyncResult{}, errors.Join(err, fmt.Errorf("%w: remote persistence lacks local durability or candidate root", ErrWritePolicyViolation))
	}
	return result, err
}

var _ ReadHandle = (*filesystemservice.Handle)(nil)
var _ WritableFilesystem = policyWritableFilesystem{}

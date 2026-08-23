package mount

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	filesystemservice "github.com/dewebprotocol/malt-client/filesystem/service"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

type fakeViewFilesystem struct {
	mu          sync.Mutex
	lastView    filesystemservice.View
	closeErrors []error
	closeCalls  int
}

type fakeLeasedViewFilesystem struct {
	*fakeViewFilesystem
	acquireCalls  int
	releaseCalls  int
	releaseErrors []error
}

func (f *fakeLeasedViewFilesystem) AcquireView(ctx context.Context, _ filesystemservice.View) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acquireCalls++
	return nil
}

func (f *fakeLeasedViewFilesystem) ReleaseView(filesystemservice.View) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCalls++
	if f.releaseCalls <= len(f.releaseErrors) {
		return f.releaseErrors[f.releaseCalls-1]
	}
	return nil
}

func (f *fakeLeasedViewFilesystem) leaseCalls() (int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acquireCalls, f.releaseCalls
}

func (f *fakeViewFilesystem) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closeCalls++
	if f.closeCalls <= len(f.closeErrors) {
		return f.closeErrors[f.closeCalls-1]
	}
	return nil
}

type fakeWritableViewFilesystem struct {
	*fakeViewFilesystem
	lastSpec       Spec
	lastBindView   filesystemservice.View
	lastMutation   string
	bindErr        error
	partialError   bool
	returnNil      bool
	returnTypedNil bool
	syncResult     *SyncResult
	closeErrors    []error
	binding        *fakeWritableBinding
}

type fakeWritableBinding struct {
	parent      *fakeWritableViewFilesystem
	view        filesystemservice.View
	closeErrors []error
	closeCalls  int
}

func (f *fakeWritableViewFilesystem) BindWritable(_ context.Context, spec Spec, view filesystemservice.View) (WritableBinding, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastSpec = spec
	f.lastBindView = view
	if f.returnNil {
		return nil, f.bindErr
	}
	if f.returnTypedNil {
		var binding *fakeWritableBinding
		return binding, f.bindErr
	}
	f.binding = &fakeWritableBinding{parent: f, view: view, closeErrors: append([]error(nil), f.closeErrors...)}
	if f.bindErr != nil && !f.partialError {
		return nil, f.bindErr
	}
	return f.binding, f.bindErr
}

func (f *fakeWritableBinding) mutation(name string, info filesystemservice.Info) (filesystemservice.Info, error) {
	f.parent.mu.Lock()
	defer f.parent.mu.Unlock()
	f.parent.lastView = f.view
	f.parent.lastMutation = name
	return info, nil
}

func (f *fakeWritableBinding) Create(_ context.Context, name string) (filesystemservice.Info, error) {
	return f.mutation("create:"+name, filesystemservice.Info{Path: name, Kind: "file"})
}

func (f *fakeWritableBinding) WriteAt(_ context.Context, name string, _ uint64, _ []byte) (filesystemservice.Info, error) {
	return f.mutation("write:"+name, filesystemservice.Info{Path: name, Kind: "file", Size: 1})
}

func (f *fakeWritableBinding) Truncate(_ context.Context, name string, size uint64) (filesystemservice.Info, error) {
	return f.mutation("truncate:"+name, filesystemservice.Info{Path: name, Kind: "file", Size: size})
}

func (f *fakeWritableBinding) Mkdir(_ context.Context, name string) (filesystemservice.Info, error) {
	return f.mutation("mkdir:"+name, filesystemservice.Info{Path: name, Kind: "directory"})
}

func (f *fakeWritableBinding) Rename(_ context.Context, source, destination string) error {
	_, err := f.mutation("rename:"+source+":"+destination, filesystemservice.Info{})
	return err
}

func (f *fakeWritableBinding) Unlink(_ context.Context, name string) error {
	_, err := f.mutation("unlink:"+name, filesystemservice.Info{})
	return err
}

func (f *fakeWritableBinding) RemoveDir(_ context.Context, name string) error {
	_, err := f.mutation("rmdir:"+name, filesystemservice.Info{})
	return err
}

func (f *fakeWritableBinding) Sync(_ context.Context) (SyncResult, error) {
	_, err := f.mutation("sync", filesystemservice.Info{})
	if f.parent.syncResult != nil {
		return *f.parent.syncResult, err
	}
	return SyncResult{LocalDurable: true, CandidateRoot: "candidate"}, err
}

func (f *fakeWritableBinding) Stat(ctx context.Context, path string) (filesystemservice.Info, error) {
	return f.parent.Stat(ctx, f.view, path)
}

func (f *fakeWritableBinding) ReadDir(ctx context.Context, path string) ([]filesystemservice.DirEntry, error) {
	return f.parent.ReadDir(ctx, f.view, path)
}

func (f *fakeWritableBinding) Open(ctx context.Context, path string) (ReadHandle, error) {
	return f.parent.Open(ctx, f.view, path)
}

func (f *fakeWritableBinding) Close() error {
	f.parent.mu.Lock()
	defer f.parent.mu.Unlock()
	f.closeCalls++
	if f.closeCalls <= len(f.closeErrors) {
		return f.closeErrors[f.closeCalls-1]
	}
	return nil
}

func (f *fakeWritableBinding) closeCallCount() int {
	f.parent.mu.Lock()
	defer f.parent.mu.Unlock()
	return f.closeCalls
}

func (f *fakeViewFilesystem) Stat(_ context.Context, view filesystemservice.View, path string) (filesystemservice.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastView = view
	return filesystemservice.Info{Path: path, Kind: "file"}, nil
}

func (*fakeViewFilesystem) ReadDir(context.Context, filesystemservice.View, string) ([]filesystemservice.DirEntry, error) {
	return nil, nil
}

func (*fakeViewFilesystem) Open(context.Context, filesystemservice.View, string) (*filesystemservice.Handle, error) {
	return nil, errors.New("not used")
}

type fakeAdapter struct {
	mu                sync.Mutex
	mountErr          error
	sessionOnError    bool
	typedNilSession   bool
	incomplete        bool
	sessionUnmountErr error
	suppressDone      bool
	recoverErr        error
	mountCalls        int
	recoverCalls      int
	lastFilesystem    ReadOnlyFilesystem
	lastSession       *fakeSession
}

func (*fakeAdapter) Name() string { return "fake-platform" }

func (a *fakeAdapter) Mount(_ context.Context, _ Spec, filesystem ReadOnlyFilesystem) (Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mountCalls++
	a.lastFilesystem = filesystem
	if a.typedNilSession {
		var session *fakeSession
		return session, a.mountErr
	}
	session := &fakeSession{done: make(chan error, 1), nilDone: a.incomplete, unmountErr: a.sessionUnmountErr}
	session.suppressDone = a.suppressDone
	a.lastSession = session
	if a.mountErr != nil {
		if a.sessionOnError {
			return session, a.mountErr
		}
		return nil, a.mountErr
	}
	return session, nil
}

func (a *fakeAdapter) RecoverUnmount(context.Context, Spec) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.recoverCalls++
	return a.recoverErr
}

type fakeSession struct {
	mu           sync.Mutex
	done         chan error
	nilDone      bool
	doneCalls    int
	unmountErr   error
	unmounted    bool
	unmountCalls int
	suppressDone bool
	afterUnmount func()
}

func (s *fakeSession) Unmount(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.unmountCalls++
	if s.unmountErr != nil {
		return s.unmountErr
	}
	if !s.unmounted {
		s.unmounted = true
		if !s.suppressDone {
			s.done <- nil
			close(s.done)
		}
		if s.afterUnmount != nil {
			s.afterUnmount()
		}
	}
	return nil
}

func (s *fakeSession) Done() <-chan error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.doneCalls++
	if s.nilDone {
		return nil
	}
	return s.done
}

func (s *fakeSession) callCounts() (done, unmount int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doneCalls, s.unmountCalls
}

type callbackAdapter struct {
	onMount   func() error
	onUnmount func() error
	session   *callbackSession
}

func (*callbackAdapter) Name() string { return "callback-platform" }

func (a *callbackAdapter) Mount(context.Context, Spec, ReadOnlyFilesystem) (Session, error) {
	if a.onMount != nil {
		if err := a.onMount(); err != nil {
			return nil, err
		}
	}
	a.session = &callbackSession{done: make(chan error, 1), callback: a.onUnmount}
	return a.session, nil
}

func (*callbackAdapter) RecoverUnmount(context.Context, Spec) error { return nil }

type callbackSession struct {
	done     chan error
	callback func() error
}

func (s *callbackSession) Unmount(context.Context) error {
	if s.callback != nil {
		if err := s.callback(); err != nil {
			return err
		}
	}
	s.done <- nil
	close(s.done)
	return nil
}

func (s *callbackSession) Done() <-chan error { return s.done }

type blockingMountAdapter struct {
	entered chan struct{}
	release chan struct{}
}

func (*blockingMountAdapter) Name() string { return "blocking-platform" }

func (a *blockingMountAdapter) Mount(ctx context.Context, _ Spec, _ ReadOnlyFilesystem) (Session, error) {
	close(a.entered)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-a.release:
		return nil, errors.New("released blocked mount")
	}
}

func (*blockingMountAdapter) RecoverUnmount(context.Context, Spec) error { return nil }

func TestManagerPersistsBeforeMountAndRestoresAfterDaemonRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mounts.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec(t, "docs")
	view := testMountView(t, spec)
	filesystem := &fakeViewFilesystem{}
	adapter := &fakeAdapter{mountErr: errors.New("platform unavailable")}
	manager := newTestManager(t, store, adapter, filesystem, view)

	status, err := manager.Mount(t.Context(), spec)
	if err == nil || !status.Desired || status.Active {
		t.Fatalf("failed Mount status = %#v, %v", status, err)
	}
	if record, err := store.Get(spec.ID); err != nil || !record.Desired {
		t.Fatalf("failed platform mount was not durable for retry: %#v, %v", record, err)
	}
	adapter.mountErr = nil
	status, err = manager.Mount(t.Context(), spec)
	if err != nil || !status.Active || status.SelectedRoot != view.Root.String() {
		t.Fatalf("retried Mount status = %#v, %v", status, err)
	}
	if _, err := adapter.lastFilesystem.Stat(t.Context(), "docs/file.txt"); err != nil {
		t.Fatal(err)
	}
	if filesystem.lastView != view {
		t.Fatalf("platform filesystem view = %#v, want %#v", filesystem.lastView, view)
	}

	if err := manager.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	if record, err := store.Get(spec.ID); err != nil || !record.Desired {
		t.Fatalf("shutdown changed desired state: %#v, %v", record, err)
	}
	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	restartAdapter := &fakeAdapter{}
	restarted := newTestManager(t, reopened, restartAdapter, filesystem, view)
	if err := restarted.Restore(t.Context()); err != nil {
		t.Fatal(err)
	}
	if statuses, err := restarted.List(); err != nil || len(statuses) != 1 || !statuses[0].Active {
		t.Fatalf("restored statuses = %#v, %v", statuses, err)
	}
	if err := restarted.Unmount(t.Context(), spec.ID); err != nil {
		t.Fatal(err)
	}
	if records, err := reopened.List(); err != nil || len(records) != 0 {
		t.Fatalf("unmounted records = %#v, %v", records, err)
	}
}

func TestManagerExposesWritesOnlyForExplicitCapablePolicy(t *testing.T) {
	writableSpec := testSpec(t, "writable")
	writableSpec.WritePolicy = WriteBack
	writableSpec.LayoutPolicy = LayoutHybridV1
	writableSpec.ConflictPolicy = ConflictPreserveLocal
	view := testMountView(t, writableSpec)
	store, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	filesystem := &fakeWritableViewFilesystem{fakeViewFilesystem: &fakeViewFilesystem{}}
	adapter := &fakeAdapter{}
	manager := newTestManager(t, store, adapter, filesystem, view)
	if _, err := manager.Mount(t.Context(), writableSpec); err != nil {
		t.Fatal(err)
	}
	if doneCalls, _ := adapter.lastSession.callCounts(); doneCalls != 1 {
		t.Fatalf("Session.Done calls=%d, want 1", doneCalls)
	}
	writable, ok := adapter.lastFilesystem.(WritableFilesystem)
	if !ok {
		t.Fatalf("write-back mount received %T", adapter.lastFilesystem)
	}
	if _, err := writable.WriteAt(t.Context(), "docs/file.txt", 4, []byte("x")); err != nil {
		t.Fatal(err)
	}
	if filesystem.lastView != view || filesystem.lastMutation != "write:docs/file.txt" {
		t.Fatalf("bound write view=%#v mutation=%q", filesystem.lastView, filesystem.lastMutation)
	}
	if filesystem.lastSpec != writableSpec || filesystem.lastBindView != view {
		t.Fatalf("writable binding spec=%#v view=%#v", filesystem.lastSpec, filesystem.lastBindView)
	}
	if result, err := writable.Sync(t.Context()); err != nil || !result.LocalDurable || result.RemotePersisted || result.RootAccepted {
		t.Fatalf("bound Sync=%#v err=%v", result, err)
	}
	if err := manager.Unmount(t.Context(), writableSpec.ID); err != nil {
		t.Fatal(err)
	}
	filesystem.mu.Lock()
	closeCalls := filesystem.binding.closeCalls
	filesystem.mu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("writable binding Close calls=%d, want 1", closeCalls)
	}

	readOnlyStore, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	readOnlyAdapter := &fakeAdapter{}
	readOnlySpec := testSpec(t, "readonly")
	readOnlyManager := newTestManager(t, readOnlyStore, readOnlyAdapter, filesystem, testMountView(t, readOnlySpec))
	if _, err := readOnlyManager.Mount(t.Context(), readOnlySpec); err != nil {
		t.Fatal(err)
	}
	if _, ok := readOnlyAdapter.lastFilesystem.(WritableFilesystem); ok {
		t.Fatal("read-only mount received a writable capability")
	}
}

func TestManagerRejectsWritableBinderFailuresAndSyncTrustViolation(t *testing.T) {
	spec := testSpec(t, "writable-failure")
	spec.WritePolicy = WriteBack
	spec.LayoutPolicy = LayoutFlatV1
	spec.ConflictPolicy = ConflictPreserveLocal
	view := testMountView(t, spec)

	for _, test := range []struct {
		name       string
		filesystem *fakeWritableViewFilesystem
	}{
		{name: "error", filesystem: &fakeWritableViewFilesystem{fakeViewFilesystem: &fakeViewFilesystem{}, bindErr: errors.New("state lease busy")}},
		{name: "nil", filesystem: &fakeWritableViewFilesystem{fakeViewFilesystem: &fakeViewFilesystem{}, returnNil: true}},
		{name: "typed-nil", filesystem: &fakeWritableViewFilesystem{fakeViewFilesystem: &fakeViewFilesystem{}, returnTypedNil: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
			if err != nil {
				t.Fatal(err)
			}
			adapter := &fakeAdapter{}
			manager := newTestManager(t, store, adapter, test.filesystem, view)
			status, err := manager.Mount(t.Context(), spec)
			if !errors.Is(err, ErrWritePolicyUnavailable) || !status.Desired || status.Active || adapter.mountCalls != 0 {
				t.Fatalf("binder failure status=%#v err=%v calls=%d", status, err, adapter.mountCalls)
			}
		})
	}

	store, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	result := SyncResult{LocalDurable: true, RemotePersisted: true, CandidateRoot: "candidate", RootAccepted: true}
	filesystem := &fakeWritableViewFilesystem{fakeViewFilesystem: &fakeViewFilesystem{}, syncResult: &result}
	adapter := &fakeAdapter{}
	manager := newTestManager(t, store, adapter, filesystem, view)
	if _, err := manager.Mount(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	writable := adapter.lastFilesystem.(WritableFilesystem)
	if result, err := writable.Sync(t.Context()); !errors.Is(err, ErrWritePolicyViolation) || result != (SyncResult{}) {
		t.Fatalf("accepted-root Sync=%#v err=%v", result, err)
	}
}

func TestManagerRetainsPartialBinderUntilCloseRetrySucceeds(t *testing.T) {
	spec := testSpec(t, "partial-binding")
	spec.WritePolicy = WriteBack
	spec.LayoutPolicy = LayoutFlatV1
	spec.ConflictPolicy = ConflictPreserveLocal
	view := testMountView(t, spec)
	temporary := errors.New("temporary close failure")
	filesystem := &fakeWritableViewFilesystem{
		fakeViewFilesystem: &fakeViewFilesystem{},
		bindErr:            errors.New("binding initialization failed"), partialError: true,
		closeErrors: []error{temporary, nil},
	}
	store, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{}
	manager := newTestManager(t, store, adapter, filesystem, view)
	status, err := manager.Mount(t.Context(), spec)
	if !errors.Is(err, ErrWritePolicyUnavailable) || !errors.Is(err, temporary) || !status.Desired || status.Active || adapter.mountCalls != 0 {
		t.Fatalf("partial binding status=%#v err=%v calls=%d", status, err, adapter.mountCalls)
	}
	partial := filesystem.binding
	if calls := partial.closeCallCount(); calls != 1 {
		t.Fatalf("initial partial Close calls=%d, want 1", calls)
	}
	if err := manager.Unmount(t.Context(), spec.ID); err != nil {
		t.Fatal(err)
	}
	if calls := partial.closeCallCount(); calls != 2 {
		t.Fatalf("retried partial Close calls=%d, want 2", calls)
	}
	if _, err := store.Get(spec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("partial binding record remained: %v", err)
	}
}

func TestManagerRetainsFailedOrIncompletePlatformSessionUntilDetach(t *testing.T) {
	for _, test := range []struct {
		name    string
		adapter *fakeAdapter
	}{
		{
			name: "session-and-error",
			adapter: &fakeAdapter{mountErr: errors.New("mount postflight failed"), sessionOnError: true,
				sessionUnmountErr: errors.New("detach ambiguous")},
		},
		{
			name:    "incomplete-session",
			adapter: &fakeAdapter{incomplete: true, sessionUnmountErr: errors.New("detach ambiguous")},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			spec := testSpec(t, test.name)
			spec.WritePolicy = WriteBack
			spec.LayoutPolicy = LayoutHybridV1
			spec.ConflictPolicy = ConflictPreserveLocal
			view := testMountView(t, spec)
			store, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
			if err != nil {
				t.Fatal(err)
			}
			filesystem := &fakeWritableViewFilesystem{fakeViewFilesystem: &fakeViewFilesystem{}}
			manager := newTestManager(t, store, test.adapter, filesystem, view)
			status, err := manager.Mount(t.Context(), spec)
			if err == nil || !status.Desired || status.Active {
				t.Fatalf("ambiguous mount status=%#v err=%v", status, err)
			}
			binding := filesystem.binding
			if calls := binding.closeCallCount(); calls != 0 {
				t.Fatalf("binding closed before detach confirmation: calls=%d", calls)
			}
			test.adapter.lastSession.unmountErr = nil
			if err := manager.Unmount(t.Context(), spec.ID); err != nil {
				t.Fatal(err)
			}
			if calls := binding.closeCallCount(); calls != 1 {
				t.Fatalf("binding Close calls after detach=%d, want 1", calls)
			}
		})
	}
}

func TestManagerNormalizesTypedNilSessionWithoutLeakingBinding(t *testing.T) {
	for _, mountErr := range []error{nil, errors.New("typed nil mount failure")} {
		name := "success-result"
		if mountErr != nil {
			name = "error-result"
		}
		t.Run(name, func(t *testing.T) {
			spec := testSpec(t, "typed-nil-session-"+name)
			spec.WritePolicy = WriteBack
			spec.LayoutPolicy = LayoutFlatV1
			spec.ConflictPolicy = ConflictPreserveLocal
			view := testMountView(t, spec)
			store, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
			if err != nil {
				t.Fatal(err)
			}
			filesystem := &fakeWritableViewFilesystem{fakeViewFilesystem: &fakeViewFilesystem{}}
			adapter := &fakeAdapter{typedNilSession: true, mountErr: mountErr}
			manager := newTestManager(t, store, adapter, filesystem, view)
			status, err := manager.Mount(t.Context(), spec)
			if err == nil || !status.Desired || status.Active {
				t.Fatalf("typed-nil session status=%#v err=%v", status, err)
			}
			if calls := filesystem.binding.closeCallCount(); calls != 1 {
				t.Fatalf("typed-nil binding Close calls=%d, want 1", calls)
			}
		})
	}
}

func TestManagerRecordsDetachBeforeRetryableBindingClose(t *testing.T) {
	spec := testSpec(t, "detached-close-retry")
	spec.WritePolicy = WriteBack
	spec.LayoutPolicy = LayoutHybridV1
	spec.ConflictPolicy = ConflictPreserveLocal
	view := testMountView(t, spec)
	temporary := errors.New("temporary close failure")
	filesystem := &fakeWritableViewFilesystem{
		fakeViewFilesystem: &fakeViewFilesystem{}, closeErrors: []error{temporary, nil},
	}
	store, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{suppressDone: true}
	manager := newTestManager(t, store, adapter, filesystem, view)
	if _, err := manager.Mount(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	if err := manager.Unmount(t.Context(), spec.ID); !errors.Is(err, temporary) {
		t.Fatalf("first Unmount error=%v, want %v", err, temporary)
	}
	statuses, err := manager.List()
	if err != nil || len(statuses) != 1 || statuses[0].Active || statuses[0].Desired {
		t.Fatalf("detached cleanup-only status=%#v err=%v", statuses, err)
	}
	if _, unmountCalls := adapter.lastSession.callCounts(); unmountCalls != 1 {
		t.Fatalf("first detach calls=%d, want 1", unmountCalls)
	}
	if err := manager.Unmount(t.Context(), spec.ID); err != nil {
		t.Fatal(err)
	}
	if _, unmountCalls := adapter.lastSession.callCounts(); unmountCalls != 1 {
		t.Fatalf("retry detached again: calls=%d", unmountCalls)
	}
	adapter.lastSession.done <- nil
	close(adapter.lastSession.done)
}

func TestManagerRetriesBindingCloseAfterUnexpectedSessionExit(t *testing.T) {
	spec := testSpec(t, "close-retry")
	spec.WritePolicy = WriteBack
	spec.LayoutPolicy = LayoutFlatV1
	spec.ConflictPolicy = ConflictPreserveLocal
	view := testMountView(t, spec)
	temporary := errors.New("temporary close failure")
	filesystem := &fakeWritableViewFilesystem{
		fakeViewFilesystem: &fakeViewFilesystem{}, closeErrors: []error{temporary, nil},
	}
	store, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{}
	manager := newTestManager(t, store, adapter, filesystem, view)
	if _, err := manager.Mount(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	original := filesystem.binding
	adapter.lastSession.done <- errors.New("driver disconnected")
	close(adapter.lastSession.done)
	deadline := time.Now().Add(time.Second)
	for {
		statuses, statusErr := manager.List()
		if statusErr != nil {
			t.Fatal(statusErr)
		}
		calls := original.closeCallCount()
		if calls == 1 && len(statuses) == 1 && !statuses[0].Active && strings.Contains(statuses[0].LastError, temporary.Error()) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("close failure not retained: calls=%d statuses=%#v", calls, statuses)
		}
		time.Sleep(time.Millisecond)
	}
	filesystem.mu.Lock()
	filesystem.closeErrors = nil
	filesystem.mu.Unlock()
	if status, err := manager.Mount(t.Context(), spec); err != nil || !status.Active {
		t.Fatalf("Mount after cleanup retry status=%#v err=%v", status, err)
	}
	if calls := original.closeCallCount(); calls != 2 {
		t.Fatalf("original binding Close calls=%d, want 2", calls)
	}
}

func TestManagerPersistsUnsupportedWriteBackForRetryWithoutCallingAdapter(t *testing.T) {
	spec := testSpec(t, "unsupported-writable")
	spec.WritePolicy = WriteBack
	spec.LayoutPolicy = LayoutFlatV1
	spec.ConflictPolicy = ConflictPreserveLocal
	store, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	adapter := &fakeAdapter{}
	manager := newTestManager(t, store, adapter, &fakeViewFilesystem{}, testMountView(t, spec))
	status, err := manager.Mount(t.Context(), spec)
	if !errors.Is(err, ErrWritePolicyUnavailable) || !status.Desired || status.Active || adapter.mountCalls != 0 {
		t.Fatalf("unsupported write-back status=%#v err=%v calls=%d", status, err, adapter.mountCalls)
	}
	if record, err := store.Get(spec.ID); err != nil || !record.Desired {
		t.Fatalf("unsupported write-back was not retained for retry: %#v err=%v", record, err)
	}
}

func TestManagerClosesWritableBindingOnMountFailureAndSessionExit(t *testing.T) {
	spec := testSpec(t, "writable-lifetime")
	spec.WritePolicy = WriteBack
	spec.LayoutPolicy = LayoutHybridV1
	spec.ConflictPolicy = ConflictPreserveLocal
	view := testMountView(t, spec)

	failedStore, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	failedFilesystem := &fakeWritableViewFilesystem{fakeViewFilesystem: &fakeViewFilesystem{}}
	failedAdapter := &fakeAdapter{mountErr: errors.New("platform failed")}
	failedManager := newTestManager(t, failedStore, failedAdapter, failedFilesystem, view)
	if _, err := failedManager.Mount(t.Context(), spec); err == nil {
		t.Fatal("platform mount failure was accepted")
	}
	failedFilesystem.mu.Lock()
	failedCloseCalls := failedFilesystem.binding.closeCalls
	failedFilesystem.mu.Unlock()
	if failedCloseCalls != 1 {
		t.Fatalf("failed mount binding Close calls=%d, want 1", failedCloseCalls)
	}

	exitedStore, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	exitedFilesystem := &fakeWritableViewFilesystem{fakeViewFilesystem: &fakeViewFilesystem{}}
	exitedAdapter := &fakeAdapter{}
	exitedManager := newTestManager(t, exitedStore, exitedAdapter, exitedFilesystem, view)
	if _, err := exitedManager.Mount(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	exitedAdapter.lastSession.done <- errors.New("driver disconnected")
	close(exitedAdapter.lastSession.done)
	deadline := time.Now().Add(time.Second)
	for {
		exitedFilesystem.mu.Lock()
		closeCalls := exitedFilesystem.binding.closeCalls
		exitedFilesystem.mu.Unlock()
		statuses, statusErr := exitedManager.List()
		if statusErr != nil {
			t.Fatal(statusErr)
		}
		if closeCalls == 1 && len(statuses) == 1 && !statuses[0].Active && statuses[0].LastError != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("unexpected exit closeCalls=%d statuses=%#v", closeCalls, statuses)
		}
		time.Sleep(time.Millisecond)
	}

	retryStore, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	retryFilesystem := &fakeWritableViewFilesystem{fakeViewFilesystem: &fakeViewFilesystem{}}
	retryAdapter := &fakeAdapter{}
	retryManager := newTestManager(t, retryStore, retryAdapter, retryFilesystem, view)
	if _, err := retryManager.Mount(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	retryAdapter.lastSession.unmountErr = errors.New("ambiguous detach")
	if err := retryManager.Unmount(t.Context(), spec.ID); err == nil {
		t.Fatal("ambiguous unmount was accepted")
	}
	retryFilesystem.mu.Lock()
	closeCalls := retryFilesystem.binding.closeCalls
	retryFilesystem.mu.Unlock()
	if closeCalls != 0 {
		t.Fatalf("binding closed while platform mount may remain active: calls=%d", closeCalls)
	}
	retryAdapter.lastSession.unmountErr = nil
	if err := retryManager.Unmount(t.Context(), spec.ID); err != nil {
		t.Fatal(err)
	}
	retryFilesystem.mu.Lock()
	closeCalls = retryFilesystem.binding.closeCalls
	retryFilesystem.mu.Unlock()
	if closeCalls != 1 {
		t.Fatalf("binding Close calls after confirmed retry=%d, want 1", closeCalls)
	}
}

func TestManagerRecoversPendingUnmountTombstone(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mounts.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec(t, "docs")
	view := testMountView(t, spec)
	adapter := &fakeAdapter{}
	manager := newTestManager(t, store, adapter, &fakeViewFilesystem{}, view)
	if _, err := manager.Mount(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	injected := errors.New("unmount interrupted")
	adapter.lastSession.unmountErr = injected
	if err := manager.Unmount(t.Context(), spec.ID); !errors.Is(err, injected) {
		t.Fatalf("Unmount error = %v", err)
	}
	if record, err := store.Get(spec.ID); err != nil || record.Desired {
		t.Fatalf("pending unmount tombstone = %#v, %v", record, err)
	}
	if status, err := manager.Mount(t.Context(), spec); !errors.Is(err, ErrPendingUnmount) || status.Active {
		t.Fatalf("Mount over pending unmount = %#v, %v", status, err)
	}
	if err := manager.Shutdown(t.Context()); !errors.Is(err, injected) {
		t.Fatalf("Shutdown after ambiguous unmount error = %v, want %v", err, injected)
	}
	if _, err := manager.List(); err != nil {
		t.Fatalf("failed Shutdown must remain retryable: %v", err)
	}
	adapter.lastSession.unmountErr = nil
	if err := manager.Shutdown(t.Context()); err != nil {
		t.Fatalf("retry Shutdown: %v", err)
	}

	reopened, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	recoveryAdapter := &fakeAdapter{}
	restarted := newTestManager(t, reopened, recoveryAdapter, &fakeViewFilesystem{}, view)
	if err := restarted.Restore(t.Context()); err != nil {
		t.Fatal(err)
	}
	if recoveryAdapter.recoverCalls != 1 {
		t.Fatalf("RecoverUnmount calls = %d, want 1", recoveryAdapter.recoverCalls)
	}
	if records, err := reopened.List(); err != nil || len(records) != 0 {
		t.Fatalf("recovered records = %#v, %v", records, err)
	}
}

func TestManagerRejectsMismatchedLocalViewAndTracksUnexpectedExit(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec(t, "docs")
	view := testMountView(t, spec)
	wrong := view
	wrong.DatasetID = "other"
	adapter := &fakeAdapter{}
	manager := newTestManager(t, store, adapter, &fakeViewFilesystem{}, wrong)
	if status, err := manager.Mount(t.Context(), spec); err == nil || status.Active || adapter.mountCalls != 0 {
		t.Fatalf("mismatched view Mount = %#v, calls=%d, err=%v", status, adapter.mountCalls, err)
	}
	if err := manager.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}

	manager = newTestManager(t, store, adapter, &fakeViewFilesystem{}, view)
	if _, err := manager.Mount(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	adapter.lastSession.done <- errors.New("driver disconnected")
	close(adapter.lastSession.done)
	deadline := time.Now().Add(time.Second)
	for {
		statuses, err := manager.List()
		if err != nil {
			t.Fatal(err)
		}
		if len(statuses) == 1 && !statuses[0].Active && statuses[0].Desired && statuses[0].LastError != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("unexpected exit status = %#v", statuses)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestConcurrentIdempotentMountCreatesOnePlatformSession(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec(t, "docs")
	view := testMountView(t, spec)
	adapter := &fakeAdapter{}
	manager := newTestManager(t, store, adapter, &fakeViewFilesystem{}, view)
	var wait sync.WaitGroup
	errorsSeen := make(chan error, 16)
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			status, err := manager.Mount(t.Context(), spec)
			if err != nil || !status.Active {
				errorsSeen <- errors.Join(err, errors.New("mount was not active"))
			}
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		t.Fatal(err)
	}
	adapter.mu.Lock()
	calls := adapter.mountCalls
	adapter.mu.Unlock()
	if calls != 1 {
		t.Fatalf("platform Mount calls = %d, want 1", calls)
	}
	if err := manager.Unmount(t.Context(), spec.ID); err != nil {
		t.Fatal(err)
	}
}

func TestManagerDoesNotHoldStateLockAcrossPlatformCallbacks(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec(t, "docs")
	view := testMountView(t, spec)
	adapter := &callbackAdapter{}
	manager := newTestManager(t, store, adapter, &fakeViewFilesystem{}, view)
	adapter.onMount = func() error {
		_, err := manager.List()
		return err
	}
	adapter.onUnmount = func() error {
		_, err := manager.List()
		return err
	}

	if _, err := manager.Mount(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	if err := manager.Unmount(t.Context(), spec.ID); err != nil {
		t.Fatal(err)
	}
}

func TestManagerLeaseExcludesSecondDaemonAndReleasesOnShutdown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mounts.json")
	firstStore, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec(t, "docs")
	view := testMountView(t, spec)
	first := newTestManager(t, firstStore, &fakeAdapter{}, &fakeViewFilesystem{}, view)
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path + ".manager.lock")
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("manager lease mode = %o, want owner-only", info.Mode().Perm())
		}
	}

	secondStore, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	secondStore.leaseTimeout = 30 * time.Millisecond
	if _, err := NewManager(Options{
		Store: secondStore, Adapter: &fakeAdapter{}, Filesystem: &fakeViewFilesystem{},
		Selector: ViewSelectorFunc(func(context.Context, Spec) (filesystemservice.View, error) { return view, nil }),
	}); err == nil || !strings.Contains(err.Error(), "manager lease") {
		t.Fatalf("second manager lease error = %v", err)
	}

	if err := first.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	second := newTestManager(t, secondStore, &fakeAdapter{}, &fakeViewFilesystem{}, view)
	if err := second.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownWaitForOperationIsContextCancelable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mounts.json")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec(t, "docs")
	adapter := &blockingMountAdapter{entered: make(chan struct{}), release: make(chan struct{})}
	manager := newTestManager(t, store, adapter, &fakeViewFilesystem{}, testMountView(t, spec))
	operationCtx, cancelOperation := context.WithCancel(t.Context())
	defer cancelOperation()
	mountDone := make(chan error, 1)
	go func() {
		_, err := manager.Mount(operationCtx, spec)
		mountDone <- err
	}()
	select {
	case <-adapter.entered:
	case <-time.After(time.Second):
		t.Fatal("blocking mount operation did not start")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancelShutdown()
	started := time.Now()
	if err := manager.Shutdown(shutdownCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown error=%v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("context-canceled Shutdown blocked for %s", elapsed)
	}

	cancelOperation()
	if err := <-mountDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("Mount error=%v, want canceled", err)
	}
	if err := manager.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	secondStore, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	second := newTestManager(t, secondStore, &fakeAdapter{}, &fakeViewFilesystem{}, testMountView(t, spec))
	if err := second.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestSuccessfulShutdownDoesNotReportExpectedSessionExit(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec(t, "docs")
	view := testMountView(t, spec)
	adapter := &fakeAdapter{}
	manager := newTestManager(t, store, adapter, &fakeViewFilesystem{}, view)
	if _, err := manager.Mount(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	adapter.lastSession.afterUnmount = func() {
		deadline := time.Now().Add(time.Second)
		for {
			manager.mu.Lock()
			_, active := manager.live[spec.ID]
			manager.mu.Unlock()
			if !active {
				return
			}
			if time.Now().After(deadline) {
				t.Error("session monitor did not observe shutdown")
				return
			}
			time.Sleep(time.Millisecond)
		}
	}
	if err := manager.Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	lastError := manager.errors[spec.ID]
	manager.mu.Unlock()
	if lastError != "" {
		t.Fatalf("successful shutdown retained LastError %q", lastError)
	}
	if _, err := manager.List(); !errors.Is(err, ErrManagerClosed) {
		t.Fatalf("List after Shutdown error = %v", err)
	}
}

func TestManagerShutdownClosesFilesystemResourcesAndRetriesFailure(t *testing.T) {
	transient := errors.New("filesystem close unavailable")
	filesystem := &fakeViewFilesystem{closeErrors: []error{transient}}
	store, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	manager := newTestManager(t, store, &fakeAdapter{}, filesystem, filesystemservice.View{})
	if err := manager.Shutdown(t.Context()); !errors.Is(err, transient) {
		t.Fatalf("first Shutdown error = %v, want close failure", err)
	}
	if err := manager.Shutdown(t.Context()); err != nil {
		t.Fatalf("retry Shutdown: %v", err)
	}
	filesystem.mu.Lock()
	closeCalls := filesystem.closeCalls
	filesystem.mu.Unlock()
	if closeCalls != 2 {
		t.Fatalf("filesystem Close calls = %d, want 2", closeCalls)
	}
}

func TestManagerReleasesViewLeaseOnUnmountAndFailedMount(t *testing.T) {
	t.Run("unmount", func(t *testing.T) {
		store, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
		if err != nil {
			t.Fatal(err)
		}
		spec := testSpec(t, "leased-unmount")
		filesystem := &fakeLeasedViewFilesystem{fakeViewFilesystem: &fakeViewFilesystem{}}
		manager := newTestManager(t, store, &fakeAdapter{}, filesystem, testMountView(t, spec))
		if _, err := manager.Mount(t.Context(), spec); err != nil {
			t.Fatal(err)
		}
		if err := manager.Unmount(t.Context(), spec.ID); err != nil {
			t.Fatal(err)
		}
		if acquired, released := filesystem.leaseCalls(); acquired != 1 || released != 1 {
			t.Fatalf("View lease calls after unmount = acquire:%d release:%d", acquired, released)
		}
	})

	t.Run("failed mount", func(t *testing.T) {
		store, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
		if err != nil {
			t.Fatal(err)
		}
		spec := testSpec(t, "leased-failure")
		filesystem := &fakeLeasedViewFilesystem{fakeViewFilesystem: &fakeViewFilesystem{}}
		manager := newTestManager(t, store, &fakeAdapter{mountErr: errors.New("platform failure")}, filesystem, testMountView(t, spec))
		if _, err := manager.Mount(t.Context(), spec); err == nil {
			t.Fatal("failed platform mount succeeded")
		}
		if acquired, released := filesystem.leaseCalls(); acquired != 1 || released != 1 {
			t.Fatalf("View lease calls after failed mount = acquire:%d release:%d", acquired, released)
		}
	})
}

func TestManagerRetainsViewLeaseUntilReleaseRetrySucceeds(t *testing.T) {
	transient := errors.New("release View resources")
	store, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json"))
	if err != nil {
		t.Fatal(err)
	}
	spec := testSpec(t, "leased-release-retry")
	filesystem := &fakeLeasedViewFilesystem{
		fakeViewFilesystem: &fakeViewFilesystem{}, releaseErrors: []error{transient},
	}
	manager := newTestManager(t, store, &fakeAdapter{}, filesystem, testMountView(t, spec))
	if _, err := manager.Mount(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
	if err := manager.Unmount(t.Context(), spec.ID); !errors.Is(err, transient) {
		t.Fatalf("first Unmount error = %v, want View release failure", err)
	}
	manager.mu.Lock()
	live, retained := manager.live[spec.ID]
	manager.mu.Unlock()
	if !retained || !live.cleanupOnly || live.needsUnmount {
		t.Fatalf("failed View release was not retained as cleanup-only: %#v", live)
	}
	if err := manager.Unmount(t.Context(), spec.ID); err != nil {
		t.Fatalf("retry Unmount: %v", err)
	}
	if acquired, released := filesystem.leaseCalls(); acquired != 1 || released != 2 {
		t.Fatalf("View lease retry calls = acquire:%d release:%d", acquired, released)
	}
}

func newTestManager(t *testing.T, store *Store, adapter Adapter, filesystem ViewFilesystem, view filesystemservice.View) *Manager {
	t.Helper()
	manager, err := NewManager(Options{
		Store: store, Adapter: adapter, Filesystem: filesystem,
		Selector: ViewSelectorFunc(func(context.Context, Spec) (filesystemservice.View, error) { return view, nil }),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	return manager
}

func testMountView(t *testing.T, spec Spec) filesystemservice.View {
	t.Helper()
	return filesystemservice.View{
		DatasetID: spec.DatasetID, Branch: spec.Branch, Root: testMountCID(t, []byte("accepted root")),
		Revision: 7, EncryptionEpoch: spec.EncryptionEpoch,
	}
}

func testMountCID(t *testing.T, body []byte) cid.Cid {
	t.Helper()
	value, err := (cid.Prefix{Version: 1, Codec: cid.Raw, MhType: mh.SHA2_256, MhLength: -1}).Sum(body)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

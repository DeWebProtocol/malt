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
	mu       sync.Mutex
	lastView filesystemservice.View
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
	mu             sync.Mutex
	mountErr       error
	recoverErr     error
	mountCalls     int
	recoverCalls   int
	lastFilesystem ReadOnlyFilesystem
	lastSession    *fakeSession
}

func (*fakeAdapter) Name() string { return "fake-platform" }

func (a *fakeAdapter) Mount(_ context.Context, _ Spec, filesystem ReadOnlyFilesystem) (Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.mountCalls++
	a.lastFilesystem = filesystem
	if a.mountErr != nil {
		return nil, a.mountErr
	}
	session := &fakeSession{done: make(chan error, 1)}
	a.lastSession = session
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
	unmountErr   error
	unmounted    bool
	afterUnmount func()
}

func (s *fakeSession) Unmount(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unmountErr != nil {
		return s.unmountErr
	}
	if !s.unmounted {
		s.unmounted = true
		s.done <- nil
		close(s.done)
		if s.afterUnmount != nil {
			s.afterUnmount()
		}
	}
	return nil
}

func (s *fakeSession) Done() <-chan error { return s.done }

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

//go:build linux

package fusefs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	filesystemmount "github.com/dewebprotocol/malt-client/filesystem/mount"
	filesystemservice "github.com/dewebprotocol/malt-client/filesystem/service"
	"github.com/dewebprotocol/malt-client/unixfs"
	"github.com/hanwen/go-fuse/v2/fs"
)

type fakeServer struct {
	once       sync.Once
	wait       chan struct{}
	unmountErr error
}

func newFakeServer() *fakeServer { return &fakeServer{wait: make(chan struct{})} }

func (s *fakeServer) Unmount() error {
	if s.unmountErr != nil {
		return s.unmountErr
	}
	s.once.Do(func() { close(s.wait) })
	return nil
}

func (s *fakeServer) Wait() { <-s.wait }

func TestAdapterMountsOwnedReadOnlyFilesystemAndSessionIsIdempotent(t *testing.T) {
	mountpoint := t.TempDir()
	spec := fuseTestSpec(mountpoint)
	var current *mountIdentity
	fuseServer := newFakeServer()
	adapter := New()
	adapter.inspect = func(string) (*mountIdentity, error) { return current, nil }
	adapter.mount = func(got string, _ fs.InodeEmbedder, options *fs.Options) (server, error) {
		if got != mountpoint || options.MountOptions.FsName != expectedSource(spec) || options.MountOptions.Name != "malt" ||
			len(options.MountOptions.Options) != 1 || options.MountOptions.Options[0] != "ro" ||
			options.MountOptions.DisableXAttrs || !options.NullPermissions {
			t.Fatalf("mount options = %#v", options)
		}
		current = &mountIdentity{ID: 42, Mountpoint: mountpoint, Filesystem: "fuse.malt", Source: expectedSource(spec)}
		return fuseServer, nil
	}
	adapter.recover = func(context.Context, string, mountIdentity) error { return errors.New("unexpected recovery") }
	session, err := adapter.Mount(t.Context(), spec, &fakeFilesystem{
		infos: map[string]filesystemservice.Info{"": {Kind: unixfs.StagedKindDirectory}},
		dirs:  map[string][]filesystemservice.DirEntry{}, bodies: map[string][]byte{}, statErr: map[string]error{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Unmount(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := session.Unmount(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-session.Done():
	case <-time.After(time.Second):
		t.Fatal("session Done did not close after unmount")
	}
}

func TestAdapterRequiresExplicitMatchingWritablePolicy(t *testing.T) {
	for _, test := range []struct {
		name        string
		writePolicy filesystemmount.WritePolicy
		filesystem  filesystemmount.ReadOnlyFilesystem
		wantRO      bool
		wantError   error
	}{
		{name: "read-only hides wider capability", writePolicy: filesystemmount.WriteReadOnly, filesystem: newFakeWritableFilesystem(), wantRO: true},
		{name: "write-back rejects narrow capability", writePolicy: filesystemmount.WriteBack, filesystem: minimalFilesystem(), wantError: filesystemmount.ErrWritePolicyUnavailable},
		{name: "write-back exposes writable capability", writePolicy: filesystemmount.WriteBack, filesystem: newFakeWritableFilesystem()},
	} {
		t.Run(test.name, func(t *testing.T) {
			mountpoint := t.TempDir()
			spec := fuseTestSpec(mountpoint)
			spec.WritePolicy = test.writePolicy
			if test.writePolicy == filesystemmount.WriteBack {
				spec.LayoutPolicy = filesystemmount.LayoutFlatV1
				spec.ConflictPolicy = filesystemmount.ConflictPreserveLocal
			}
			var current *mountIdentity
			mountCalls := 0
			fuseServer := newFakeServer()
			adapter := New()
			adapter.inspect = func(string) (*mountIdentity, error) { return current, nil }
			adapter.mount = func(_ string, root fs.InodeEmbedder, options *fs.Options) (server, error) {
				mountCalls++
				operations := root.(*node)
				hasWritable := operations.mount.writable != nil
				if hasWritable != (test.writePolicy == filesystemmount.WriteBack) {
					t.Fatalf("root writable=%v policy=%s", hasWritable, test.writePolicy)
				}
				hasRO := len(options.MountOptions.Options) == 1 && options.MountOptions.Options[0] == "ro"
				if hasRO != test.wantRO {
					t.Fatalf("mount options=%#v wantRO=%v", options.MountOptions.Options, test.wantRO)
				}
				current = &mountIdentity{ID: 51, Mountpoint: mountpoint, Filesystem: "fuse.malt", Source: expectedSource(spec)}
				return fuseServer, nil
			}
			session, err := adapter.Mount(t.Context(), spec, test.filesystem)
			if !errors.Is(err, test.wantError) {
				t.Fatalf("Mount error=%v, want %v", err, test.wantError)
			}
			if test.wantError != nil {
				if mountCalls != 0 || session != nil {
					t.Fatalf("failed capability mount calls=%d session=%T", mountCalls, session)
				}
				return
			}
			if mountCalls != 1 || session == nil {
				t.Fatalf("mount calls=%d session=%T", mountCalls, session)
			}
			if err := session.Unmount(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRecoverUnmountDoesNotInspectDisconnectedFinalPath(t *testing.T) {
	mountpoint := filepath.Join(t.TempDir(), "disconnected")
	spec := fuseTestSpec(mountpoint)
	current := &mountIdentity{ID: 7, Mountpoint: mountpoint, Filesystem: "fuse.malt", Source: expectedSource(spec)}
	adapter := New()
	adapter.ready = func(string) (string, error) {
		t.Fatal("recovery inspected the disconnected final mountpoint")
		return "", syscall.ENOTCONN
	}
	adapter.inspect = func(string) (*mountIdentity, error) { return current, nil }
	adapter.recover = func(context.Context, string, mountIdentity) error {
		current = nil
		return nil
	}
	if err := adapter.RecoverUnmount(t.Context(), spec); err != nil {
		t.Fatal(err)
	}
}

func TestAdapterRecoversOnlyExactOwnedMount(t *testing.T) {
	mountpoint := t.TempDir()
	spec := fuseTestSpec(mountpoint)
	for _, test := range []struct {
		name      string
		identity  *mountIdentity
		wantError error
		wantCalls int
	}{
		{name: "absent"},
		{name: "owned", identity: &mountIdentity{ID: 2, Mountpoint: mountpoint, Filesystem: "fuse.malt", Source: expectedSource(spec)}, wantCalls: 1},
		{name: "foreign source", identity: &mountIdentity{ID: 2, Mountpoint: mountpoint, Filesystem: "fuse.malt", Source: "other"}, wantError: ErrForeignMount},
		{name: "generic fuse type", identity: &mountIdentity{ID: 2, Mountpoint: mountpoint, Filesystem: "fuse", Source: expectedSource(spec)}, wantError: ErrForeignMount},
		{name: "foreign type", identity: &mountIdentity{ID: 2, Mountpoint: mountpoint, Filesystem: "ext4", Source: expectedSource(spec)}, wantError: ErrForeignMount},
	} {
		t.Run(test.name, func(t *testing.T) {
			current := test.identity
			calls := 0
			adapter := New()
			adapter.inspect = func(string) (*mountIdentity, error) { return current, nil }
			adapter.recover = func(context.Context, string, mountIdentity) error {
				calls++
				current = nil
				return nil
			}
			err := adapter.RecoverUnmount(t.Context(), spec)
			if !errors.Is(err, test.wantError) || calls != test.wantCalls {
				t.Fatalf("RecoverUnmount error=%v calls=%d", err, calls)
			}
		})
	}
}

func TestAdapterReconcilesOwnedStaleMountBeforeCreatingSession(t *testing.T) {
	mountpoint := t.TempDir()
	spec := fuseTestSpec(mountpoint)
	current := &mountIdentity{ID: 3, Mountpoint: mountpoint, Filesystem: "fuse.malt", Source: expectedSource(spec)}
	recoverCalls := 0
	mountCalls := 0
	adapter := New()
	adapter.inspect = func(string) (*mountIdentity, error) { return current, nil }
	adapter.recover = func(context.Context, string, mountIdentity) error {
		recoverCalls++
		current = nil
		return nil
	}
	fuseServer := newFakeServer()
	adapter.mount = func(string, fs.InodeEmbedder, *fs.Options) (server, error) {
		mountCalls++
		current = &mountIdentity{ID: 4, Mountpoint: mountpoint, Filesystem: "fuse.malt", Source: expectedSource(spec)}
		return fuseServer, nil
	}
	session, err := adapter.Mount(t.Context(), spec, minimalFilesystem())
	if err != nil {
		t.Fatal(err)
	}
	if recoverCalls != 1 || mountCalls != 1 {
		t.Fatalf("recover calls=%d mount calls=%d", recoverCalls, mountCalls)
	}
	if err := session.Unmount(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestAdapterCleansServerWhenMountedIdentityCannotBeVerified(t *testing.T) {
	mountpoint := t.TempDir()
	adapter := New()
	adapter.inspect = func(string) (*mountIdentity, error) { return nil, nil }
	fuseServer := newFakeServer()
	adapter.mount = func(string, fs.InodeEmbedder, *fs.Options) (server, error) { return fuseServer, nil }
	if _, err := adapter.Mount(t.Context(), fuseTestSpec(mountpoint), minimalFilesystem()); !errors.Is(err, ErrMountIdentity) {
		t.Fatalf("Mount error=%v, want ErrMountIdentity", err)
	}
	select {
	case <-fuseServer.wait:
	default:
		t.Fatal("unverified mount server was not cleaned up")
	}
}

func TestSessionUnmountContinuesAfterCallerCancellationAndCanBeRetried(t *testing.T) {
	wait := make(chan struct{})
	unblock := make(chan struct{})
	server := &blockingServer{wait: wait, unblock: unblock}
	session := newSession(server)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err := session.Unmount(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Unmount error=%v", err)
	}
	close(unblock)
	if err := session.Unmount(t.Context()); err != nil {
		t.Fatal(err)
	}
	if server.calls != 1 {
		t.Fatalf("server Unmount calls=%d, want 1", server.calls)
	}
}

func TestAdapterRejectsUnsafeOrForeignMountpointBeforeMount(t *testing.T) {
	for _, test := range []struct {
		name     string
		prepare  func(*testing.T) string
		identity *mountIdentity
		want     error
	}{
		{name: "nonempty", prepare: func(t *testing.T) string {
			path := t.TempDir()
			if err := os.WriteFile(filepath.Join(path, "keep"), []byte("data"), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}, want: ErrUnsafeMountpoint},
		{name: "foreign", prepare: func(t *testing.T) string { return t.TempDir() }, identity: &mountIdentity{ID: 9, Filesystem: "ext4", Source: "/dev/sda"}, want: ErrForeignMount},
	} {
		t.Run(test.name, func(t *testing.T) {
			mountpoint := test.prepare(t)
			identity := test.identity
			if identity != nil {
				identity.Mountpoint = mountpoint
			}
			adapter := New()
			adapter.inspect = func(string) (*mountIdentity, error) { return identity, nil }
			adapter.mount = func(string, fs.InodeEmbedder, *fs.Options) (server, error) {
				t.Fatal("unsafe mountpoint reached mount operation")
				return nil, nil
			}
			_, err := adapter.Mount(t.Context(), fuseTestSpec(mountpoint), &fakeFilesystem{})
			if !errors.Is(err, test.want) {
				t.Fatalf("Mount error=%v, want %v", err, test.want)
			}
		})
	}
}

func TestParseMountIdentitiesDecodesStackedEntries(t *testing.T) {
	table := strings.Join([]string{
		"20 1 0:1 / /tmp/other rw - ext4 /dev/root rw",
		"31 20 0:42 / /tmp/malt\\040mount rw,nosuid,nodev - fuse.malt malt:docs rw,user_id=1000",
		"35 31 0:43 / /tmp/malt\\040mount rw,nosuid,nodev - fuse.malt malt:newer rw,user_id=1000",
	}, "\n")
	identities, err := parseMountIdentities(strings.NewReader(table), "/tmp/malt mount")
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 2 || identities[0].ID != 31 || identities[0].Mountpoint != "/tmp/malt mount" || identities[0].Source != "malt:docs" || identities[1].ID != 35 {
		t.Fatalf("identities = %#v", identities)
	}
	if _, err := parseMountIdentities(strings.NewReader("broken"), "/tmp/malt"); err == nil {
		t.Fatal("malformed mount table was accepted")
	}
	if identities, err := parseMountIdentities(strings.NewReader(table), "/tmp/missing"); err != nil || len(identities) != 0 {
		t.Fatalf("missing mount identities=%#v err=%v", identities, err)
	}
}

func TestParseVisibleMountIDFailsClosed(t *testing.T) {
	id, err := parseVisibleMountID(strings.NewReader("pos:\t0\nflags:\t010000000\nmnt_id:\t35\n"))
	if err != nil || id != 35 {
		t.Fatalf("mount ID=%d err=%v", id, err)
	}
	for _, input := range []string{"pos:\t0\n", "mnt_id:\t0\n", "mnt_id:\tnot-a-number\n"} {
		if _, err := parseVisibleMountID(strings.NewReader(input)); err == nil {
			t.Fatalf("invalid fdinfo %q was accepted", input)
		}
	}
}

func TestSingleMountIdentityDoesNotTouchDisconnectedMountpoint(t *testing.T) {
	want := mountIdentity{ID: 35, Mountpoint: "/tmp/malt", Filesystem: "fuse.malt", Source: "malt:docs"}
	identity, err := selectVisibleMountIdentity([]mountIdentity{want}, func() (uint64, error) {
		t.Fatal("single mount identity consulted disconnected mountpoint")
		return 0, syscall.ENOTCONN
	})
	if err != nil || identity == nil || *identity != want {
		t.Fatalf("identity=%#v err=%v", identity, err)
	}
	stacked := []mountIdentity{{ID: 31}, {ID: 35}}
	identity, err = selectVisibleMountIdentity(stacked, func() (uint64, error) { return 35, nil })
	if err != nil || identity == nil || identity.ID != 35 {
		t.Fatalf("stacked identity=%#v err=%v", identity, err)
	}
	if _, err := selectVisibleMountIdentity(stacked, func() (uint64, error) { return 0, syscall.ENOTCONN }); err == nil {
		t.Fatal("ambiguous disconnected stack was accepted")
	}
}

func TestLinuxFUSESmoke(t *testing.T) {
	if os.Getenv("MALT_FUSE_SMOKE") != "1" {
		t.Skip("set MALT_FUSE_SMOKE=1 on a Linux host with FUSE to run the mount smoke test")
	}
	mountpoint := t.TempDir()
	filesystem := &fakeFilesystem{
		infos: map[string]filesystemservice.Info{
			"":          {Kind: unixfs.StagedKindDirectory},
			"hello.txt": {Path: "hello.txt", Name: "hello.txt", Kind: unixfs.StagedKindFile, Size: 11},
		},
		dirs: map[string][]filesystemservice.DirEntry{
			"": {{Name: "hello.txt", Kind: unixfs.StagedKindFile}},
		},
		bodies: map[string][]byte{"hello.txt": []byte("hello world")}, statErr: map[string]error{},
	}
	session, err := New().Mount(t.Context(), fuseTestSpec(mountpoint), filesystem)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Unmount(context.Background()) })
	body, err := os.ReadFile(filepath.Join(mountpoint, "hello.txt"))
	if err != nil || string(body) != "hello world" {
		t.Fatalf("mounted read body=%q err=%v", body, err)
	}
	if err := os.WriteFile(filepath.Join(mountpoint, "new.txt"), []byte("blocked"), 0o600); err == nil {
		t.Fatal("read-only FUSE mount accepted a write")
	}
	if err := session.Unmount(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestLinuxFUSEWritableSmoke(t *testing.T) {
	if os.Getenv("MALT_FUSE_WRITABLE_SMOKE") != "1" {
		t.Skip("set MALT_FUSE_WRITABLE_SMOKE=1 on a Linux host with FUSE to run the writable mount smoke test")
	}
	mountpoint := t.TempDir()
	filesystem := newFakeWritableFilesystem()
	spec := fuseTestSpec(mountpoint)
	spec.WritePolicy = filesystemmount.WriteBack
	spec.LayoutPolicy = filesystemmount.LayoutFlatV1
	spec.ConflictPolicy = filesystemmount.ConflictPreserveLocal
	session, err := New().Mount(t.Context(), spec, filesystem)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Unmount(context.Background()) })
	created := filepath.Join(mountpoint, "created.txt")
	if err := os.WriteFile(created, []byte("created body"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(created, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("MALT"), 0); err != nil {
		t.Fatal(err)
	}
	if err := file.Sync(); err != nil {
		t.Fatal(err)
	}
	busySource := filepath.Join(mountpoint, "busy-source.txt")
	if err := os.WriteFile(busySource, []byte("replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(busySource, created); !errors.Is(err, syscall.EBUSY) {
		t.Fatalf("overwrite open destination error=%v, want EBUSY", err)
	}
	if err := os.Remove(created); !errors.Is(err, syscall.EBUSY) {
		t.Fatalf("unlink open file error=%v, want EBUSY", err)
	}
	if err := file.Truncate(4); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(created)
	if err != nil || string(body) != "MALT" {
		t.Fatalf("writable body=%q err=%v", body, err)
	}
	if err := os.Remove(busySource); err != nil {
		t.Fatal(err)
	}
	renamed := filepath.Join(mountpoint, "renamed.txt")
	if err := os.Rename(created, renamed); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(renamed); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(mountpoint, "empty")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(directory); err != nil {
		t.Fatal(err)
	}
	if err := session.Unmount(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type blockingServer struct {
	wait    chan struct{}
	unblock chan struct{}
	calls   int
	once    sync.Once
}

func (s *blockingServer) Unmount() error {
	s.calls++
	<-s.unblock
	s.once.Do(func() { close(s.wait) })
	return nil
}

func (s *blockingServer) Wait() { <-s.wait }

func minimalFilesystem() *fakeFilesystem {
	return &fakeFilesystem{
		infos: map[string]filesystemservice.Info{"": {Kind: unixfs.StagedKindDirectory}},
		dirs:  map[string][]filesystemservice.DirEntry{}, bodies: map[string][]byte{}, statErr: map[string]error{},
	}
}

func fuseTestSpec(mountpoint string) filesystemmount.Spec {
	return filesystemmount.Spec{
		ID: "docs", DatasetID: "bucket-one", Branch: "main", Mountpoint: mountpoint,
		TrustAlias: "docs", CachePolicy: filesystemmount.CacheVerified,
		WritePolicy: filesystemmount.WriteReadOnly, ConflictPolicy: filesystemmount.ConflictFailReadOnly,
	}
}

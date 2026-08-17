//go:build linux

package fusefs

import (
	"context"
	"errors"
	"path"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"

	filesystemmount "github.com/dewebprotocol/malt-client/filesystem/mount"
	filesystemservice "github.com/dewebprotocol/malt-client/filesystem/service"
	"github.com/dewebprotocol/malt-client/unixfs"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

type fakeFilesystem struct {
	infos   map[string]filesystemservice.Info
	dirs    map[string][]filesystemservice.DirEntry
	bodies  map[string][]byte
	statErr map[string]error
}

func (f *fakeFilesystem) Stat(_ context.Context, path string) (filesystemservice.Info, error) {
	if err := f.statErr[path]; err != nil {
		return filesystemservice.Info{}, err
	}
	info, ok := f.infos[path]
	if !ok {
		return filesystemservice.Info{}, unixfs.ErrNotFound
	}
	return info, nil
}

func (f *fakeFilesystem) ReadDir(_ context.Context, path string) ([]filesystemservice.DirEntry, error) {
	info, err := f.Stat(context.Background(), path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, unixfs.ErrNotDirectory
	}
	return append([]filesystemservice.DirEntry(nil), f.dirs[path]...), nil
}

func (f *fakeFilesystem) Open(_ context.Context, path string) (filesystemmount.ReadHandle, error) {
	info, err := f.Stat(context.Background(), path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return nil, unixfs.ErrNotFile
	}
	return &fakeReadHandle{info: info, body: append([]byte(nil), f.bodies[path]...)}, nil
}

type fakeReadHandle struct {
	mu     sync.Mutex
	info   filesystemservice.Info
	body   []byte
	closed bool
}

type fakeWritableFilesystem struct {
	mu         sync.Mutex
	infos      map[string]filesystemservice.Info
	bodies     map[string][]byte
	syncResult filesystemmount.SyncResult
	syncErr    error
	calls      []string
}

type adversarialOpenFilesystem struct {
	*fakeWritableFilesystem
	handle filesystemmount.ReadHandle
	err    error
}

func (f *adversarialOpenFilesystem) Open(context.Context, string) (filesystemmount.ReadHandle, error) {
	return f.handle, f.err
}

type shortWriteFilesystem struct{ *fakeWritableFilesystem }

func (f *shortWriteFilesystem) WriteAt(ctx context.Context, name string, offset uint64, data []byte) (filesystemservice.Info, error) {
	info, err := f.fakeWritableFilesystem.WriteAt(ctx, name, offset, data)
	info.Size = 0
	return info, err
}

func newFakeWritableFilesystem() *fakeWritableFilesystem {
	return &fakeWritableFilesystem{
		infos: map[string]filesystemservice.Info{
			"":        {Path: "", Name: "", Kind: unixfs.StagedKindDirectory},
			"old.txt": {Path: "old.txt", Name: "old.txt", Kind: unixfs.StagedKindFile, Size: 6},
		},
		bodies:     map[string][]byte{"old.txt": []byte("before")},
		syncResult: filesystemmount.SyncResult{LocalDurable: true},
	}
}

func (f *fakeWritableFilesystem) Stat(_ context.Context, name string) (filesystemservice.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, ok := f.infos[name]
	if !ok {
		return filesystemservice.Info{}, unixfs.ErrNotFound
	}
	return info, nil
}

func (f *fakeWritableFilesystem) ReadDir(_ context.Context, parent string) ([]filesystemservice.DirEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, ok := f.infos[parent]
	if !ok {
		return nil, unixfs.ErrNotFound
	}
	if !info.IsDir() {
		return nil, unixfs.ErrNotDirectory
	}
	var result []filesystemservice.DirEntry
	for name, child := range f.infos {
		if name == parent || path.Dir(name) != parent && !(parent == "" && !strings.Contains(name, "/")) {
			continue
		}
		result = append(result, filesystemservice.DirEntry{Name: child.Name, Kind: child.Kind})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (f *fakeWritableFilesystem) Open(_ context.Context, name string) (filesystemmount.ReadHandle, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, ok := f.infos[name]
	if !ok {
		return nil, unixfs.ErrNotFound
	}
	if info.IsDir() {
		return nil, unixfs.ErrNotFile
	}
	return &fakeReadHandle{info: info, body: append([]byte(nil), f.bodies[name]...)}, nil
}

func (f *fakeWritableFilesystem) Create(_ context.Context, name string) (filesystemservice.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.infos[name]; ok {
		return filesystemservice.Info{}, filesystemmount.ErrAlreadyExists
	}
	info := filesystemservice.Info{Path: name, Name: path.Base(name), Kind: unixfs.StagedKindFile}
	f.infos[name] = info
	f.bodies[name] = []byte{}
	f.calls = append(f.calls, "create:"+name)
	return info, nil
}

func (f *fakeWritableFilesystem) WriteAt(_ context.Context, name string, offset uint64, data []byte) (filesystemservice.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, ok := f.infos[name]
	if !ok {
		return filesystemservice.Info{}, unixfs.ErrNotFound
	}
	if info.IsDir() {
		return filesystemservice.Info{}, unixfs.ErrNotFile
	}
	end := offset + uint64(len(data))
	if end < offset || end > uint64(^uint(0)>>1) {
		return filesystemservice.Info{}, filesystemmount.ErrFileTooLarge
	}
	body := append([]byte(nil), f.bodies[name]...)
	if end > uint64(len(body)) {
		body = append(body, make([]byte, int(end)-len(body))...)
	}
	copy(body[int(offset):], data)
	f.bodies[name] = body
	info.Size = uint64(len(body))
	f.infos[name] = info
	f.calls = append(f.calls, "write:"+name)
	return info, nil
}

func (f *fakeWritableFilesystem) Truncate(_ context.Context, name string, size uint64) (filesystemservice.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, ok := f.infos[name]
	if !ok {
		return filesystemservice.Info{}, unixfs.ErrNotFound
	}
	if info.IsDir() {
		return filesystemservice.Info{}, unixfs.ErrNotFile
	}
	body := make([]byte, int(size))
	copy(body, f.bodies[name])
	f.bodies[name] = body
	info.Size = size
	f.infos[name] = info
	f.calls = append(f.calls, "truncate:"+name)
	return info, nil
}

func (f *fakeWritableFilesystem) Mkdir(_ context.Context, name string) (filesystemservice.Info, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.infos[name]; ok {
		return filesystemservice.Info{}, filesystemmount.ErrAlreadyExists
	}
	info := filesystemservice.Info{Path: name, Name: path.Base(name), Kind: unixfs.StagedKindDirectory}
	f.infos[name] = info
	f.calls = append(f.calls, "mkdir:"+name)
	return info, nil
}

func (f *fakeWritableFilesystem) Rename(_ context.Context, source, destination string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.infos[source]; !ok {
		return unixfs.ErrNotFound
	}
	for name, info := range f.infos {
		if name != source && !strings.HasPrefix(name, source+"/") {
			continue
		}
		next := destination + strings.TrimPrefix(name, source)
		delete(f.infos, name)
		info.Path = next
		info.Name = path.Base(next)
		f.infos[next] = info
		if body, ok := f.bodies[name]; ok {
			delete(f.bodies, name)
			f.bodies[next] = body
		}
	}
	f.calls = append(f.calls, "rename:"+source+":"+destination)
	return nil
}

func (f *fakeWritableFilesystem) Unlink(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, ok := f.infos[name]
	if !ok {
		return unixfs.ErrNotFound
	}
	if info.IsDir() {
		return unixfs.ErrNotFile
	}
	delete(f.infos, name)
	delete(f.bodies, name)
	f.calls = append(f.calls, "unlink:"+name)
	return nil
}

func (f *fakeWritableFilesystem) RemoveDir(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	info, ok := f.infos[name]
	if !ok {
		return unixfs.ErrNotFound
	}
	if !info.IsDir() {
		return unixfs.ErrNotDirectory
	}
	for candidate := range f.infos {
		if strings.HasPrefix(candidate, name+"/") {
			return filesystemmount.ErrNotEmpty
		}
	}
	delete(f.infos, name)
	f.calls = append(f.calls, "rmdir:"+name)
	return nil
}

func (f *fakeWritableFilesystem) Sync(context.Context) (filesystemmount.SyncResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, "sync")
	return f.syncResult, f.syncErr
}

func (h *fakeReadHandle) Info() filesystemservice.Info { return h.info }

func (h *fakeReadHandle) Read(_ context.Context, offset, length uint64) ([]byte, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return nil, filesystemservice.ErrClosed
	}
	if offset >= uint64(len(h.body)) {
		return []byte{}, nil
	}
	end := offset + length
	if end < offset || end > uint64(len(h.body)) {
		end = uint64(len(h.body))
	}
	return append([]byte(nil), h.body[offset:end]...), nil
}

func (h *fakeReadHandle) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	return nil
}

func (h *fakeReadHandle) isClosed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.closed
}

func TestNodeProjectsReadOnlyVerifiedFilesystem(t *testing.T) {
	filesystem := &fakeFilesystem{
		infos: map[string]filesystemservice.Info{
			"":          {Path: "", Kind: unixfs.StagedKindDirectory},
			"docs":      {Path: "docs", Name: "docs", Kind: unixfs.StagedKindDirectory},
			"hello.txt": {Path: "hello.txt", Name: "hello.txt", Kind: unixfs.StagedKindFile, Size: 11},
		},
		dirs: map[string][]filesystemservice.DirEntry{
			"": {{Name: "docs", Kind: unixfs.StagedKindDirectory}, {Name: "hello.txt", Kind: unixfs.StagedKindFile}},
		},
		bodies:  map[string][]byte{"hello.txt": []byte("hello world")},
		statErr: map[string]error{},
	}
	root := newRoot(filesystem, nil)
	_ = fs.NewNodeFS(root, &fs.Options{RootStableAttr: &fs.StableAttr{Mode: fuse.S_IFDIR}})

	var rootAttr fuse.AttrOut
	if errno := root.Getattr(t.Context(), nil, &rootAttr); errno != 0 || rootAttr.Mode != fuse.S_IFDIR|0o555 {
		t.Fatalf("root Getattr errno=%v attr=%#v", errno, rootAttr.Attr)
	}
	stream, errno := root.Readdir(t.Context())
	if errno != 0 {
		t.Fatal(errno)
	}
	var names []string
	for stream.HasNext() {
		entry, errno := stream.Next()
		if errno != 0 {
			t.Fatal(errno)
		}
		names = append(names, entry.Name)
	}
	stream.Close()
	if len(names) != 2 || names[0] != "docs" || names[1] != "hello.txt" {
		t.Fatalf("Readdir names = %v", names)
	}

	var entry fuse.EntryOut
	child, errno := root.Lookup(t.Context(), "hello.txt", &entry)
	if errno != 0 || child == nil || entry.Mode != fuse.S_IFREG|0o444 || entry.Size != 11 {
		t.Fatalf("Lookup errno=%v child=%v entry=%#v", errno, child, entry.Attr)
	}
	operations := child.Operations().(*node)
	handle, flags, errno := operations.Open(t.Context(), syscall.O_RDONLY)
	if errno != 0 || flags != fuse.FOPEN_KEEP_CACHE {
		t.Fatalf("Open errno=%v flags=%#x", errno, flags)
	}
	result, errno := handle.(*fileHandle).Read(t.Context(), make([]byte, 5), 6)
	if errno != 0 {
		t.Fatal(errno)
	}
	body, status := result.Bytes(nil)
	result.Done()
	if !status.Ok() || string(body) != "world" {
		t.Fatalf("Read status=%v body=%q", status, body)
	}
	if errno := handle.(*fileHandle).Release(t.Context()); errno != 0 {
		t.Fatal(errno)
	}
	if _, errno := handle.(*fileHandle).Read(t.Context(), make([]byte, 1), 0); errno != syscall.EBADF {
		t.Fatalf("Read after Release errno=%v", errno)
	}
}

func TestNodeProjectsWritableOverlayWithFreshHandlesAndLocalFsync(t *testing.T) {
	filesystem := newFakeWritableFilesystem()
	root := newRoot(filesystem, filesystem)
	_ = fs.NewNodeFS(root, &fs.Options{RootStableAttr: &fs.StableAttr{Mode: fuse.S_IFDIR}})

	var rootAttr fuse.AttrOut
	if failure := root.Getattr(t.Context(), nil, &rootAttr); failure != 0 || rootAttr.Mode != fuse.S_IFDIR|0o755 {
		t.Fatalf("writable root Getattr errno=%v attr=%#v", failure, rootAttr.Attr)
	}
	child, failure := root.Lookup(t.Context(), "old.txt", &fuse.EntryOut{})
	if failure != 0 || child == nil {
		t.Fatalf("Lookup child=%v errno=%v", child, failure)
	}
	if !root.AddChild("old.txt", child, true) {
		t.Fatal("attach looked-up child")
	}
	operations := child.Operations().(*node)
	var fileAttr fuse.AttrOut
	if failure := operations.Getattr(t.Context(), nil, &fileAttr); failure != 0 || fileAttr.Mode != fuse.S_IFREG|0o644 {
		t.Fatalf("writable file Getattr errno=%v attr=%#v", failure, fileAttr.Attr)
	}
	handleValue, flags, failure := operations.Open(t.Context(), syscall.O_RDWR)
	if failure != 0 || flags != fuse.FOPEN_DIRECT_IO {
		t.Fatalf("writable Open errno=%v flags=%#x", failure, flags)
	}
	handle := handleValue.(*writableFileHandle)
	if written, failure := handle.Write(t.Context(), []byte("AFTER"), 0); failure != 0 || written != 5 {
		t.Fatalf("Write bytes=%d errno=%v", written, failure)
	}
	result, failure := handle.Read(t.Context(), make([]byte, 6), 0)
	if failure != 0 {
		t.Fatal(failure)
	}
	body, status := result.Bytes(nil)
	result.Done()
	if !status.Ok() || string(body) != "AFTERe" {
		t.Fatalf("fresh read status=%v body=%q", status, body)
	}
	truncate := &fuse.SetAttrIn{}
	truncate.Valid = fuse.FATTR_SIZE | fuse.FATTR_FH
	truncate.Size = 3
	if failure := handle.Setattr(t.Context(), truncate, &fuse.AttrOut{}); failure != 0 {
		t.Fatalf("truncate errno=%v", failure)
	}
	if failure := handle.Fsync(t.Context(), 0); failure != 0 {
		t.Fatalf("fsync errno=%v", failure)
	}
	if failure := handle.Flush(t.Context()); failure != 0 {
		t.Fatalf("flush errno=%v", failure)
	}

	if failure := root.Rename(t.Context(), "old.txt", root, "moved.txt", 0); failure != 0 {
		t.Fatalf("rename errno=%v", failure)
	}
	if !root.MvChild("old.txt", root.EmbeddedInode(), "moved.txt", true) {
		t.Fatal("move child in simulated go-fuse tree")
	}
	if written, failure := handle.Write(t.Context(), []byte("!"), 3); failure != 0 || written != 1 {
		t.Fatalf("write after rename bytes=%d errno=%v", written, failure)
	}
	filesystem.mu.Lock()
	moved := string(filesystem.bodies["moved.txt"])
	_, oldExists := filesystem.bodies["old.txt"]
	filesystem.mu.Unlock()
	if moved != "AFT!" || oldExists {
		t.Fatalf("renamed handle body=%q oldExists=%v", moved, oldExists)
	}
	if failure := handle.Release(t.Context()); failure != 0 {
		t.Fatal(failure)
	}
	if _, failure := handle.Write(t.Context(), []byte("x"), 0); failure != syscall.EBADF {
		t.Fatalf("write after release errno=%v", failure)
	}
}

func TestNodeMapsWritableCreateNamespaceTruncateAndPolicyFailures(t *testing.T) {
	filesystem := newFakeWritableFilesystem()
	root := newRoot(filesystem, filesystem)
	_ = fs.NewNodeFS(root, &fs.Options{RootStableAttr: &fs.StableAttr{Mode: fuse.S_IFDIR}})

	child, handleValue, flags, failure := root.Create(t.Context(), "new.txt", syscall.O_RDWR|syscall.O_CREAT, 0o600, &fuse.EntryOut{})
	if failure != 0 || child == nil || handleValue == nil || flags != fuse.FOPEN_DIRECT_IO {
		t.Fatalf("Create child=%v handle=%T flags=%#x errno=%v", child, handleValue, flags, failure)
	}
	handle := handleValue.(*writableFileHandle)
	if written, failure := handle.Write(t.Context(), []byte("created"), 0); failure != 0 || written != 7 {
		t.Fatalf("created Write bytes=%d errno=%v", written, failure)
	}
	if failure := handle.Release(t.Context()); failure != 0 {
		t.Fatal(failure)
	}
	if directory, failure := root.Mkdir(t.Context(), "docs", 0o700, &fuse.EntryOut{}); failure != 0 || directory == nil {
		t.Fatalf("Mkdir child=%v errno=%v", directory, failure)
	}
	if _, failure := root.Mkdir(t.Context(), "docs", 0o700, &fuse.EntryOut{}); failure != syscall.EEXIST {
		t.Fatalf("duplicate Mkdir errno=%v", failure)
	}
	if failure := root.Rmdir(t.Context(), "docs"); failure != 0 {
		t.Fatalf("Rmdir errno=%v", failure)
	}
	if failure := root.Unlink(t.Context(), "new.txt"); failure != 0 {
		t.Fatalf("Unlink errno=%v", failure)
	}
	old, failure := root.Lookup(t.Context(), "old.txt", &fuse.EntryOut{})
	if failure != 0 {
		t.Fatal(failure)
	}
	oldOperations := old.Operations().(*node)
	if _, _, failure := oldOperations.Open(t.Context(), syscall.O_RDONLY|syscall.O_TRUNC); failure != syscall.EINVAL {
		t.Fatalf("read-only truncating Open errno=%v", failure)
	}
	if _, _, failure := oldOperations.Open(t.Context(), syscall.O_RDWR|syscall.O_TRUNC); failure != 0 {
		t.Fatalf("writable truncating Open errno=%v", failure)
	}
	metadata := &fuse.SetAttrIn{}
	metadata.Valid = fuse.FATTR_MODE
	if failure := root.Setattr(t.Context(), nil, metadata, &fuse.AttrOut{}); failure != syscall.ENOTSUP {
		t.Fatalf("metadata Setattr errno=%v", failure)
	}
	if failure := root.Rename(t.Context(), "old.txt", root, "new.txt", 1); failure != syscall.ENOTSUP {
		t.Fatalf("flagged Rename errno=%v", failure)
	}
	if failure := root.Setxattr(t.Context(), "user.test", []byte("x"), 0); failure != syscall.ENOTSUP {
		t.Fatalf("writable unsupported xattr errno=%v", failure)
	}

	filesystem.syncResult = filesystemmount.SyncResult{}
	if failure := root.Fsync(t.Context(), nil, 0); failure != syscall.EIO {
		t.Fatalf("non-durable Sync errno=%v", failure)
	}
	filesystem.syncResult = filesystemmount.SyncResult{LocalDurable: true, RootAccepted: true}
	if failure := root.Fsync(t.Context(), nil, 0); failure != syscall.EIO {
		t.Fatalf("accepted-root Sync errno=%v", failure)
	}
	filesystem.syncResult = filesystemmount.SyncResult{LocalDurable: true, RemotePersisted: true}
	if failure := root.Fsync(t.Context(), nil, 0); failure != syscall.EIO {
		t.Fatalf("candidate-less remote Sync errno=%v", failure)
	}
}

func TestWritableHandleConcurrentOffsetWrites(t *testing.T) {
	filesystem := newFakeWritableFilesystem()
	root := newRoot(filesystem, filesystem)
	_ = fs.NewNodeFS(root, &fs.Options{RootStableAttr: &fs.StableAttr{Mode: fuse.S_IFDIR}})
	child, failure := root.Lookup(t.Context(), "old.txt", &fuse.EntryOut{})
	if failure != 0 {
		t.Fatal(failure)
	}
	handleValue, _, failure := child.Operations().(*node).Open(t.Context(), syscall.O_RDWR|syscall.O_TRUNC)
	if failure != 0 {
		t.Fatal(failure)
	}
	handle := handleValue.(*writableFileHandle)
	const writers = 64
	var group sync.WaitGroup
	errorsByWriter := make(chan syscall.Errno, writers)
	for index := 0; index < writers; index++ {
		index := index
		group.Add(1)
		go func() {
			defer group.Done()
			written, failure := handle.Write(t.Context(), []byte{byte(index)}, int64(index))
			if failure != 0 || written != 1 {
				errorsByWriter <- failure
			}
		}()
	}
	group.Wait()
	close(errorsByWriter)
	for failure := range errorsByWriter {
		t.Fatalf("concurrent write errno=%v", failure)
	}
	result, failure := handle.Read(t.Context(), make([]byte, writers), 0)
	if failure != 0 {
		t.Fatal(failure)
	}
	body, status := result.Bytes(nil)
	result.Done()
	if !status.Ok() || len(body) != writers {
		t.Fatalf("concurrent body len=%d status=%v", len(body), status)
	}
	for index, value := range body {
		if value != byte(index) {
			t.Fatalf("body[%d]=%d", index, value)
		}
	}
}

func TestWritableNamespaceNeverRetargetsOrphanedOpenInodes(t *testing.T) {
	filesystem := newFakeWritableFilesystem()
	if _, err := filesystem.Create(t.Context(), "source.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := filesystem.Create(t.Context(), "destination.txt"); err != nil {
		t.Fatal(err)
	}
	root := newRoot(filesystem, filesystem)
	_ = fs.NewNodeFS(root, &fs.Options{RootStableAttr: &fs.StableAttr{Mode: fuse.S_IFDIR}})
	destination, failure := root.Lookup(t.Context(), "destination.txt", &fuse.EntryOut{})
	if failure != 0 {
		t.Fatal(failure)
	}
	destinationNode := destination.Operations().(*node)
	destinationHandleValue, _, failure := destinationNode.Open(t.Context(), syscall.O_RDWR)
	if failure != 0 {
		t.Fatal(failure)
	}
	destinationHandle := destinationHandleValue.(*writableFileHandle)
	if failure := root.Rename(t.Context(), "source.txt", root, "destination.txt", 0); failure != syscall.EBUSY {
		t.Fatalf("overwrite open destination errno=%v", failure)
	}
	if failure := destinationHandle.Release(t.Context()); failure != 0 {
		t.Fatal(failure)
	}
	if failure := root.Rename(t.Context(), "source.txt", root, "destination.txt", 0); failure != 0 {
		t.Fatalf("overwrite released destination errno=%v", failure)
	}
	if failure := destinationNode.Getattr(t.Context(), nil, &fuse.AttrOut{}); failure != syscall.ESTALE {
		t.Fatalf("overwritten destination inode errno=%v", failure)
	}

	replacement, failure := root.Lookup(t.Context(), "destination.txt", &fuse.EntryOut{})
	if failure != 0 {
		t.Fatal(failure)
	}
	replacementNode := replacement.Operations().(*node)
	replacementHandleValue, _, failure := replacementNode.Open(t.Context(), syscall.O_RDWR)
	if failure != 0 {
		t.Fatal(failure)
	}
	replacementHandle := replacementHandleValue.(*writableFileHandle)
	if failure := root.Unlink(t.Context(), "destination.txt"); failure != syscall.EBUSY {
		t.Fatalf("unlink open inode errno=%v", failure)
	}
	if failure := replacementHandle.Release(t.Context()); failure != 0 {
		t.Fatal(failure)
	}
	if failure := root.Unlink(t.Context(), "destination.txt"); failure != 0 {
		t.Fatalf("unlink released inode errno=%v", failure)
	}
	if _, _, _, failure := root.Create(t.Context(), "destination.txt", syscall.O_RDONLY|syscall.O_CREAT, 0o600, &fuse.EntryOut{}); failure != 0 {
		t.Fatalf("recreate destination errno=%v", failure)
	}
	if failure := replacementNode.Getattr(t.Context(), nil, &fuse.AttrOut{}); failure != syscall.ESTALE {
		t.Fatalf("unlinked inode retargeted after recreate: errno=%v", failure)
	}
}

func TestForgottenNodeRegistryLifetimeFollowsOpenHandles(t *testing.T) {
	filesystem := newFakeWritableFilesystem()
	root := newRoot(filesystem, filesystem)
	_ = fs.NewNodeFS(root, &fs.Options{RootStableAttr: &fs.StableAttr{Mode: fuse.S_IFDIR}})
	child, failure := root.Lookup(t.Context(), "old.txt", &fuse.EntryOut{})
	if failure != 0 {
		t.Fatal(failure)
	}
	operations := child.Operations().(*node)
	handleValue, _, failure := operations.Open(t.Context(), syscall.O_RDWR)
	if failure != 0 {
		t.Fatal(failure)
	}
	handle := handleValue.(*writableFileHandle)

	operations.OnForget()
	root.mount.mu.RLock()
	_, retainedWhileOpen := root.mount.nodes[operations]
	root.mount.mu.RUnlock()
	if !retainedWhileOpen {
		t.Fatal("forgotten inode with an open handle was removed from the logical-path registry")
	}
	if _, _, failure := operations.Open(t.Context(), syscall.O_RDWR); failure != syscall.ESTALE {
		t.Fatalf("forgotten inode was revived by a new Open: errno=%v", failure)
	}
	if failure := operations.Getattr(t.Context(), nil, &fuse.AttrOut{}); failure != syscall.ESTALE {
		t.Fatalf("forgotten inode was revived by Getattr: errno=%v", failure)
	}
	if failure := root.Rename(t.Context(), "old.txt", root, "moved.txt", 0); failure != 0 {
		t.Fatalf("rename forgotten inode with existing handle errno=%v", failure)
	}
	if written, failure := handle.Write(t.Context(), []byte("!"), 6); failure != 0 || written != 1 {
		t.Fatalf("existing handle after forget and rename bytes=%d errno=%v", written, failure)
	}
	var handleAttr fuse.AttrOut
	if failure := operations.Getattr(t.Context(), handle, &handleAttr); failure != 0 || handleAttr.Size != 7 {
		t.Fatalf("existing handle Getattr after forget and rename errno=%v attr=%#v", failure, handleAttr.Attr)
	}
	filesystem.mu.Lock()
	movedBody := string(filesystem.bodies["moved.txt"])
	_, oldBodyExists := filesystem.bodies["old.txt"]
	filesystem.mu.Unlock()
	if movedBody != "before!" || oldBodyExists {
		t.Fatalf("forgotten handle path was not moved atomically: body=%q oldExists=%v", movedBody, oldBodyExists)
	}
	if failure := handle.Release(t.Context()); failure != 0 {
		t.Fatal(failure)
	}
	root.mount.mu.RLock()
	_, retainedAfterRelease := root.mount.nodes[operations]
	root.mount.mu.RUnlock()
	if retainedAfterRelease {
		t.Fatal("forgotten inode remained in the logical-path registry after its last handle closed")
	}

	directory, failure := root.Mkdir(t.Context(), "docs", 0o700, &fuse.EntryOut{})
	if failure != 0 {
		t.Fatal(failure)
	}
	directoryOperations := directory.Operations().(*node)
	directoryOperations.OnForget()
	root.mount.mu.RLock()
	_, retainedDirectory := root.mount.nodes[directoryOperations]
	root.mount.mu.RUnlock()
	if retainedDirectory {
		t.Fatal("forgotten inode without an open handle remained in the logical-path registry")
	}
	if _, failure := directoryOperations.Mkdir(t.Context(), "child", 0o700, &fuse.EntryOut{}); failure != syscall.ESTALE {
		t.Fatalf("forgotten directory was revived by Mkdir: errno=%v", failure)
	}
}

func TestReadOnlyHandleGetattrSurvivesForget(t *testing.T) {
	filesystem := &fakeFilesystem{
		infos: map[string]filesystemservice.Info{
			"":        {Path: "", Kind: unixfs.StagedKindDirectory},
			"old.txt": {Path: "old.txt", Name: "old.txt", Kind: unixfs.StagedKindFile, Size: 6},
		},
		dirs:    map[string][]filesystemservice.DirEntry{},
		bodies:  map[string][]byte{"old.txt": []byte("before")},
		statErr: map[string]error{},
	}
	root := newRoot(filesystem, nil)
	_ = fs.NewNodeFS(root, &fs.Options{RootStableAttr: &fs.StableAttr{Mode: fuse.S_IFDIR}})
	child, failure := root.Lookup(t.Context(), "old.txt", &fuse.EntryOut{})
	if failure != 0 {
		t.Fatal(failure)
	}
	operations := child.Operations().(*node)
	handleValue, _, failure := operations.Open(t.Context(), syscall.O_RDONLY)
	if failure != 0 {
		t.Fatal(failure)
	}
	handle := handleValue.(*fileHandle)
	operations.OnForget()
	if failure := operations.Getattr(t.Context(), nil, &fuse.AttrOut{}); failure != syscall.ESTALE {
		t.Fatalf("forgotten read-only inode Getattr errno=%v", failure)
	}
	var out fuse.AttrOut
	if failure := operations.Getattr(t.Context(), handle, &out); failure != 0 || out.Size != 6 || out.Mode != fuse.S_IFREG|0o444 {
		t.Fatalf("forgotten read-only handle Getattr errno=%v attr=%#v", failure, out.Attr)
	}
	other, failure := root.Lookup(t.Context(), "old.txt", &fuse.EntryOut{})
	if failure != 0 {
		t.Fatal(failure)
	}
	if failure := other.Operations().(*node).Getattr(t.Context(), handle, &fuse.AttrOut{}); failure != syscall.EBADF {
		t.Fatalf("cross-inode read handle Getattr errno=%v", failure)
	}
	if failure := handle.Release(t.Context()); failure != 0 {
		t.Fatal(failure)
	}
}

func TestForgetAndOpenSerializeRegistryOwnership(t *testing.T) {
	const attempts = 128
	for attempt := 0; attempt < attempts; attempt++ {
		filesystem := newFakeWritableFilesystem()
		root := newRoot(filesystem, filesystem)
		_ = fs.NewNodeFS(root, &fs.Options{RootStableAttr: &fs.StableAttr{Mode: fuse.S_IFDIR}})
		child, failure := root.Lookup(t.Context(), "old.txt", &fuse.EntryOut{})
		if failure != 0 {
			t.Fatal(failure)
		}
		operations := child.Operations().(*node)
		start := make(chan struct{})
		var group sync.WaitGroup
		group.Add(2)
		var handle fs.FileHandle
		var openFailure syscall.Errno
		go func() {
			defer group.Done()
			<-start
			handle, _, openFailure = operations.Open(t.Context(), syscall.O_RDWR)
		}()
		go func() {
			defer group.Done()
			<-start
			operations.OnForget()
		}()
		close(start)
		group.Wait()

		root.mount.mu.RLock()
		_, retained := root.mount.nodes[operations]
		root.mount.mu.RUnlock()
		switch openFailure {
		case 0:
			if !retained {
				t.Fatalf("attempt %d: successful concurrent Open lost registry ownership", attempt)
			}
			if failure := handle.(*writableFileHandle).Release(t.Context()); failure != 0 {
				t.Fatal(failure)
			}
		case syscall.ESTALE:
			if retained {
				t.Fatalf("attempt %d: rejected concurrent Open retained forgotten inode", attempt)
			}
		default:
			t.Fatalf("attempt %d: concurrent Open errno=%v", attempt, openFailure)
		}
	}
}

func TestWritableRenameNoopAndSelfSubtreeSemantics(t *testing.T) {
	filesystem := newFakeWritableFilesystem()
	root := newRoot(filesystem, filesystem)
	_ = fs.NewNodeFS(root, &fs.Options{RootStableAttr: &fs.StableAttr{Mode: fuse.S_IFDIR}})
	if failure := root.Rename(t.Context(), "old.txt", root, "old.txt", 0); failure != 0 {
		t.Fatalf("same-path rename errno=%v", failure)
	}
	if failure := root.Rename(t.Context(), "missing.txt", root, "missing.txt", 0); failure != syscall.ENOENT {
		t.Fatalf("missing same-path rename errno=%v", failure)
	}
	directory, failure := root.Mkdir(t.Context(), "docs", 0o700, &fuse.EntryOut{})
	if failure != 0 {
		t.Fatal(failure)
	}
	if failure := root.Rename(t.Context(), "docs", directory.Operations(), "child", 0); failure != syscall.EINVAL {
		t.Fatalf("self-subtree rename errno=%v", failure)
	}
}

func TestReadHandleResultsAreNilSafeAndPartialResultsAreClosed(t *testing.T) {
	for _, writable := range []bool{false, true} {
		name := "read-only-open"
		if writable {
			name = "writable-fresh-read"
		}
		t.Run(name, func(t *testing.T) {
			var typedNil *fakeReadHandle
			nilFilesystem := &adversarialOpenFilesystem{fakeWritableFilesystem: newFakeWritableFilesystem(), handle: typedNil}
			var writableCapability filesystemmount.WritableFilesystem
			if writable {
				writableCapability = nilFilesystem
			}
			root := newRoot(nilFilesystem, writableCapability)
			_ = fs.NewNodeFS(root, &fs.Options{RootStableAttr: &fs.StableAttr{Mode: fuse.S_IFDIR}})
			child, failure := root.Lookup(t.Context(), "old.txt", &fuse.EntryOut{})
			if failure != 0 {
				t.Fatal(failure)
			}
			if writable {
				handleValue, _, failure := child.Operations().(*node).Open(t.Context(), syscall.O_RDONLY)
				if failure != 0 {
					t.Fatal(failure)
				}
				if _, failure := handleValue.(*writableFileHandle).Read(t.Context(), make([]byte, 1), 0); failure != syscall.EIO {
					t.Fatalf("typed-nil fresh read errno=%v", failure)
				}
			} else if _, _, failure := child.Operations().(*node).Open(t.Context(), syscall.O_RDONLY); failure != syscall.EIO {
				t.Fatalf("typed-nil read-only Open errno=%v", failure)
			}

			partial := &fakeReadHandle{
				info: filesystemservice.Info{Path: "old.txt", Name: "old.txt", Kind: unixfs.StagedKindFile, Size: 6},
				body: []byte("before"),
			}
			partialFilesystem := &adversarialOpenFilesystem{
				fakeWritableFilesystem: newFakeWritableFilesystem(), handle: partial, err: errors.New("partial open failure"),
			}
			writableCapability = nil
			if writable {
				writableCapability = partialFilesystem
			}
			root = newRoot(partialFilesystem, writableCapability)
			_ = fs.NewNodeFS(root, &fs.Options{RootStableAttr: &fs.StableAttr{Mode: fuse.S_IFDIR}})
			child, failure = root.Lookup(t.Context(), "old.txt", &fuse.EntryOut{})
			if failure != 0 {
				t.Fatal(failure)
			}
			if writable {
				handleValue, _, failure := child.Operations().(*node).Open(t.Context(), syscall.O_RDONLY)
				if failure != 0 {
					t.Fatal(failure)
				}
				if _, failure := handleValue.(*writableFileHandle).Read(t.Context(), make([]byte, 1), 0); failure != syscall.EIO {
					t.Fatalf("partial fresh read errno=%v", failure)
				}
			} else if _, _, failure := child.Operations().(*node).Open(t.Context(), syscall.O_RDONLY); failure != syscall.EIO {
				t.Fatalf("partial read-only Open errno=%v", failure)
			}
			if !partial.isClosed() {
				t.Fatal("partial read handle was not closed")
			}
		})
	}
}

func TestWritableWriteRejectsShortCapabilityResult(t *testing.T) {
	filesystem := &shortWriteFilesystem{fakeWritableFilesystem: newFakeWritableFilesystem()}
	root := newRoot(filesystem, filesystem)
	_ = fs.NewNodeFS(root, &fs.Options{RootStableAttr: &fs.StableAttr{Mode: fuse.S_IFDIR}})
	child, failure := root.Lookup(t.Context(), "old.txt", &fuse.EntryOut{})
	if failure != 0 {
		t.Fatal(failure)
	}
	handleValue, _, failure := child.Operations().(*node).Open(t.Context(), syscall.O_RDWR)
	if failure != 0 {
		t.Fatal(failure)
	}
	if written, failure := handleValue.(*writableFileHandle).Write(t.Context(), []byte("extended"), 6); failure != syscall.EIO || written != 0 {
		t.Fatalf("short WriteAt result bytes=%d errno=%v", written, failure)
	}
}

func TestWritableErrnoContract(t *testing.T) {
	for _, test := range []struct {
		err  error
		want syscall.Errno
	}{
		{filesystemmount.ErrAlreadyExists, syscall.EEXIST},
		{filesystemmount.ErrNotEmpty, syscall.ENOTEMPTY},
		{filesystemmount.ErrFileTooLarge, syscall.EFBIG},
		{unixfs.ErrNotFile, syscall.EISDIR},
		{unixfs.ErrNotDirectory, syscall.ENOTDIR},
	} {
		if got := errno(test.err); got != test.want {
			t.Fatalf("errno(%v)=%v, want %v", test.err, got, test.want)
		}
	}
}

func TestNodeFailsClosedForWritesInvalidNamesAndRemoteErrors(t *testing.T) {
	filesystem := &fakeFilesystem{
		infos: map[string]filesystemservice.Info{
			"":        {Kind: unixfs.StagedKindDirectory},
			"unknown": {Kind: "device"},
		},
		dirs: map[string][]filesystemservice.DirEntry{
			"": {{Name: "bad/name", Kind: unixfs.StagedKindFile}},
		},
		bodies:  map[string][]byte{},
		statErr: map[string]error{"missing": unixfs.ErrNotFound, "broken": errors.New("invalid proof")},
	}
	root := newRoot(filesystem, nil)
	_ = fs.NewNodeFS(root, &fs.Options{RootStableAttr: &fs.StableAttr{Mode: fuse.S_IFDIR}})
	for _, name := range []string{"", ".", "..", "bad/name", `bad\name`, "@payload"} {
		if child, errno := root.Lookup(t.Context(), name, &fuse.EntryOut{}); child != nil || errno != syscall.ENOENT {
			t.Fatalf("Lookup(%q) = %v, %v", name, child, errno)
		}
	}
	if _, errno := root.Lookup(t.Context(), "missing", &fuse.EntryOut{}); errno != syscall.ENOENT {
		t.Fatalf("missing errno=%v", errno)
	}
	if _, errno := root.Lookup(t.Context(), "broken", &fuse.EntryOut{}); errno != syscall.EIO {
		t.Fatalf("broken proof errno=%v", errno)
	}
	if _, errno := root.Lookup(t.Context(), "unknown", &fuse.EntryOut{}); errno != syscall.EIO {
		t.Fatalf("unknown kind errno=%v", errno)
	}
	if _, errno := root.Readdir(t.Context()); errno != syscall.EIO {
		t.Fatalf("invalid directory entry errno=%v", errno)
	}
	if _, _, errno := root.Open(t.Context(), syscall.O_WRONLY); errno != syscall.EROFS {
		t.Fatalf("write Open errno=%v", errno)
	}
	if errno := root.Unlink(t.Context(), "anything"); errno != syscall.EROFS {
		t.Fatalf("Unlink errno=%v", errno)
	}
	if _, errno := root.Mkdir(t.Context(), "anything", 0o755, &fuse.EntryOut{}); errno != syscall.EROFS {
		t.Fatalf("Mkdir errno=%v", errno)
	}
	if errno := root.Rename(t.Context(), "a", root, "b", 0); errno != syscall.EROFS {
		t.Fatalf("Rename errno=%v", errno)
	}
	if _, errno := root.Write(t.Context(), nil, []byte("x"), 0); errno != syscall.EROFS {
		t.Fatalf("Write errno=%v", errno)
	}
	if errno := root.Allocate(t.Context(), nil, 0, 1, 0); errno != syscall.EROFS {
		t.Fatalf("Allocate errno=%v", errno)
	}
	if _, errno := root.CopyFileRange(t.Context(), nil, 0, root.EmbeddedInode(), nil, 0, 1, 0); errno != syscall.EROFS {
		t.Fatalf("CopyFileRange errno=%v", errno)
	}
	if errno := root.Setxattr(t.Context(), "user.test", []byte("x"), 0); errno != syscall.EROFS {
		t.Fatalf("Setxattr errno=%v", errno)
	}
	if errno := root.Removexattr(t.Context(), "user.test"); errno != syscall.EROFS {
		t.Fatalf("Removexattr errno=%v", errno)
	}
	if size, errno := root.Getxattr(t.Context(), "user.test", nil); size != 0 || errno != syscall.ENODATA {
		t.Fatalf("Getxattr size=%d errno=%v", size, errno)
	}
	if size, errno := root.Listxattr(t.Context(), nil); size != 0 || errno != 0 {
		t.Fatalf("Listxattr size=%d errno=%v", size, errno)
	}
	file := &fileHandle{}
	if _, errno := file.Write(t.Context(), []byte("x"), 0); errno != syscall.EROFS {
		t.Fatalf("file Write errno=%v", errno)
	}
	if errno := file.Setattr(t.Context(), &fuse.SetAttrIn{}, &fuse.AttrOut{}); errno != syscall.EROFS {
		t.Fatalf("file Setattr errno=%v", errno)
	}
	if errno := file.Allocate(t.Context(), 0, 1, 0); errno != syscall.EROFS {
		t.Fatalf("file Allocate errno=%v", errno)
	}
}

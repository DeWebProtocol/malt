//go:build linux

package fusefs

import (
	"context"
	"errors"
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
	root := newRoot(filesystem)
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
	root := newRoot(filesystem)
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
}

//go:build linux

package fusefs

import (
	"context"
	"errors"
	"os"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	filesystemmount "github.com/dewebprotocol/malt-client/filesystem/mount"
	filesystemservice "github.com/dewebprotocol/malt-client/filesystem/service"
	"github.com/dewebprotocol/malt-client/unixfs"
	unixfsmodel "github.com/dewebprotocol/malt-client/unixfs/model"
	"github.com/hanwen/go-fuse/v2/fs"
	"github.com/hanwen/go-fuse/v2/fuse"
)

const metadataTimeout = time.Second

type mountedFilesystem struct {
	readOnly filesystemmount.ReadOnlyFilesystem
	writable filesystemmount.WritableFilesystem
	mu       sync.RWMutex
	nodes    map[*node]struct{}
}

type node struct {
	fs.Inode
	mount       *mountedFilesystem
	logicalPath string
	orphaned    bool
	forgotten   bool
	openHandles uint64
	uid         uint32
	gid         uint32
}

func newRoot(readOnly filesystemmount.ReadOnlyFilesystem, writable filesystemmount.WritableFilesystem) *node {
	mount := &mountedFilesystem{readOnly: readOnly, writable: writable, nodes: map[*node]struct{}{}}
	root := &node{
		mount: mount,
		uid:   uint32(os.Getuid()), gid: uint32(os.Getgid()),
	}
	mount.nodes[root] = struct{}{}
	return root
}

func (n *node) nodePathLocked() (string, bool) {
	if n == nil || n.orphaned || n.forgotten {
		return "", false
	}
	return n.logicalPath, true
}

func (n *node) handlePathLocked() (string, bool) {
	if n == nil || n.orphaned {
		return "", false
	}
	return n.logicalPath, true
}

func (n *node) childPathLocked(name string) (string, syscall.Errno) {
	segments, err := unixfsmodel.ParsePath(name)
	if err != nil || len(segments) != 1 {
		return "", syscall.EINVAL
	}
	parent, ok := n.nodePathLocked()
	if !ok {
		return "", syscall.ESTALE
	}
	return path.Join(parent, name), 0
}

func (n *node) OnForget() {
	if n == nil || n.mount == nil {
		return
	}
	n.mount.mu.Lock()
	defer n.mount.mu.Unlock()
	n.forgotten = true
	if n.openHandles == 0 {
		delete(n.mount.nodes, n)
	}
}

func (n *node) Getattr(ctx context.Context, handle fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	switch opened := handle.(type) {
	case *writableFileHandle:
		if opened == nil || opened.node != n {
			return syscall.EBADF
		}
		return opened.Getattr(ctx, out)
	case *fileHandle:
		if opened == nil || opened.node != n {
			return syscall.EBADF
		}
		return opened.Getattr(ctx, out)
	}
	n.mount.mu.RLock()
	defer n.mount.mu.RUnlock()
	currentPath, ok := n.nodePathLocked()
	if !ok {
		return syscall.ESTALE
	}
	return n.getattrLocked(ctx, currentPath, out)
}

func (n *node) getattrLocked(ctx context.Context, currentPath string, out *fuse.AttrOut) syscall.Errno {
	info, err := n.mount.readOnly.Stat(ctx, currentPath)
	if err != nil {
		return errno(err)
	}
	if !validInfoPath(info, currentPath) {
		return syscall.EIO
	}
	mode, ok := modeForInfo(info)
	if !ok {
		return syscall.EIO
	}
	n.setAttr(&out.Attr, info, mode)
	out.SetTimeout(metadataTimeout)
	return 0
}

func (n *node) Lookup(ctx context.Context, name string, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	n.mount.mu.Lock()
	defer n.mount.mu.Unlock()
	childPath, invalid := n.childPathLocked(name)
	if invalid != 0 {
		return nil, syscall.ENOENT
	}
	info, err := n.mount.readOnly.Stat(ctx, childPath)
	if err != nil {
		return nil, errno(err)
	}
	return n.projectChildLocked(ctx, childPath, info, out)
}

func (n *node) projectChildLocked(ctx context.Context, childPath string, info filesystemservice.Info, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if !validInfoPath(info, childPath) {
		return nil, syscall.EIO
	}
	mode, ok := modeForInfo(info)
	if !ok {
		return nil, syscall.EIO
	}
	child := &node{mount: n.mount, logicalPath: childPath, uid: n.uid, gid: n.gid}
	n.mount.nodes[child] = struct{}{}
	stable := fs.StableAttr{Mode: mode}
	n.setAttr(&out.Attr, info, mode)
	out.SetEntryTimeout(metadataTimeout)
	out.SetAttrTimeout(metadataTimeout)
	return n.NewInode(ctx, child, stable), 0
}

func (n *node) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	n.mount.mu.RLock()
	defer n.mount.mu.RUnlock()
	currentPath, ok := n.nodePathLocked()
	if !ok {
		return nil, syscall.ESTALE
	}
	entries, err := n.mount.readOnly.ReadDir(ctx, currentPath)
	if err != nil {
		return nil, errno(err)
	}
	result := make([]fuse.DirEntry, len(entries))
	for index, entry := range entries {
		segments, err := unixfsmodel.ParsePath(entry.Name)
		if err != nil || len(segments) != 1 {
			return nil, syscall.EIO
		}
		mode := uint32(fuse.S_IFREG)
		if entry.Kind == unixfs.StagedKindDirectory {
			mode = fuse.S_IFDIR
		} else if entry.Kind != unixfs.StagedKindFile {
			return nil, syscall.EIO
		}
		result[index] = fuse.DirEntry{Name: entry.Name, Mode: mode}
	}
	return fs.NewListDirStream(result), 0
}

func (n *node) Open(ctx context.Context, flags uint32) (fs.FileHandle, uint32, syscall.Errno) {
	readable, writable, invalid := accessMode(flags)
	if invalid != 0 {
		return nil, 0, invalid
	}
	n.mount.mu.Lock()
	defer n.mount.mu.Unlock()
	currentPath, ok := n.nodePathLocked()
	if !ok {
		return nil, 0, syscall.ESTALE
	}
	if n.mount.writable == nil {
		if !readable || writable || flags&(syscall.O_APPEND|syscall.O_CREAT|syscall.O_TRUNC) != 0 {
			return nil, 0, syscall.EROFS
		}
		handle, _, err := n.openReadHandleLocked(ctx, currentPath)
		if err != nil {
			return nil, 0, errno(err)
		}
		return &fileHandle{node: n, handle: handle, uid: n.uid, gid: n.gid}, fuse.FOPEN_KEEP_CACHE, 0
	}
	info, err := n.mount.writable.Stat(ctx, currentPath)
	if err != nil {
		return nil, 0, errno(err)
	}
	if !validInfoPath(info, currentPath) {
		return nil, 0, syscall.EIO
	}
	if info.IsDir() {
		return nil, 0, syscall.EISDIR
	}
	if info.Kind != unixfs.StagedKindFile {
		return nil, 0, syscall.EIO
	}
	if flags&syscall.O_TRUNC != 0 {
		if !writable {
			return nil, 0, syscall.EINVAL
		}
		info, err := n.mount.writable.Truncate(ctx, currentPath, 0)
		if err != nil {
			return nil, 0, errno(err)
		}
		if info.Kind != unixfs.StagedKindFile || !validInfoPath(info, currentPath) {
			return nil, 0, syscall.EIO
		}
	}
	n.openHandles++
	return &writableFileHandle{node: n, readable: readable, writable: writable}, fuse.FOPEN_DIRECT_IO, 0
}

func (n *node) openReadHandleLocked(ctx context.Context, currentPath string) (filesystemmount.ReadHandle, filesystemservice.Info, error) {
	handle, openErr := n.mount.readOnly.Open(ctx, currentPath)
	if isNilCapability(handle) {
		handle = nil
	}
	if openErr != nil {
		if handle != nil {
			openErr = errors.Join(openErr, handle.Close())
		}
		return nil, filesystemservice.Info{}, openErr
	}
	if handle == nil {
		return nil, filesystemservice.Info{}, errors.New("filesystem returned a nil read handle")
	}
	info := handle.Info()
	if info.Kind != unixfs.StagedKindFile || !validInfoPath(info, currentPath) {
		return nil, filesystemservice.Info{}, errors.Join(errors.New("filesystem returned an invalid read handle"), handle.Close())
	}
	return handle, info, nil
}

func accessMode(flags uint32) (readable, writable bool, errno syscall.Errno) {
	switch flags & syscall.O_ACCMODE {
	case syscall.O_RDONLY:
		return true, false, 0
	case syscall.O_WRONLY:
		return false, true, 0
	case syscall.O_RDWR:
		return true, true, 0
	default:
		return false, false, syscall.EINVAL
	}
}

func (n *node) Setattr(ctx context.Context, handle fs.FileHandle, input *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	if n.mount.writable == nil {
		return syscall.EROFS
	}
	if writable, ok := handle.(*writableFileHandle); ok {
		return writable.Setattr(ctx, input, out)
	}
	return n.truncateAttr(ctx, input, out, false)
}

func (n *node) truncateAttr(ctx context.Context, input *fuse.SetAttrIn, out *fuse.AttrOut, fromHandle bool) syscall.Errno {
	if input == nil || input.Valid&^uint32(fuse.FATTR_SIZE|fuse.FATTR_FH|fuse.FATTR_LOCKOWNER|fuse.FATTR_KILL_SUIDGID) != 0 {
		return n.mutationUnsupported()
	}
	size, ok := input.GetSize()
	if !ok {
		return n.mutationUnsupported()
	}
	n.mount.mu.RLock()
	defer n.mount.mu.RUnlock()
	currentPath, exists := n.nodePathLocked()
	if fromHandle {
		currentPath, exists = n.handlePathLocked()
	}
	if !exists {
		return syscall.ESTALE
	}
	info, err := n.mount.writable.Truncate(ctx, currentPath, size)
	if err != nil {
		return errno(err)
	}
	if info.Kind != unixfs.StagedKindFile || !validInfoPath(info, currentPath) || info.Size != size {
		return syscall.EIO
	}
	n.setAttr(&out.Attr, info, fuse.S_IFREG)
	out.SetTimeout(metadataTimeout)
	return 0
}

func (n *node) Getxattr(context.Context, string, []byte) (uint32, syscall.Errno) {
	return 0, syscall.ENODATA
}

func (n *node) Listxattr(context.Context, []byte) (uint32, syscall.Errno) { return 0, 0 }

func (n *node) Mkdir(ctx context.Context, name string, _ uint32, out *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	if n.mount.writable == nil {
		return nil, syscall.EROFS
	}
	n.mount.mu.Lock()
	defer n.mount.mu.Unlock()
	childPath, invalid := n.childPathLocked(name)
	if invalid != 0 {
		return nil, invalid
	}
	info, err := n.mount.writable.Mkdir(ctx, childPath)
	if err != nil {
		return nil, errno(err)
	}
	if !info.IsDir() {
		return nil, syscall.EIO
	}
	return n.projectChildLocked(ctx, childPath, info, out)
}

func (n *node) Mknod(context.Context, string, uint32, uint32, *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return nil, n.mutationUnsupported()
}

func (n *node) Link(context.Context, fs.InodeEmbedder, string, *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return nil, n.mutationUnsupported()
}

func (n *node) Symlink(context.Context, string, string, *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return nil, n.mutationUnsupported()
}

func (n *node) Create(ctx context.Context, name string, flags uint32, _ uint32, out *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	if n.mount.writable == nil {
		return nil, nil, 0, syscall.EROFS
	}
	readable, writable, invalid := accessMode(flags)
	if invalid != 0 {
		return nil, nil, 0, invalid
	}
	n.mount.mu.Lock()
	defer n.mount.mu.Unlock()
	childPath, invalid := n.childPathLocked(name)
	if invalid != 0 {
		return nil, nil, 0, invalid
	}
	info, err := n.mount.writable.Create(ctx, childPath)
	if err != nil {
		return nil, nil, 0, errno(err)
	}
	if info.Kind != unixfs.StagedKindFile || info.Size != 0 {
		return nil, nil, 0, syscall.EIO
	}
	child, failure := n.projectChildLocked(ctx, childPath, info, out)
	if failure != 0 {
		return nil, nil, 0, failure
	}
	operations, ok := child.Operations().(*node)
	if !ok {
		return nil, nil, 0, syscall.EIO
	}
	operations.openHandles++
	handle := &writableFileHandle{node: operations, readable: readable, writable: writable}
	return child, handle, fuse.FOPEN_DIRECT_IO, 0
}

func (n *node) Unlink(ctx context.Context, name string) syscall.Errno {
	if n.mount.writable == nil {
		return syscall.EROFS
	}
	n.mount.mu.Lock()
	defer n.mount.mu.Unlock()
	childPath, invalid := n.childPathLocked(name)
	if invalid != 0 {
		return invalid
	}
	if n.mount.hasOpenLocked(childPath) {
		return syscall.EBUSY
	}
	if err := n.mount.writable.Unlink(ctx, childPath); err != nil {
		return errno(err)
	}
	n.mount.orphanLocked(childPath)
	return 0
}

func (n *node) Rmdir(ctx context.Context, name string) syscall.Errno {
	if n.mount.writable == nil {
		return syscall.EROFS
	}
	n.mount.mu.Lock()
	defer n.mount.mu.Unlock()
	childPath, invalid := n.childPathLocked(name)
	if invalid != 0 {
		return invalid
	}
	if n.mount.hasOpenLocked(childPath) {
		return syscall.EBUSY
	}
	if err := n.mount.writable.RemoveDir(ctx, childPath); err != nil {
		return errno(err)
	}
	n.mount.orphanLocked(childPath)
	return 0
}

func (n *node) Rename(ctx context.Context, name string, newParent fs.InodeEmbedder, newName string, flags uint32) syscall.Errno {
	if n.mount.writable == nil {
		return syscall.EROFS
	}
	if flags != 0 {
		return syscall.ENOTSUP
	}
	destinationParent, ok := newParent.(*node)
	if !ok || destinationParent.mount != n.mount {
		return syscall.EXDEV
	}
	n.mount.mu.Lock()
	defer n.mount.mu.Unlock()
	source, invalid := n.childPathLocked(name)
	if invalid != 0 {
		return invalid
	}
	destination, invalid := destinationParent.childPathLocked(newName)
	if invalid != 0 {
		return invalid
	}
	if source == destination {
		info, err := n.mount.writable.Stat(ctx, source)
		if err != nil {
			return errno(err)
		}
		if !validInfoPath(info, source) {
			return syscall.EIO
		}
		if _, ok := modeForInfo(info); !ok {
			return syscall.EIO
		}
		return 0
	}
	if isPathWithin(destination, source) {
		return syscall.EINVAL
	}
	if n.mount.hasOpenLocked(destination) {
		return syscall.EBUSY
	}
	if err := n.mount.writable.Rename(ctx, source, destination); err != nil {
		return errno(err)
	}
	n.mount.orphanLocked(destination)
	for candidate := range n.mount.nodes {
		if candidate.orphaned || !isPathWithin(candidate.logicalPath, source) {
			continue
		}
		candidate.logicalPath = replacePathPrefix(candidate.logicalPath, source, destination)
	}
	return 0
}

func (m *mountedFilesystem) hasOpenLocked(target string) bool {
	for candidate := range m.nodes {
		if !candidate.orphaned && candidate.openHandles != 0 && isPathWithin(candidate.logicalPath, target) {
			return true
		}
	}
	return false
}

func (m *mountedFilesystem) orphanLocked(target string) {
	for candidate := range m.nodes {
		if !candidate.orphaned && isPathWithin(candidate.logicalPath, target) {
			candidate.orphaned = true
			candidate.logicalPath = ""
		}
	}
}

func isPathWithin(value, ancestor string) bool {
	return value == ancestor || strings.HasPrefix(value, ancestor+"/")
}

func replacePathPrefix(value, old, replacement string) string {
	if value == old {
		return replacement
	}
	return replacement + strings.TrimPrefix(value, old)
}

func (n *node) Setxattr(context.Context, string, []byte, uint32) syscall.Errno {
	return n.mutationUnsupported()
}

func (n *node) Removexattr(context.Context, string) syscall.Errno {
	return n.mutationUnsupported()
}

func (n *node) Write(ctx context.Context, handle fs.FileHandle, data []byte, offset int64) (uint32, syscall.Errno) {
	if writable, ok := handle.(*writableFileHandle); ok {
		return writable.Write(ctx, data, offset)
	}
	if n.mount.writable == nil {
		return 0, syscall.EROFS
	}
	if offset < 0 {
		return 0, syscall.EINVAL
	}
	end, ok := writeEnd(uint64(offset), len(data))
	if !ok {
		return 0, syscall.EFBIG
	}
	n.mount.mu.RLock()
	defer n.mount.mu.RUnlock()
	currentPath, exists := n.nodePathLocked()
	if !exists {
		return 0, syscall.ESTALE
	}
	info, err := n.mount.writable.WriteAt(ctx, currentPath, uint64(offset), data)
	if err != nil {
		return 0, errno(err)
	}
	if info.Kind != unixfs.StagedKindFile || !validInfoPath(info, currentPath) || (len(data) != 0 && info.Size < end) {
		return 0, syscall.EIO
	}
	return uint32(len(data)), 0
}

func writeEnd(offset uint64, length int) (uint64, bool) {
	end := offset + uint64(length)
	return end, end >= offset
}

func (n *node) Fsync(ctx context.Context, handle fs.FileHandle, flags uint32) syscall.Errno {
	if writable, ok := handle.(*writableFileHandle); ok {
		return writable.Fsync(ctx, flags)
	}
	if n.mount.writable == nil {
		return 0
	}
	return n.sync(ctx, false)
}

func (n *node) Allocate(context.Context, fs.FileHandle, uint64, uint64, uint32) syscall.Errno {
	return n.mutationUnsupported()
}

func (n *node) CopyFileRange(
	context.Context, fs.FileHandle, uint64, *fs.Inode, fs.FileHandle, uint64, uint64, uint64,
) (uint32, syscall.Errno) {
	return 0, n.mutationUnsupported()
}

func (n *node) sync(ctx context.Context, fromHandle bool) syscall.Errno {
	n.mount.mu.RLock()
	defer n.mount.mu.RUnlock()
	_, exists := n.nodePathLocked()
	if fromHandle {
		_, exists = n.handlePathLocked()
	}
	if !exists {
		return syscall.ESTALE
	}
	result, err := n.mount.writable.Sync(ctx)
	if err != nil {
		return errno(err)
	}
	if !result.LocalDurable || result.RootAccepted || (result.RemotePersisted && strings.TrimSpace(result.CandidateRoot) == "") {
		return syscall.EIO
	}
	return 0
}

func (n *node) mutationUnsupported() syscall.Errno {
	if n.mount.writable == nil {
		return syscall.EROFS
	}
	return syscall.ENOTSUP
}

func (n *node) setAttr(out *fuse.Attr, info filesystemservice.Info, mode uint32) {
	out.Mode = mode | n.permissionsForMode(mode)
	out.Size = info.Size
	out.Nlink = 1
	if info.IsDir() {
		out.Nlink = 2
	}
	out.Owner = fuse.Owner{Uid: n.uid, Gid: n.gid}
}

func modeForInfo(info filesystemservice.Info) (uint32, bool) {
	switch info.Kind {
	case unixfs.StagedKindDirectory:
		return fuse.S_IFDIR, true
	case unixfs.StagedKindFile:
		return fuse.S_IFREG, true
	default:
		return 0, false
	}
}

func validInfoPath(info filesystemservice.Info, expected string) bool {
	if info.Path != expected {
		return false
	}
	if expected == "" {
		return info.Name == ""
	}
	return info.Name == path.Base(expected)
}

func (n *node) permissionsForMode(mode uint32) uint32 {
	if n.mount.writable != nil {
		if mode == fuse.S_IFDIR {
			return 0o755
		}
		return 0o644
	}
	if mode == fuse.S_IFDIR {
		return 0o555
	}
	return 0o444
}

type fileHandle struct {
	node   *node
	handle filesystemmount.ReadHandle
	uid    uint32
	gid    uint32
	once   sync.Once
	err    error
}

func (f *fileHandle) Read(ctx context.Context, destination []byte, offset int64) (fuse.ReadResult, syscall.Errno) {
	if offset < 0 {
		return nil, syscall.EINVAL
	}
	body, err := f.handle.Read(ctx, uint64(offset), uint64(len(destination)))
	if err != nil {
		return nil, errno(err)
	}
	if len(body) > len(destination) {
		return nil, syscall.EIO
	}
	return fuse.ReadResultData(body), 0
}

func (f *fileHandle) Getattr(_ context.Context, out *fuse.AttrOut) syscall.Errno {
	info := f.handle.Info()
	mode, ok := modeForInfo(info)
	if !ok || mode != fuse.S_IFREG {
		return syscall.EIO
	}
	out.Mode = mode | 0o444
	out.Size = info.Size
	out.Nlink = 1
	out.Owner = fuse.Owner{Uid: f.uid, Gid: f.gid}
	out.SetTimeout(metadataTimeout)
	return 0
}

func (f *fileHandle) Write(context.Context, []byte, int64) (uint32, syscall.Errno) {
	return 0, syscall.EROFS
}

func (f *fileHandle) Setattr(context.Context, *fuse.SetAttrIn, *fuse.AttrOut) syscall.Errno {
	return syscall.EROFS
}

func (f *fileHandle) Flush(context.Context) syscall.Errno         { return 0 }
func (f *fileHandle) Fsync(context.Context, uint32) syscall.Errno { return 0 }

func (f *fileHandle) Allocate(context.Context, uint64, uint64, uint32) syscall.Errno {
	return syscall.EROFS
}

func (f *fileHandle) Release(context.Context) syscall.Errno {
	f.once.Do(func() { f.err = f.handle.Close() })
	return errno(f.err)
}

type writableFileHandle struct {
	node      *node
	readable  bool
	writable  bool
	lifecycle sync.RWMutex
	closed    bool
}

func (f *writableFileHandle) enter(allowed bool) (func(), syscall.Errno) {
	f.lifecycle.RLock()
	if f.closed || !allowed {
		f.lifecycle.RUnlock()
		return nil, syscall.EBADF
	}
	return f.lifecycle.RUnlock, 0
}

func (f *writableFileHandle) Read(ctx context.Context, destination []byte, offset int64) (fuse.ReadResult, syscall.Errno) {
	leave, failure := f.enter(f.readable)
	if failure != 0 {
		return nil, failure
	}
	defer leave()
	if offset < 0 {
		return nil, syscall.EINVAL
	}
	f.node.mount.mu.RLock()
	defer f.node.mount.mu.RUnlock()
	currentPath, exists := f.node.handlePathLocked()
	if !exists {
		return nil, syscall.ESTALE
	}
	handle, _, err := f.node.openReadHandleLocked(ctx, currentPath)
	if err != nil {
		return nil, errno(err)
	}
	body, readErr := handle.Read(ctx, uint64(offset), uint64(len(destination)))
	closeErr := handle.Close()
	if readErr != nil || closeErr != nil {
		return nil, errno(errors.Join(readErr, closeErr))
	}
	if len(body) > len(destination) {
		return nil, syscall.EIO
	}
	return fuse.ReadResultData(body), 0
}

func (f *writableFileHandle) Getattr(ctx context.Context, out *fuse.AttrOut) syscall.Errno {
	leave, failure := f.enter(true)
	if failure != 0 {
		return failure
	}
	defer leave()
	f.node.mount.mu.RLock()
	defer f.node.mount.mu.RUnlock()
	currentPath, exists := f.node.handlePathLocked()
	if !exists {
		return syscall.ESTALE
	}
	return f.node.getattrLocked(ctx, currentPath, out)
}

func (f *writableFileHandle) Write(ctx context.Context, data []byte, offset int64) (uint32, syscall.Errno) {
	leave, failure := f.enter(f.writable)
	if failure != 0 {
		return 0, failure
	}
	defer leave()
	if offset < 0 {
		return 0, syscall.EINVAL
	}
	end, ok := writeEnd(uint64(offset), len(data))
	if !ok {
		return 0, syscall.EFBIG
	}
	f.node.mount.mu.RLock()
	defer f.node.mount.mu.RUnlock()
	currentPath, exists := f.node.handlePathLocked()
	if !exists {
		return 0, syscall.ESTALE
	}
	info, err := f.node.mount.writable.WriteAt(ctx, currentPath, uint64(offset), data)
	if err != nil {
		return 0, errno(err)
	}
	if info.Kind != unixfs.StagedKindFile || !validInfoPath(info, currentPath) || (len(data) != 0 && info.Size < end) {
		return 0, syscall.EIO
	}
	return uint32(len(data)), 0
}

func (f *writableFileHandle) Setattr(ctx context.Context, input *fuse.SetAttrIn, out *fuse.AttrOut) syscall.Errno {
	leave, failure := f.enter(f.writable)
	if failure != 0 {
		return failure
	}
	defer leave()
	return f.node.truncateAttr(ctx, input, out, true)
}

func (f *writableFileHandle) Flush(context.Context) syscall.Errno {
	leave, failure := f.enter(true)
	if failure != 0 {
		return failure
	}
	leave()
	return 0
}

func (f *writableFileHandle) Fsync(ctx context.Context, _ uint32) syscall.Errno {
	leave, failure := f.enter(true)
	if failure != 0 {
		return failure
	}
	defer leave()
	return f.node.sync(ctx, true)
}

func (f *writableFileHandle) Allocate(context.Context, uint64, uint64, uint32) syscall.Errno {
	if f.node.mount.writable == nil {
		return syscall.EROFS
	}
	return syscall.ENOTSUP
}

func (f *writableFileHandle) Release(context.Context) syscall.Errno {
	f.lifecycle.Lock()
	if !f.closed {
		f.node.mount.mu.Lock()
		if f.node.openHandles != 0 {
			f.node.openHandles--
		}
		if f.node.openHandles == 0 && f.node.forgotten {
			delete(f.node.mount.nodes, f.node)
		}
		f.node.mount.mu.Unlock()
		f.closed = true
	}
	f.lifecycle.Unlock()
	return 0
}

func errno(err error) syscall.Errno {
	switch {
	case err == nil:
		return 0
	case errors.Is(err, context.Canceled):
		return syscall.EINTR
	case errors.Is(err, context.DeadlineExceeded):
		return syscall.ETIMEDOUT
	case errors.Is(err, unixfs.ErrNotFound):
		return syscall.ENOENT
	case errors.Is(err, unixfs.ErrNotDirectory):
		return syscall.ENOTDIR
	case errors.Is(err, unixfs.ErrNotFile):
		return syscall.EISDIR
	case errors.Is(err, filesystemmount.ErrAlreadyExists):
		return syscall.EEXIST
	case errors.Is(err, filesystemmount.ErrNotEmpty):
		return syscall.ENOTEMPTY
	case errors.Is(err, filesystemmount.ErrFileTooLarge):
		return syscall.EFBIG
	case errors.Is(err, filesystemservice.ErrClosed):
		return syscall.EBADF
	default:
		return syscall.EIO
	}
}

var (
	_ fs.NodeOnForgetter    = (*node)(nil)
	_ fs.NodeGetattrer      = (*node)(nil)
	_ fs.NodeLookuper       = (*node)(nil)
	_ fs.NodeReaddirer      = (*node)(nil)
	_ fs.NodeOpener         = (*node)(nil)
	_ fs.NodeSetattrer      = (*node)(nil)
	_ fs.NodeGetxattrer     = (*node)(nil)
	_ fs.NodeListxattrer    = (*node)(nil)
	_ fs.NodeMkdirer        = (*node)(nil)
	_ fs.NodeMknoder        = (*node)(nil)
	_ fs.NodeLinker         = (*node)(nil)
	_ fs.NodeSymlinker      = (*node)(nil)
	_ fs.NodeCreater        = (*node)(nil)
	_ fs.NodeUnlinker       = (*node)(nil)
	_ fs.NodeRmdirer        = (*node)(nil)
	_ fs.NodeRenamer        = (*node)(nil)
	_ fs.NodeSetxattrer     = (*node)(nil)
	_ fs.NodeRemovexattrer  = (*node)(nil)
	_ fs.NodeWriter         = (*node)(nil)
	_ fs.NodeFsyncer        = (*node)(nil)
	_ fs.NodeAllocater      = (*node)(nil)
	_ fs.NodeCopyFileRanger = (*node)(nil)
	_ fs.FileReader         = (*fileHandle)(nil)
	_ fs.FileGetattrer      = (*fileHandle)(nil)
	_ fs.FileReleaser       = (*fileHandle)(nil)
	_ fs.FileWriter         = (*fileHandle)(nil)
	_ fs.FileSetattrer      = (*fileHandle)(nil)
	_ fs.FileFlusher        = (*fileHandle)(nil)
	_ fs.FileFsyncer        = (*fileHandle)(nil)
	_ fs.FileAllocater      = (*fileHandle)(nil)
	_ fs.FileReader         = (*writableFileHandle)(nil)
	_ fs.FileGetattrer      = (*writableFileHandle)(nil)
	_ fs.FileReleaser       = (*writableFileHandle)(nil)
	_ fs.FileWriter         = (*writableFileHandle)(nil)
	_ fs.FileSetattrer      = (*writableFileHandle)(nil)
	_ fs.FileFlusher        = (*writableFileHandle)(nil)
	_ fs.FileFsyncer        = (*writableFileHandle)(nil)
	_ fs.FileAllocater      = (*writableFileHandle)(nil)
)

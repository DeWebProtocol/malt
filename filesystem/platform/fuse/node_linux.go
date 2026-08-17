//go:build linux

package fusefs

import (
	"context"
	"errors"
	"os"
	"path"
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

type node struct {
	fs.Inode
	filesystem filesystemmount.ReadOnlyFilesystem
	path       string
	uid        uint32
	gid        uint32
}

func newRoot(filesystem filesystemmount.ReadOnlyFilesystem) *node {
	return &node{filesystem: filesystem, uid: uint32(os.Getuid()), gid: uint32(os.Getgid())}
}

func (n *node) Getattr(ctx context.Context, _ fs.FileHandle, out *fuse.AttrOut) syscall.Errno {
	info, err := n.filesystem.Stat(ctx, n.path)
	if err != nil {
		return errno(err)
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
	segments, err := unixfsmodel.ParsePath(name)
	if err != nil || len(segments) != 1 {
		return nil, syscall.ENOENT
	}
	childPath := path.Join(n.path, name)
	info, err := n.filesystem.Stat(ctx, childPath)
	if err != nil {
		return nil, errno(err)
	}
	mode, ok := modeForInfo(info)
	if !ok {
		return nil, syscall.EIO
	}
	child := &node{filesystem: n.filesystem, path: childPath, uid: n.uid, gid: n.gid}
	stable := fs.StableAttr{Mode: mode}
	n.setAttr(&out.Attr, info, mode)
	out.SetEntryTimeout(metadataTimeout)
	out.SetAttrTimeout(metadataTimeout)
	return n.NewInode(ctx, child, stable), 0
}

func (n *node) Readdir(ctx context.Context) (fs.DirStream, syscall.Errno) {
	entries, err := n.filesystem.ReadDir(ctx, n.path)
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
	if flags&syscall.O_ACCMODE != syscall.O_RDONLY || flags&(syscall.O_APPEND|syscall.O_CREAT|syscall.O_TRUNC) != 0 {
		return nil, 0, syscall.EROFS
	}
	handle, err := n.filesystem.Open(ctx, n.path)
	if err != nil {
		return nil, 0, errno(err)
	}
	if handle == nil || handle.Info().Kind != unixfs.StagedKindFile {
		if handle != nil {
			_ = handle.Close()
		}
		return nil, 0, syscall.EIO
	}
	return &fileHandle{handle: handle, uid: n.uid, gid: n.gid}, fuse.FOPEN_KEEP_CACHE, 0
}

func (n *node) Setattr(context.Context, fs.FileHandle, *fuse.SetAttrIn, *fuse.AttrOut) syscall.Errno {
	return syscall.EROFS
}

func (n *node) Mkdir(context.Context, string, uint32, *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return nil, syscall.EROFS
}

func (n *node) Mknod(context.Context, string, uint32, uint32, *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return nil, syscall.EROFS
}

func (n *node) Link(context.Context, fs.InodeEmbedder, string, *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return nil, syscall.EROFS
}

func (n *node) Symlink(context.Context, string, string, *fuse.EntryOut) (*fs.Inode, syscall.Errno) {
	return nil, syscall.EROFS
}

func (n *node) Create(context.Context, string, uint32, uint32, *fuse.EntryOut) (*fs.Inode, fs.FileHandle, uint32, syscall.Errno) {
	return nil, nil, 0, syscall.EROFS
}

func (n *node) Unlink(context.Context, string) syscall.Errno { return syscall.EROFS }
func (n *node) Rmdir(context.Context, string) syscall.Errno  { return syscall.EROFS }

func (n *node) Rename(context.Context, string, fs.InodeEmbedder, string, uint32) syscall.Errno {
	return syscall.EROFS
}

func (n *node) Setxattr(context.Context, string, []byte, uint32) syscall.Errno {
	return syscall.EROFS
}

func (n *node) Removexattr(context.Context, string) syscall.Errno { return syscall.EROFS }

func (n *node) setAttr(out *fuse.Attr, info filesystemservice.Info, mode uint32) {
	out.Mode = mode | permissionsForMode(mode)
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

func permissionsForMode(mode uint32) uint32 {
	if mode == fuse.S_IFDIR {
		return 0o555
	}
	return 0o444
}

type fileHandle struct {
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
	out.Mode = mode | permissionsForMode(mode)
	out.Size = info.Size
	out.Nlink = 1
	out.Owner = fuse.Owner{Uid: f.uid, Gid: f.gid}
	out.SetTimeout(metadataTimeout)
	return 0
}

func (f *fileHandle) Release(context.Context) syscall.Errno {
	f.once.Do(func() { f.err = f.handle.Close() })
	return errno(f.err)
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
	case errors.Is(err, filesystemservice.ErrClosed):
		return syscall.EBADF
	default:
		return syscall.EIO
	}
}

var (
	_ fs.NodeGetattrer     = (*node)(nil)
	_ fs.NodeLookuper      = (*node)(nil)
	_ fs.NodeReaddirer     = (*node)(nil)
	_ fs.NodeOpener        = (*node)(nil)
	_ fs.NodeSetattrer     = (*node)(nil)
	_ fs.NodeMkdirer       = (*node)(nil)
	_ fs.NodeMknoder       = (*node)(nil)
	_ fs.NodeLinker        = (*node)(nil)
	_ fs.NodeSymlinker     = (*node)(nil)
	_ fs.NodeCreater       = (*node)(nil)
	_ fs.NodeUnlinker      = (*node)(nil)
	_ fs.NodeRmdirer       = (*node)(nil)
	_ fs.NodeRenamer       = (*node)(nil)
	_ fs.NodeSetxattrer    = (*node)(nil)
	_ fs.NodeRemovexattrer = (*node)(nil)
	_ fs.FileReader        = (*fileHandle)(nil)
	_ fs.FileGetattrer     = (*fileHandle)(nil)
	_ fs.FileReleaser      = (*fileHandle)(nil)
)

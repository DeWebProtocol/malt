//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package local

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
	"golang.org/x/sys/unix"
)

// platformStore keeps the blocks directory open for the lifetime of the CAS.
// Descriptor-relative operations therefore stay inside the directory selected
// by Open even if an attacker later renames or replaces its pathname.
type platformStore struct {
	root          *os.File
	blocks        *os.File
	syncDirectory func(*os.File) error
	closeFile     func(*os.File) error
}

func (s *platformStore) close() error {
	if s == nil {
		return nil
	}
	var closeErrors []error
	if s.blocks != nil {
		if err := s.closeHandle(s.blocks); err != nil {
			closeErrors = append(closeErrors, err)
		}
		// os.File.Close terminally invalidates the File even when it reports an
		// error. Retaining it would make every retry operate on os.ErrClosed.
		s.blocks = nil
	}
	if s.root != nil {
		if err := s.closeHandle(s.root); err != nil {
			closeErrors = append(closeErrors, err)
		}
		s.root = nil
	}
	return errors.Join(closeErrors...)
}

func openPlatformStore(directory string) (blockStore, error) {
	return openPlatformStoreWithSync(directory, func(directory *os.File) error { return directory.Sync() })
}

func openPlatformStoreWithSync(directory string, syncDirectory func(*os.File) error) (blockStore, error) {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create local CAS directory: %w", err)
	}
	rootFD, err := unix.Open(directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open local CAS boundary without following links: %w", err)
	}
	root := os.NewFile(uintptr(rootFD), directory)
	if root == nil {
		_ = unix.Close(rootFD)
		return nil, fmt.Errorf("open local CAS boundary handle")
	}
	keepRoot := false
	defer func() {
		if !keepRoot {
			_ = root.Close()
		}
	}()
	if err := requireUnixDirectory(rootFD, "local CAS boundary"); err != nil {
		return nil, err
	}
	if err := unix.Fchmod(rootFD, 0o700); err != nil {
		return nil, fmt.Errorf("protect local CAS boundary: %w", err)
	}

	if err := unix.Mkdirat(rootFD, "blocks", 0o700); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return nil, fmt.Errorf("create local CAS blocks directory: %w", err)
		}
	}
	blocksFD, err := unix.Openat(rootFD, "blocks", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fmt.Errorf("open local CAS blocks directory without following links: %w", err)
	}
	blocks := os.NewFile(uintptr(blocksFD), directory+string(os.PathSeparator)+"blocks")
	if blocks == nil {
		_ = unix.Close(blocksFD)
		return nil, fmt.Errorf("open local CAS blocks directory handle")
	}
	if err := requireUnixDirectory(blocksFD, "local CAS blocks directory"); err != nil {
		_ = blocks.Close()
		return nil, err
	}
	if err := unix.Fchmod(blocksFD, 0o700); err != nil {
		_ = blocks.Close()
		return nil, fmt.Errorf("protect local CAS blocks directory: %w", err)
	}
	// Persist the blocks directory's own repaired mode before its parent entry.
	if err := syncDirectory(blocks); err != nil {
		_ = blocks.Close()
		return nil, fmt.Errorf("sync local CAS blocks metadata: %w", err)
	}
	// Sync on every Open, not only the mkdir attempt. If an earlier Open created
	// blocks but its parent sync failed, a later Open must complete durability.
	if err := syncDirectory(root); err != nil {
		_ = blocks.Close()
		return nil, fmt.Errorf("sync local CAS boundary: %w", err)
	}
	// A directory fsync does not confirm the directory entry in its parent.
	// Confirm the complete ancestor chain from the selected boundary outward on
	// every Open, so a retry also completes a prior MkdirAll whose parent sync
	// failed after the directories became visible.
	if err := syncUnixParentChain(root, syncDirectory); err != nil {
		_ = blocks.Close()
		return nil, err
	}
	keepRoot = true
	return &platformStore{
		root: root, blocks: blocks,
		syncDirectory: syncDirectory,
		closeFile:     func(file *os.File) error { return file.Close() },
	}, nil
}

func syncUnixParentChain(root *os.File, syncDirectory func(*os.File) error) error {
	if root == nil {
		return fmt.Errorf("sync local CAS ancestor chain from nil boundary")
	}
	current := root
	currentOwned := false
	var currentStat unix.Stat_t
	if err := unix.Fstat(int(current.Fd()), &currentStat); err != nil {
		return fmt.Errorf("inspect local CAS boundary before ancestor sync: %w", err)
	}
	for {
		fd, err := unix.Openat(int(current.Fd()), "..", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err != nil {
			if currentOwned {
				_ = current.Close()
			}
			return fmt.Errorf("open parent of local CAS ancestor %q for sync: %w", current.Name(), err)
		}
		handle := os.NewFile(uintptr(fd), filepath.Join(current.Name(), ".."))
		if handle == nil {
			_ = unix.Close(fd)
			if currentOwned {
				_ = current.Close()
			}
			return fmt.Errorf("open parent of local CAS ancestor %q handle for sync", current.Name())
		}
		var parentStat unix.Stat_t
		statErr := unix.Fstat(fd, &parentStat)
		syncErr := syncDirectory(handle)
		if err := errors.Join(statErr, syncErr); err != nil {
			_ = handle.Close()
			if currentOwned {
				_ = current.Close()
			}
			return fmt.Errorf("sync local CAS ancestor %q: %w", handle.Name(), err)
		}
		atFilesystemRoot := currentStat.Dev == parentStat.Dev && currentStat.Ino == parentStat.Ino
		if currentOwned {
			if err := current.Close(); err != nil {
				_ = handle.Close()
				return fmt.Errorf("close local CAS ancestor %q after sync: %w", current.Name(), err)
			}
		}
		if atFilesystemRoot {
			if err := handle.Close(); err != nil {
				return fmt.Errorf("close filesystem root after local CAS sync: %w", err)
			}
			return nil
		}
		current = handle
		currentOwned = true
		currentStat = parentStat
	}
}

func (s *platformStore) readBlock(ctx context.Context, shard, name string, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	shardDirectory, err := s.openShard(shard, false)
	if err != nil {
		return nil, err
	}
	defer shardDirectory.Close()

	// O_NONBLOCK prevents an untrusted FIFO from blocking before fstat can reject
	// it. It has no effect on ordinary regular-file reads.
	shardFD := int(shardDirectory.Fd())
	fd, err := unix.Openat(shardFD, name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil, fmt.Errorf("%w: local CAS block is absent", transportcap.ErrNotFound)
		}
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return nil, fmt.Errorf("%w: local CAS block path is unsafe", transportcap.ErrCorruptedBlock)
		}
		if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.ENXIO) || errors.Is(err, unix.ENODEV) {
			var stat unix.Stat_t
			if inspectErr := unix.Fstatat(shardFD, name, &stat, unix.AT_SYMLINK_NOFOLLOW); inspectErr == nil && unsafeUnixBlockMetadata(&stat) {
				return nil, fmt.Errorf("%w: local CAS block exists but is unreadable or is not a regular file", transportcap.ErrCorruptedBlock)
			} else if errors.Is(inspectErr, unix.ENOENT) {
				return nil, fmt.Errorf("%w: local CAS block is absent", transportcap.ErrNotFound)
			}
		}
		return nil, fmt.Errorf("open local CAS block without following links: %w", err)
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open local CAS block handle")
	}
	defer file.Close()

	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, fmt.Errorf("inspect local CAS block: %w", err)
	}
	// An atomic replacement by another CAS instance can drop the link count of
	// this already-open immutable inode to zero. That handle is still contained
	// and safe to verify; only additional hard links create an external alias.
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink > 1 {
		return nil, fmt.Errorf("%w: local CAS block must be a regular file without additional hard links", transportcap.ErrCorruptedBlock)
	}
	if stat.Mode&0o777 != 0o600 || stat.Uid != uint32(os.Geteuid()) {
		return nil, fmt.Errorf("%w: local CAS block is not owner-private", transportcap.ErrCorruptedBlock)
	}
	if stat.Size < 0 || stat.Size > maxBytes {
		return nil, fmt.Errorf("%w: local CAS block exceeds %d bytes", transportcap.ErrCorruptedBlock, maxBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read local CAS block: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("%w: local CAS block exceeds %d bytes", transportcap.ErrCorruptedBlock, maxBytes)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return data, nil
}

func unsafeUnixBlockMetadata(stat *unix.Stat_t) bool {
	return stat == nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink > 1 ||
		stat.Mode&0o777 != 0o600 || stat.Uid != uint32(os.Geteuid())
}

func (s *platformStore) writeBlock(ctx context.Context, shard, name string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	shardDirectory, err := s.openShard(shard, true)
	if err != nil {
		return err
	}
	defer shardDirectory.Close()

	temporaryName, err := randomTemporaryName()
	if err != nil {
		return err
	}
	shardFD := int(shardDirectory.Fd())
	fd, err := unix.Openat(shardFD, temporaryName, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("create local CAS temporary block: %w", err)
	}
	temporary := os.NewFile(uintptr(fd), temporaryName)
	if temporary == nil {
		_ = unix.Close(fd)
		_ = unix.Unlinkat(shardFD, temporaryName, 0)
		return fmt.Errorf("open local CAS temporary block handle")
	}
	installed := false
	defer func() {
		_ = temporary.Close()
		if !installed {
			_ = unix.Unlinkat(shardFD, temporaryName, 0)
		}
	}()
	if err := unix.Fchmod(fd, 0o600); err != nil {
		return fmt.Errorf("protect local CAS temporary block: %w", err)
	}
	if err := writeAllWithContext(ctx, temporary, data); err != nil {
		return fmt.Errorf("write local CAS temporary block: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync local CAS temporary block: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close local CAS temporary block: %w", err)
	}
	if err := unix.Renameat(shardFD, temporaryName, shardFD, name); err != nil {
		return fmt.Errorf("install local CAS block: %w", err)
	}
	installed = true
	if err := s.sync(shardDirectory); err != nil {
		return fmt.Errorf("sync local CAS shard: %w", err)
	}
	return nil
}

func (s *platformStore) openShard(shard string, create bool) (*os.File, error) {
	blocksFD := int(s.blocks.Fd())
	created := false
	if create {
		if err := unix.Mkdirat(blocksFD, shard, 0o700); err != nil {
			if !errors.Is(err, unix.EEXIST) {
				return nil, fmt.Errorf("create local CAS shard: %w", err)
			}
		} else {
			created = true
		}
	}
	fd, err := unix.Openat(blocksFD, shard, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) && !create {
			return nil, fmt.Errorf("%w: local CAS shard is absent", transportcap.ErrNotFound)
		}
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			return nil, fmt.Errorf("%w: local CAS shard path is unsafe", transportcap.ErrCorruptedBlock)
		}
		if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM) {
			var stat unix.Stat_t
			if inspectErr := unix.Fstatat(blocksFD, shard, &stat, unix.AT_SYMLINK_NOFOLLOW); inspectErr == nil && unsafeUnixShardMetadata(&stat) {
				return nil, fmt.Errorf("%w: local CAS shard exists but is not safely searchable; repair its owner-private directory metadata offline", transportcap.ErrCorruptedBlock)
			} else if errors.Is(inspectErr, unix.ENOENT) && !create {
				return nil, fmt.Errorf("%w: local CAS shard is absent", transportcap.ErrNotFound)
			}
		}
		return nil, fmt.Errorf("open local CAS shard without following links: %w", err)
	}
	directory := os.NewFile(uintptr(fd), shard)
	if directory == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("open local CAS shard handle")
	}
	if err := requireUnixDirectory(fd, "local CAS shard"); err != nil {
		_ = directory.Close()
		return nil, fmt.Errorf("%w: %v", transportcap.ErrCorruptedBlock, err)
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = directory.Close()
		return nil, fmt.Errorf("inspect local CAS shard: %w", err)
	}
	// An existing shard that is not owner-readable and owner-searchable must be
	// repaired offline even when this process has elevated privileges. Otherwise
	// root could silently turn a deliberately inaccessible directory containing
	// unrelated immutable blocks back into an online write target.
	if !created && stat.Mode&0o500 != 0o500 {
		_ = directory.Close()
		return nil, fmt.Errorf("%w: local CAS shard is not safely searchable; repair its owner-private directory metadata offline", transportcap.ErrCorruptedBlock)
	}
	if create {
		if err := unix.Fchmod(fd, 0o700); err != nil {
			_ = directory.Close()
			return nil, fmt.Errorf("protect local CAS shard: %w", err)
		}
	} else {
		if stat.Mode&0o777 != 0o700 {
			_ = directory.Close()
			return nil, fmt.Errorf("%w: local CAS shard is not owner-private", transportcap.ErrCorruptedBlock)
		}
	}
	if create {
		// Repeat the parent sync even when the shard already exists so a retry can
		// complete a prior mkdir whose durability confirmation failed.
		if err := s.sync(s.blocks); err != nil {
			_ = directory.Close()
			return nil, fmt.Errorf("sync local CAS blocks directory: %w", err)
		}
	}
	return directory, nil
}

func unsafeUnixShardMetadata(stat *unix.Stat_t) bool {
	return stat == nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR ||
		stat.Mode&0o777 != 0o700 || stat.Uid != uint32(os.Geteuid())
}

func (s *platformStore) ensureDurable(ctx context.Context, shard string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	directory, err := s.openShard(shard, false)
	if err != nil {
		return err
	}
	defer directory.Close()
	chain := []struct {
		description string
		directory   *os.File
	}{
		{description: "local CAS shard", directory: directory},
		{description: "local CAS blocks directory", directory: s.blocks},
		{description: "local CAS boundary", directory: s.root},
	}
	for _, entry := range chain {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.sync(entry.directory); err != nil {
			return fmt.Errorf("sync %s: %w", entry.description, err)
		}
	}
	return nil
}

func (s *platformStore) sync(directory *os.File) error {
	if s.syncDirectory == nil {
		return directory.Sync()
	}
	return s.syncDirectory(directory)
}

func (s *platformStore) closeHandle(file *os.File) error {
	if s.closeFile == nil {
		return file.Close()
	}
	return s.closeFile(file)
}

func requireUnixDirectory(fd int, description string) error {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("inspect %s: %w", description, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fmt.Errorf("%s is not a directory", description)
	}
	if stat.Uid != uint32(os.Geteuid()) {
		return fmt.Errorf("%s is not owned by the current user", description)
	}
	return nil
}

func randomTemporaryName() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate local CAS temporary name: %w", err)
	}
	return ".malt-cas-" + hex.EncodeToString(value[:]) + ".block", nil
}

func writeAllWithContext(ctx context.Context, file *os.File, data []byte) error {
	for len(data) != 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		written, err := file.Write(data)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		data = data[written:]
	}
	return nil
}

//go:build windows

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

	"github.com/dewebprotocol/malt-client/internal/securefile"
	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
	"golang.org/x/sys/windows"
)

var errUnsafeWindowsDirectory = errors.New("path is not a non-reparse directory")

// platformStore retains non-delete-sharing directory handles. Windows will
// therefore reject attempts to rename or replace the selected boundary while
// this CAS is active. Every subsequently opened component is inspected through
// a no-reparse-point handle before it is used.
type platformStore struct {
	root          *os.File
	blocks        *os.File
	blocksPath    string
	closeFile     func(*os.File) error
	openDirectory func(string) (*os.File, error)
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
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return nil, fmt.Errorf("create local CAS directory: %w", err)
	}
	root, err := openWindowsDirectory(directory)
	if err != nil {
		return nil, fmt.Errorf("open local CAS boundary without following reparse points: %w", err)
	}
	owned, err := securefile.IsCurrentOwnerHandle(windows.Handle(root.Fd()))
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("inspect local CAS boundary owner: %w", err)
	}
	if !owned {
		_ = root.Close()
		return nil, fmt.Errorf("local CAS boundary is not owned by the current user")
	}
	if err := protectDirectory(directory); err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("protect local CAS boundary: %w", err)
	}
	blocksPath := filepath.Join(directory, "blocks")
	if err := os.Mkdir(blocksPath, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		_ = root.Close()
		return nil, fmt.Errorf("create local CAS blocks directory: %w", err)
	}
	blocks, err := openWindowsDirectory(blocksPath)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("open local CAS blocks directory without following reparse points: %w", err)
	}
	owned, err = securefile.IsCurrentOwnerHandle(windows.Handle(blocks.Fd()))
	if err != nil {
		_ = blocks.Close()
		_ = root.Close()
		return nil, fmt.Errorf("inspect local CAS blocks directory owner: %w", err)
	}
	if !owned {
		_ = blocks.Close()
		_ = root.Close()
		return nil, fmt.Errorf("local CAS blocks directory is not owned by the current user")
	}
	if err := protectDirectory(blocksPath); err != nil {
		_ = blocks.Close()
		_ = root.Close()
		return nil, fmt.Errorf("protect local CAS blocks directory: %w", err)
	}
	return &platformStore{
		root: root, blocks: blocks, blocksPath: blocksPath,
		closeFile: func(file *os.File) error { return file.Close() },
	}, nil
}

func (s *platformStore) readBlock(ctx context.Context, shard, name string, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	shardDirectory, shardPath, err := s.openShard(shard, false)
	if err != nil {
		return nil, err
	}
	defer shardDirectory.Close()

	path := filepath.Join(shardPath, name)
	// READ_CONTROL is required by GetSecurityInfo. If a damaged DACL denies it,
	// fall back to a zero-access pinned handle only to confirm that an unsafe
	// object exists; no bytes are exposed through that fallback.
	metadataHandle, metadata, err := openWindowsFile(path, windows.READ_CONTROL, windows.OPEN_EXISTING, true)
	if err != nil {
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return nil, fmt.Errorf("%w: local CAS block is absent", transportcap.ErrNotFound)
		}
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			fallback, _, inspectErr := openWindowsFile(path, 0, windows.OPEN_EXISTING, true)
			if inspectErr == nil {
				_ = windows.CloseHandle(fallback)
				return nil, fmt.Errorf("%w: local CAS block security access is denied", transportcap.ErrCorruptedBlock)
			}
			if errors.Is(inspectErr, windows.ERROR_FILE_NOT_FOUND) || errors.Is(inspectErr, windows.ERROR_PATH_NOT_FOUND) {
				return nil, fmt.Errorf("%w: local CAS block is absent", transportcap.ErrNotFound)
			}
		}
		return nil, fmt.Errorf("open local CAS block without following reparse points: %w", err)
	}
	metadataFile := os.NewFile(uintptr(metadataHandle), path+":metadata")
	if metadataFile == nil {
		_ = windows.CloseHandle(metadataHandle)
		return nil, fmt.Errorf("open local CAS block metadata handle")
	}
	defer metadataFile.Close()
	if err := validateWindowsBlockHandle(metadataHandle, metadata); err != nil {
		return nil, err
	}

	handle, information, err := openWindowsFile(path, windows.GENERIC_READ|windows.READ_CONTROL, windows.OPEN_EXISTING, true)
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil, fmt.Errorf("%w: local CAS block became unreadable", transportcap.ErrCorruptedBlock)
		}
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND) {
			return nil, fmt.Errorf("%w: local CAS block is absent", transportcap.ErrNotFound)
		}
		return nil, fmt.Errorf("open local CAS block data handle: %w", err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("open local CAS block handle")
	}
	defer file.Close()
	// A concurrent atomic replacement may unlink this already-open immutable
	// handle. Zero links remains contained; more than one exposes a hard-link
	// alias outside the selected CAS boundary.
	if err := validateWindowsBlockHandle(handle, information); err != nil {
		return nil, err
	}
	size := int64(uint64(information.FileSizeHigh)<<32 | uint64(information.FileSizeLow))
	if size < 0 || size > maxBytes {
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

func (s *platformStore) writeBlock(ctx context.Context, shard, name string, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	shardDirectory, shardPath, err := s.openShard(shard, true)
	if err != nil {
		return err
	}
	defer shardDirectory.Close()

	temporaryName, err := randomTemporaryName()
	if err != nil {
		return err
	}
	temporaryPath := filepath.Join(shardPath, temporaryName)
	handle, _, err := openWindowsFile(temporaryPath, windows.GENERIC_READ|windows.GENERIC_WRITE, windows.CREATE_NEW, false)
	if err != nil {
		return fmt.Errorf("create local CAS temporary block: %w", err)
	}
	temporary := os.NewFile(uintptr(handle), temporaryPath)
	if temporary == nil {
		_ = windows.CloseHandle(handle)
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("open local CAS temporary block handle")
	}
	installed := false
	defer func() {
		_ = temporary.Close()
		if !installed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := securefile.Secure(temporaryPath); err != nil {
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
	if err := moveWindowsFile(temporaryPath, filepath.Join(shardPath, name)); err != nil {
		return fmt.Errorf("install local CAS block: %w", err)
	}
	installed = true
	return nil
}

func (s *platformStore) ensureDurable(ctx context.Context, _ string) error {
	// Temporary contents are flushed before installation and MoveFileEx uses
	// MOVEFILE_WRITE_THROUGH. Windows has no portable directory-fsync contract
	// equivalent to the Unix implementation below this interface.
	return ctx.Err()
}

func (s *platformStore) closeHandle(file *os.File) error {
	if s.closeFile == nil {
		return file.Close()
	}
	return s.closeFile(file)
}

func validateWindowsBlockHandle(handle windows.Handle, information windows.ByHandleFileInformation) error {
	if information.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 || information.NumberOfLinks > 1 {
		return fmt.Errorf("%w: local CAS block must be a regular file without additional hard links", transportcap.ErrCorruptedBlock)
	}
	private, err := securefile.IsSecureHandle(handle)
	if err != nil {
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return fmt.Errorf("%w: local CAS block security cannot be verified", transportcap.ErrCorruptedBlock)
		}
		return fmt.Errorf("inspect local CAS block security: %w", err)
	}
	if !private {
		return fmt.Errorf("%w: local CAS block is not owner-private", transportcap.ErrCorruptedBlock)
	}
	return nil
}

func (s *platformStore) openShard(shard string, create bool) (*os.File, string, error) {
	path := filepath.Join(s.blocksPath, shard)
	if create {
		if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return nil, "", fmt.Errorf("create local CAS shard: %w", err)
		}
	}
	directory, err := s.openShardDirectory(path)
	if err != nil {
		if !create && (errors.Is(err, windows.ERROR_FILE_NOT_FOUND) || errors.Is(err, windows.ERROR_PATH_NOT_FOUND)) {
			return nil, "", fmt.Errorf("%w: local CAS shard is absent", transportcap.ErrNotFound)
		}
		if errors.Is(err, windows.ERROR_ACCESS_DENIED) || errors.Is(err, errUnsafeWindowsDirectory) {
			return nil, "", fmt.Errorf("%w: local CAS shard path is unsafe: %v", transportcap.ErrCorruptedBlock, err)
		}
		return nil, "", fmt.Errorf("open local CAS shard without following reparse points: %w", err)
	}
	handle := windows.Handle(directory.Fd())
	owned, err := securefile.IsCurrentOwnerHandle(handle)
	if err != nil {
		_ = directory.Close()
		return nil, "", fmt.Errorf("inspect local CAS shard owner: %w", err)
	}
	if !owned {
		_ = directory.Close()
		return nil, "", fmt.Errorf("%w: local CAS shard is not owned by the current user", transportcap.ErrCorruptedBlock)
	}
	if create {
		if err := protectDirectory(path); err != nil {
			_ = directory.Close()
			return nil, "", fmt.Errorf("protect local CAS shard: %w", err)
		}
	} else {
		private, err := securefile.IsSecureDirectoryHandle(handle)
		if err != nil {
			_ = directory.Close()
			return nil, "", fmt.Errorf("inspect local CAS shard security: %w", err)
		}
		if !private {
			_ = directory.Close()
			return nil, "", fmt.Errorf("%w: local CAS shard is not owner-private", transportcap.ErrCorruptedBlock)
		}
	}
	return directory, path, nil
}

func (s *platformStore) openShardDirectory(path string) (*os.File, error) {
	if s.openDirectory != nil {
		return s.openDirectory(path)
	}
	return openWindowsDirectory(path)
}

func openWindowsDirectory(path string) (*os.File, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(
		pointer,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return nil, err
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, err
	}
	if information.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY == 0 || information.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return nil, errUnsafeWindowsDirectory
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		return nil, fmt.Errorf("open directory handle")
	}
	return file, nil
}

func openWindowsFile(path string, access, disposition uint32, allowDelete bool) (windows.Handle, windows.ByHandleFileInformation, error) {
	pointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, windows.ByHandleFileInformation{}, err
	}
	shareMode := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE)
	if allowDelete {
		// Immutable target readers remain valid after another CAS instance
		// atomically replaces the directory entry. Temporary writers omit delete
		// sharing so their path cannot be swapped before ACL hardening/install.
		shareMode |= windows.FILE_SHARE_DELETE
	}
	handle, err := windows.CreateFile(
		pointer,
		access,
		shareMode,
		nil,
		disposition,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		return windows.InvalidHandle, windows.ByHandleFileInformation{}, err
	}
	var information windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &information); err != nil {
		_ = windows.CloseHandle(handle)
		return windows.InvalidHandle, windows.ByHandleFileInformation{}, err
	}
	return handle, information, nil
}

func moveWindowsFile(source, target string) error {
	from, err := windows.UTF16PtrFromString(source)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
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

// Package backup owns encrypted local snapshot creation and verified restore
// orchestration. It deliberately relies on MALT root/ProofList/CID validation
// for remote ciphertext integrity instead of adding an AEAD authentication
// tag to the archive format.
package backup

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/chacha20"
)

const (
	archiveVersion    = byte(1)
	archiveHeaderSize = 8 + 1 + 4 + chacha20.NonceSizeX
)

var archiveMagic = [8]byte{'M', 'A', 'L', 'T', 'B', 'K', 'P', '1'}

type ArchiveInfo struct {
	Epoch uint32 `json:"key_epoch"`
	Bytes int64  `json:"encrypted_bytes"`
}

// CreateArchive writes a gzip-compressed tar stream encrypted with XChaCha20.
// The cleartext header and ciphertext are later committed by the authenticated
// UnixFS target CID. Nonce uniqueness is provided by crypto/rand.
func CreateArchive(ctx context.Context, source, target string, epoch uint32, key [32]byte) (ArchiveInfo, error) {
	return createArchive(ctx, source, target, epoch, key, false)
}

// CreateBindingArchive stores the contents of one bound directory at the
// archive root. Sync can therefore restore it directly into the configured
// local binding without introducing another basename directory.
func CreateBindingArchive(ctx context.Context, source, target string, epoch uint32, key [32]byte) (ArchiveInfo, error) {
	return createArchive(ctx, source, target, epoch, key, true)
}

func createArchive(ctx context.Context, source, target string, epoch uint32, key [32]byte, contentsOnly bool) (ArchiveInfo, error) {
	if epoch == 0 {
		return ArchiveInfo{}, fmt.Errorf("backup key epoch must be positive")
	}
	if strings.TrimSpace(source) == "" || strings.TrimSpace(target) == "" {
		return ArchiveInfo{}, fmt.Errorf("backup source and archive target are required")
	}
	absSource, err := filepath.Abs(strings.TrimSpace(source))
	if err != nil {
		return ArchiveInfo{}, fmt.Errorf("resolve backup source: %w", err)
	}
	absTarget, err := filepath.Abs(strings.TrimSpace(target))
	if err != nil {
		return ArchiveInfo{}, fmt.Errorf("resolve backup archive target: %w", err)
	}
	rootInfo, err := os.Lstat(absSource)
	if err != nil {
		return ArchiveInfo{}, fmt.Errorf("stat backup source: %w", err)
	}
	if !rootInfo.IsDir() && !rootInfo.Mode().IsRegular() && rootInfo.Mode()&os.ModeSymlink == 0 {
		return ArchiveInfo{}, fmt.Errorf("backup source must be a directory, regular file, or symlink")
	}
	if contentsOnly && !rootInfo.IsDir() {
		return ArchiveInfo{}, fmt.Errorf("backup binding source must be a directory")
	}
	inside, err := pathWithin(absSource, absTarget)
	if err != nil {
		return ArchiveInfo{}, fmt.Errorf("compare backup source and archive target: %w", err)
	}
	if rootInfo.IsDir() && !inside {
		inside, err = resolvedPathWithin(absSource, absTarget)
		if err != nil {
			return ArchiveInfo{}, fmt.Errorf("resolve backup source and archive target: %w", err)
		}
	}
	if (rootInfo.IsDir() && inside) || (!rootInfo.IsDir() && absSource == absTarget) {
		return ArchiveInfo{}, fmt.Errorf("encrypted backup archive target must be outside the backup source")
	}
	target = absTarget
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return ArchiveInfo{}, fmt.Errorf("create backup staging directory: %w", err)
	}
	out, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return ArchiveInfo{}, fmt.Errorf("create encrypted backup archive: %w", err)
	}
	ok := false
	defer func() {
		_ = out.Close()
		if !ok {
			_ = os.Remove(target)
		}
	}()

	nonce := make([]byte, chacha20.NonceSizeX)
	if _, err := rand.Read(nonce); err != nil {
		return ArchiveInfo{}, fmt.Errorf("generate backup nonce: %w", err)
	}
	header := make([]byte, archiveHeaderSize)
	copy(header, archiveMagic[:])
	header[8] = archiveVersion
	binary.BigEndian.PutUint32(header[9:13], epoch)
	copy(header[13:], nonce)
	if _, err := out.Write(header); err != nil {
		return ArchiveInfo{}, fmt.Errorf("write backup header: %w", err)
	}
	stream, err := chacha20.NewUnauthenticatedCipher(key[:], nonce)
	if err != nil {
		return ArchiveInfo{}, fmt.Errorf("initialize backup encryption: %w", err)
	}
	encrypted := &cipher.StreamWriter{S: stream, W: out}
	compressed := gzip.NewWriter(encrypted)
	tarWriter := tar.NewWriter(compressed)
	var archiveErr error
	if contentsOnly {
		archiveErr = writeTarContents(ctx, tarWriter, absSource)
	} else {
		archiveErr = writeTarTree(ctx, tarWriter, absSource)
	}
	if archiveErr != nil {
		_ = tarWriter.Close()
		_ = compressed.Close()
		return ArchiveInfo{}, archiveErr
	}
	if err := tarWriter.Close(); err != nil {
		return ArchiveInfo{}, fmt.Errorf("finalize backup tar stream: %w", err)
	}
	if err := compressed.Close(); err != nil {
		return ArchiveInfo{}, fmt.Errorf("finalize backup compression: %w", err)
	}
	if err := out.Sync(); err != nil {
		return ArchiveInfo{}, fmt.Errorf("sync encrypted backup archive: %w", err)
	}
	if err := out.Close(); err != nil {
		return ArchiveInfo{}, fmt.Errorf("close encrypted backup archive: %w", err)
	}
	stat, err := os.Stat(target)
	if err != nil {
		return ArchiveInfo{}, err
	}
	ok = true
	return ArchiveInfo{Epoch: epoch, Bytes: stat.Size()}, nil
}

func writeTarTree(ctx context.Context, writer *tar.Writer, source string) error {
	parent := filepath.Dir(source)
	return writeTarTreeFrom(ctx, writer, source, parent)
}

func writeTarContents(ctx context.Context, writer *tar.Writer, source string) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		return fmt.Errorf("read backup binding source: %w", err)
	}
	for _, entry := range entries {
		if err := writeTarTreeFrom(ctx, writer, filepath.Join(source, entry.Name()), source); err != nil {
			return err
		}
	}
	return nil
}

func writeTarTreeFrom(ctx context.Context, writer *tar.Writer, source, parent string) error {
	return filepath.WalkDir(source, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return fmt.Errorf("unsupported filesystem object in backup: %s", current)
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(current)
			if err != nil {
				return fmt.Errorf("read backup symlink %s: %w", current, err)
			}
			if err := validateArchiveSymlink(parent, current, link); err != nil {
				return fmt.Errorf("unsafe backup symlink %s: %w", current, err)
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return fmt.Errorf("create archive header for %s: %w", current, err)
		}
		relative, err := filepath.Rel(parent, current)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(relative)
		header.Linkname = filepath.ToSlash(header.Linkname)
		if info.IsDir() && !strings.HasSuffix(header.Name, "/") {
			header.Name += "/"
		}
		if err := writer.WriteHeader(header); err != nil {
			return fmt.Errorf("write archive header for %s: %w", current, err)
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		file, err := os.Open(current)
		if err != nil {
			return fmt.Errorf("open backup file %s: %w", current, err)
		}
		_, copyErr := io.Copy(writer, &contextReader{ctx: ctx, reader: file})
		closeErr := file.Close()
		if copyErr != nil {
			return fmt.Errorf("archive backup file %s: %w", current, copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close backup file %s: %w", current, closeErr)
		}
		return nil
	})
}

// archiveEpoch reads only the CID-bound cleartext framing needed to select a
// local key. It does not establish integrity on its own.
func archiveEpoch(reader io.Reader) (uint32, []byte, error) {
	header := make([]byte, archiveHeaderSize)
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, nil, fmt.Errorf("read backup archive header: %w", err)
	}
	if string(header[:8]) != string(archiveMagic[:]) || header[8] != archiveVersion {
		return 0, nil, fmt.Errorf("unsupported encrypted backup archive")
	}
	epoch := binary.BigEndian.Uint32(header[9:13])
	if epoch == 0 {
		return 0, nil, fmt.Errorf("backup archive has invalid key epoch")
	}
	return epoch, append([]byte(nil), header[13:]...), nil
}

// restoreArchive is deliberately package-private: the public restore path must
// first authenticate the fixed path and every ciphertext range against the
// caller-selected trusted root.
func restoreArchive(ctx context.Context, archivePath, destination string, keyForEpoch func(uint32) ([32]byte, error), overwrite bool) error {
	if strings.TrimSpace(archivePath) == "" || strings.TrimSpace(destination) == "" || keyForEpoch == nil {
		return fmt.Errorf("backup archive, restore destination, and key resolver are required")
	}
	in, err := os.Open(archivePath)
	if err != nil {
		return fmt.Errorf("open encrypted backup archive: %w", err)
	}
	defer in.Close()
	epoch, nonce, err := archiveEpoch(in)
	if err != nil {
		return err
	}
	key, err := keyForEpoch(epoch)
	if err != nil {
		return err
	}
	stream, err := chacha20.NewUnauthenticatedCipher(key[:], nonce)
	if err != nil {
		return fmt.Errorf("initialize backup decryption: %w", err)
	}
	compressed, err := gzip.NewReader(&cipher.StreamReader{S: stream, R: in})
	if err != nil {
		return fmt.Errorf("decrypt backup archive: key, epoch, or archive format is invalid: %w", err)
	}
	defer compressed.Close()
	root, err := filepath.Abs(strings.TrimSpace(destination))
	if err != nil {
		return fmt.Errorf("resolve restore destination: %w", err)
	}
	if info, err := os.Lstat(root); err == nil {
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("restore destination must not be a symlink: %s", root)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect restore destination: %w", err)
	}
	root, err = resolveExistingAncestors(root)
	if err != nil {
		return fmt.Errorf("resolve restore destination ancestors: %w", err)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create restore destination: %w", err)
	}
	tarReader := tar.NewReader(&contextReader{ctx: ctx, reader: compressed})
	type pendingSymlink struct {
		path   string
		target string
	}
	type pendingDirectory struct {
		path    string
		mode    fs.FileMode
		modTime time.Time
	}
	var symlinks []pendingSymlink
	var directories []pendingDirectory
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read decrypted backup archive: %w", err)
		}
		target, err := safeArchiveTarget(root, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := ensureSafeParents(root, target); err != nil {
				return err
			}
			directories = append(directories, pendingDirectory{
				path: target, mode: fs.FileMode(header.Mode) & 0o777, modTime: header.ModTime,
			})
			if info, err := os.Lstat(target); err == nil {
				if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
					return fmt.Errorf("restore directory target is not a directory: %s", target)
				}
				continue
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err := os.MkdirAll(target, 0o700); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := ensureSafeParents(root, target); err != nil {
				return err
			}
			if info, err := os.Lstat(target); err == nil {
				if !overwrite {
					return fmt.Errorf("restore target already exists: %s", target)
				}
				if info.IsDir() {
					return fmt.Errorf("refusing to replace restore directory with a file: %s", target)
				}
				if err := os.Remove(target); err != nil {
					return err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
			file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, fs.FileMode(header.Mode)&0o777)
			if err != nil {
				return fmt.Errorf("create restored file %s: %w", target, err)
			}
			_, copyErr := io.CopyN(file, tarReader, header.Size)
			closeErr := file.Close()
			if copyErr != nil {
				return fmt.Errorf("restore file %s: %w", target, copyErr)
			}
			if closeErr != nil {
				return closeErr
			}
			if err := os.Chmod(target, fs.FileMode(header.Mode)&0o777); err != nil {
				return err
			}
			if !header.ModTime.IsZero() {
				if err := os.Chtimes(target, header.ModTime, header.ModTime); err != nil {
					return err
				}
			}
		case tar.TypeSymlink:
			if err := validateArchiveSymlink(root, target, header.Linkname); err != nil {
				return err
			}
			symlinks = append(symlinks, pendingSymlink{path: target, target: header.Linkname})
		default:
			return fmt.Errorf("unsupported archive entry type for %q", header.Name)
		}
	}
	for _, link := range symlinks {
		if err := ensureSafeParents(root, link.path); err != nil {
			return err
		}
		if !overwrite {
			if _, err := os.Lstat(link.path); err == nil {
				return fmt.Errorf("restore target already exists: %s", link.path)
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		} else {
			if info, err := os.Lstat(link.path); err == nil {
				if info.IsDir() {
					return fmt.Errorf("refusing to replace restore directory with a symlink: %s", link.path)
				}
				if err := os.Remove(link.path); err != nil {
					return err
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := os.Symlink(link.target, link.path); err != nil {
			return fmt.Errorf("restore symlink %s: %w", link.path, err)
		}
	}
	for i := len(directories) - 1; i >= 0; i-- {
		directory := directories[i]
		if err := os.Chmod(directory.path, directory.mode); err != nil {
			return err
		}
		if !directory.modTime.IsZero() {
			if err := os.Chtimes(directory.path, directory.modTime, directory.modTime); err != nil {
				return err
			}
		}
	}
	return nil
}

func safeArchiveTarget(root, name string) (string, error) {
	if strings.Contains(name, `\`) || !filepath.IsLocal(filepath.FromSlash(name)) {
		return "", fmt.Errorf("unsafe path in backup archive: %q", name)
	}
	target := filepath.Join(root, filepath.FromSlash(name))
	relative, err := filepath.Rel(root, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("backup archive path escapes restore destination: %q", name)
	}
	return target, nil
}

func ensureSafeParents(root, target string) error {
	parent := filepath.Dir(target)
	relative, err := filepath.Rel(root, parent)
	if err != nil {
		return err
	}
	current := root
	if relative == "." {
		return nil
	}
	for _, segment := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, segment)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("unsafe restore parent: %s", current)
		}
	}
	return nil
}

func validateArchiveSymlink(root, target, link string) error {
	if strings.Contains(link, `\`) || filepath.IsAbs(link) {
		return fmt.Errorf("absolute symlink target is not allowed in backup restore: %q", link)
	}
	resolved := filepath.Clean(filepath.Join(filepath.Dir(target), filepath.FromSlash(link)))
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return fmt.Errorf("symlink target escapes restore destination: %q", link)
	}
	return nil
}

func pathWithin(root, target string) (bool, error) {
	absRoot, err := filepath.Abs(strings.TrimSpace(root))
	if err != nil {
		return false, err
	}
	absTarget, err := filepath.Abs(strings.TrimSpace(target))
	if err != nil {
		return false, err
	}
	relative, err := filepath.Rel(absRoot, absTarget)
	if err != nil {
		return false, err
	}
	return relative == "." ||
		(relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
}

func resolvedPathWithin(root, target string) (bool, error) {
	resolvedRoot, err := resolveExistingAncestors(root)
	if err != nil {
		return false, err
	}
	resolvedTarget, err := resolveExistingAncestors(target)
	if err != nil {
		return false, err
	}
	return pathWithin(resolvedRoot, resolvedTarget)
}

func resolveExistingAncestors(value string) (string, error) {
	current, err := filepath.Abs(strings.TrimSpace(value))
	if err != nil {
		return "", err
	}
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

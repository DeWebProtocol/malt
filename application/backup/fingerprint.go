package backup

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// FingerprintSource computes a local-only plaintext tree fingerprint used to
// skip unchanged automatic backups. It is never sent to the Gateway or stored
// in the encrypted filesystem profile.
func FingerprintSource(ctx context.Context, source string) (string, error) {
	root, displayName, err := openPinnedSource(source)
	if err != nil {
		return "", err
	}
	defer root.Close()
	return fingerprintPinnedSource(ctx, root, displayName)
}

func openPinnedSource(source string) (*os.Root, string, error) {
	if source == "" {
		return nil, "", fmt.Errorf("backup source is required")
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		return nil, "", err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, "", fmt.Errorf("fingerprint backup source: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, "", fmt.Errorf("fingerprint backup source: source must be a real directory")
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, "", fmt.Errorf("fingerprint backup source: %w", err)
	}
	opened, err := root.Open(".")
	if err != nil {
		_ = root.Close()
		return nil, "", fmt.Errorf("fingerprint backup source: %w", err)
	}
	openedInfo, statErr := opened.Stat()
	closeErr := opened.Close()
	if statErr != nil || closeErr != nil {
		_ = root.Close()
		return nil, "", fmt.Errorf("fingerprint backup source: %w", errors.Join(statErr, closeErr))
	}
	if !openedInfo.IsDir() || !os.SameFile(info, openedInfo) {
		_ = root.Close()
		return nil, "", fmt.Errorf("fingerprint backup source: source changed while it was opened")
	}
	return root, filepath.Base(abs), nil
}

func fingerprintPinnedSource(ctx context.Context, root *os.Root, displayName string) (string, error) {
	if root == nil || displayName == "" {
		return "", fmt.Errorf("fingerprint backup source: pinned source is incomplete")
	}
	hash := sha256.New()
	var walk func(string) error
	walk = func(relative string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		rootName := relative
		if rootName == "" {
			rootName = "."
		}
		info, err := root.Lstat(rootName)
		if err != nil {
			return err
		}
		displayPath := displayName
		if relative != "" {
			displayPath = filepath.Join(displayName, relative)
		}
		writeFingerprintField(hash, []byte(filepath.ToSlash(displayPath)))
		var metadata [24]byte
		binary.BigEndian.PutUint64(metadata[0:8], uint64(info.Mode()))
		binary.BigEndian.PutUint64(metadata[8:16], uint64(info.Size()))
		binary.BigEndian.PutUint64(metadata[16:24], uint64(info.ModTime().UnixNano()))
		_, _ = hash.Write(metadata[:])
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := root.Readlink(rootName)
			if err != nil {
				return err
			}
			writeFingerprintField(hash, []byte(target))
		case info.Mode().IsRegular():
			file, err := root.Open(rootName)
			if err != nil {
				return err
			}
			openedInfo, statErr := file.Stat()
			if statErr != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
				_ = file.Close()
				if statErr != nil {
					return statErr
				}
				return fmt.Errorf("backup source file changed while it was opened: %s", displayPath)
			}
			_, copyErr := io.Copy(hash, &contextReader{ctx: ctx, reader: file})
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
		case info.IsDir():
			directory, err := root.Open(rootName)
			if err != nil {
				return err
			}
			openedInfo, statErr := directory.Stat()
			if statErr != nil || !openedInfo.IsDir() || !os.SameFile(info, openedInfo) {
				_ = directory.Close()
				if statErr != nil {
					return statErr
				}
				return fmt.Errorf("backup source directory changed while it was opened: %s", displayPath)
			}
			entries, readErr := directory.ReadDir(-1)
			closeErr := directory.Close()
			if readErr != nil || closeErr != nil {
				return errors.Join(readErr, closeErr)
			}
			sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
			for _, entry := range entries {
				child := entry.Name()
				if relative != "" {
					child = filepath.Join(relative, child)
				}
				if err := walk(child); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("unsupported filesystem object in backup: %s", displayPath)
		}
		return nil
	}
	if err := walk(""); err != nil {
		return "", fmt.Errorf("fingerprint backup source: %w", err)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func fingerprintRootedDirectory(ctx context.Context, parent *os.Root, relative, displayName string) (string, error) {
	if parent == nil || relative == "" {
		return "", fmt.Errorf("fingerprint rooted directory request is incomplete")
	}
	root, err := parent.OpenRoot(relative)
	if err != nil {
		return "", err
	}
	defer root.Close()
	return fingerprintPinnedSource(ctx, root, displayName)
}

func writeFingerprintField(writer io.Writer, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(value)
}

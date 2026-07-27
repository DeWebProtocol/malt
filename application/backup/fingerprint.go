package backup

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// FingerprintSource computes a local-only plaintext tree fingerprint used to
// skip unchanged automatic backups. It is never sent to the Gateway or stored
// in the remote archive framing.
func FingerprintSource(ctx context.Context, source string) (string, error) {
	if strings.TrimSpace(source) == "" {
		return "", fmt.Errorf("backup source is required")
	}
	abs, err := filepath.Abs(source)
	if err != nil {
		return "", err
	}
	parent := filepath.Dir(abs)
	hash := sha256.New()
	err = filepath.WalkDir(abs, func(current string, entry fs.DirEntry, walkErr error) error {
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
		relative, err := filepath.Rel(parent, current)
		if err != nil {
			return err
		}
		writeFingerprintField(hash, []byte(filepath.ToSlash(relative)))
		var metadata [24]byte
		binary.BigEndian.PutUint64(metadata[0:8], uint64(info.Mode()))
		binary.BigEndian.PutUint64(metadata[8:16], uint64(info.Size()))
		binary.BigEndian.PutUint64(metadata[16:24], uint64(info.ModTime().UnixNano()))
		_, _ = hash.Write(metadata[:])
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(current)
			if err != nil {
				return err
			}
			writeFingerprintField(hash, []byte(target))
		case info.Mode().IsRegular():
			file, err := os.Open(current)
			if err != nil {
				return err
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
			// Directory metadata above is sufficient.
		default:
			return fmt.Errorf("unsupported filesystem object in backup: %s", current)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("fingerprint backup source: %w", err)
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func writeFingerprintField(writer io.Writer, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(value)
}

//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package durablefile

import (
	"os"
	"path/filepath"
)

// SyncParent asks the filesystem to persist a preceding atomic rename.
func SyncParent(path string) error {
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

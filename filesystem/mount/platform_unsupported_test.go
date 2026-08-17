//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly && !windows

package mount

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestOpenStoreFailsClosedWithoutProcessReleasedLease(t *testing.T) {
	if _, err := OpenStore(filepath.Join(t.TempDir(), "mounts.json")); !errors.Is(err, ErrUnsupportedPlatform) {
		t.Fatalf("OpenStore error = %v, want ErrUnsupportedPlatform", err)
	}
}

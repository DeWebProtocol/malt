//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly && !windows

package mount

import "os"

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}

//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package mount

import "os"

func replaceFile(source, target string) error {
	return os.Rename(source, target)
}

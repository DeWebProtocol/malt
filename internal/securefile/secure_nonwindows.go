//go:build !windows

// Package securefile applies owner-only protection to local client state.
package securefile

import "os"

func Secure(path string) error {
	return os.Chmod(path, 0o600)
}

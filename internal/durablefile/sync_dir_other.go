//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly

package durablefile

// SyncParent is a no-op where directory fsync is unavailable.
func SyncParent(string) error { return nil }

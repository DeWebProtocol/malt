// Package filelock provides bounded cross-process advisory locks for local
// runtime state transitions.
package filelock

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

func Acquire(path string, timeout time.Duration) (func() error, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("local lock path is empty")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("local lock timeout must be positive")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	return acquire(path, timeout)
}

// retryableUnlockClose keeps the descriptor open when unlock fails so callers
// can retry without losing an ambiguously held lease. Once unlock succeeds,
// Close is attempted exactly once; os.File.Close renders the descriptor
// unusable even when it reports an error, so later retries acknowledge the
// already-released lease instead of operating on an invalid handle.
func retryableUnlockClose(unlock, closeFile func() error) func() error {
	var mu sync.Mutex
	unlocked := false
	closed := false
	return func() error {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return nil
		}
		if !unlocked {
			if err := unlock(); err != nil {
				return err
			}
			unlocked = true
		}
		err := closeFile()
		closed = true
		return err
	}
}

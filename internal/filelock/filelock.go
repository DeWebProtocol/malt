// Package filelock provides bounded cross-process advisory locks for local
// runtime state transitions.
package filelock

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

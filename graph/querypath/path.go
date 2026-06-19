// Package querypath implements locked path canonicalization for stat and content APIs.
//
// Rules:
//   - empty or omitted path means current root
//   - leading "/" is accepted and removed
//   - ".", "..", empty interior segments, and NUL bytes are rejected
package querypath

import (
	"errors"
	"fmt"
	"strings"
)

// ErrInvalidQueryPath identifies malformed client query paths.
var ErrInvalidQueryPath = errors.New("invalid query path")

// CanonicalizeQueryPath normalizes a path from a stat/content query string.
// The empty string denotes the root. Leading slashes are stripped;
// internal path semantics are validated but otherwise unchanged.
func CanonicalizeQueryPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "/")
	if p == "" {
		return "", nil
	}
	if strings.ContainsRune(p, '\x00') {
		return "", fmt.Errorf("%w: contains NUL byte", ErrInvalidQueryPath)
	}
	parts := strings.Split(p, "/")
	for _, part := range parts {
		switch part {
		case "":
			return "", fmt.Errorf("%w: contains empty segment", ErrInvalidQueryPath)
		case ".":
			return "", fmt.Errorf("%w: contains current-directory segment", ErrInvalidQueryPath)
		case "..":
			return "", fmt.Errorf("%w: contains parent-directory segment", ErrInvalidQueryPath)
		}
	}
	return p, nil
}

// Package querypath implements locked path canonicalization for stat and content APIs.
//
// Rules:
//   - empty or omitted path means current root
//   - leading "/" is accepted and removed
//   - ".", "..", empty interior segments, and NUL bytes are rejected
package querypath

import (
	"fmt"
	"strings"
)

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
		return "", fmt.Errorf("query path contains NUL byte")
	}
	parts := strings.Split(p, "/")
	for _, part := range parts {
		switch part {
		case "":
			return "", fmt.Errorf("query path contains empty segment")
		case ".":
			return "", fmt.Errorf("query path contains current-directory segment")
		case "..":
			return "", fmt.Errorf("query path contains parent-directory segment")
		}
	}
	return p, nil
}

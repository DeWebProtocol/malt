// Package querypath implements locked path canonicalization for stat and content APIs.
//
// Rules:
//   - empty or omitted path means current root
//   - leading "/" is accepted and removed
package querypath

import "strings"

// CanonicalizeQueryPath normalizes a path from a stat/content query string.
// The empty string denotes the root. Leading slashes are stripped;
// internal path semantics are unchanged.
func CanonicalizeQueryPath(p string) string {
	p = strings.TrimSpace(p)
	return strings.TrimPrefix(p, "/")
}

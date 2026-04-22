// Package bucketpath implements locked path canonicalization for bucket stat and content APIs.
//
// Rules (Implementation plan Phase 0):
//   - empty or omitted path means bucket root
//   - leading "/" is accepted and removed
package bucketpath

import "strings"

// CanonicalizeQueryPath normalizes a path from a stat/content query string.
// The empty string denotes the bucket root. Leading slashes are stripped;
// internal path semantics are unchanged.
func CanonicalizeQueryPath(p string) string {
	p = strings.TrimSpace(p)
	return strings.TrimPrefix(p, "/")
}

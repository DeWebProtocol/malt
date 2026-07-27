// Package bucketbranch owns the client-side grammar for writable managed
// Bucket refs. It mirrors the Gateway's public branch-name contract.
package bucketbranch

import (
	"fmt"
	"strings"
)

// NormalizeSelector returns main for the empty/default selector and otherwise
// returns a canonical explicit branch name without the heads/ namespace.
func NormalizeSelector(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "main" {
		return "main", nil
	}
	return NormalizeExplicit(raw)
}

// NormalizeExplicit validates one user-created branch and removes an optional
// public heads/ namespace prefix.
func NormalizeExplicit(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimPrefix(raw, "heads/")
	if raw == "" || raw == "main" || strings.HasPrefix(raw, "conflicts/") ||
		len(raw) > 128 || strings.HasPrefix(raw, "/") || strings.HasSuffix(raw, "/") ||
		strings.Contains(raw, "..") {
		return "", fmt.Errorf("invalid Bucket branch %q", raw)
	}
	for _, segment := range strings.Split(raw, "/") {
		if !validIdentifier(segment) {
			return "", fmt.Errorf("invalid Bucket branch %q", raw)
		}
	}
	return raw, nil
}

func validIdentifier(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// Package bucketbranch owns the runtime-side grammar for writable managed
// Bucket refs. It mirrors the Gateway's public branch-name contract.
package bucketbranch

import (
	"fmt"
	"strings"
)

// NormalizeSelector returns main for the empty/default selector and otherwise
// returns an idempotent explicit selector. A name beginning with heads/ keeps
// its namespace prefix so passing the result through another adapter cannot
// select a different branch.
func NormalizeSelector(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "main" {
		return "main", nil
	}
	return NormalizeExplicit(raw)
}

// NormalizeExplicit validates one user-created branch selector. It removes the
// optional heads/ namespace only when the remaining name is unambiguous.
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
	if strings.HasPrefix(raw, "heads/") {
		return "heads/" + raw, nil
	}
	return raw, nil
}

// RefName returns the exact registry ref addressed by a branch selector.
func RefName(raw string) (string, error) {
	selector, err := NormalizeSelector(raw)
	if err != nil || selector == "main" {
		return selector, err
	}
	return "heads/" + strings.TrimPrefix(selector, "heads/"), nil
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

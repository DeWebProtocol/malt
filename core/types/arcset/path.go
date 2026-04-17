package arcset

import "strings"

// Path is a canonical arc path used inside MALT core components.
// The zero value represents an empty path.
type Path string

// CanonicalizePath normalizes a raw path into a stable arcset form.
// It removes empty segments and joins the remaining segments with '/'.
func CanonicalizePath(path string) Path {
	if path == "" {
		return ""
	}

	parts := strings.Split(path, "/")
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			continue
		}
		filtered = append(filtered, part)
	}
	return Path(strings.Join(filtered, "/"))
}

// String returns the canonical string form of the path.
func (p Path) String() string {
	return string(p)
}

// IsEmpty reports whether the path is empty.
func (p Path) IsEmpty() bool {
	return p == ""
}

// Segments returns the canonical path split into segments.
func (p Path) Segments() []string {
	if p.IsEmpty() {
		return nil
	}
	return strings.Split(p.String(), "/")
}

// Depth returns the number of path segments.
func (p Path) Depth() int {
	return len(p.Segments())
}

// HasPrefix reports whether prefix is a full-segment prefix of p.
func (p Path) HasPrefix(prefix Path) bool {
	if prefix.IsEmpty() {
		return true
	}
	if p == prefix {
		return true
	}

	s := p.String()
	pre := prefix.String()
	return strings.HasPrefix(s, pre+"/")
}

// Consume removes prefix from the front of p when prefix is a full-segment prefix.
// It returns the remaining canonical path and whether the consumption succeeded.
func (p Path) Consume(prefix Path) (Path, bool) {
	if prefix.IsEmpty() {
		return p, true
	}
	if p == prefix {
		return "", true
	}
	if !p.HasPrefix(prefix) {
		return "", false
	}

	s := p.String()
	pre := prefix.String()
	return Path(s[len(pre)+1:]), true
}

// PrefixesLongestFirst returns all non-empty prefixes ordered from longest to shortest.
func (p Path) PrefixesLongestFirst() []Path {
	segments := p.Segments()
	if len(segments) == 0 {
		return nil
	}

	prefixes := make([]Path, 0, len(segments))
	for i := len(segments); i > 0; i-- {
		prefixes = append(prefixes, Path(strings.Join(segments[:i], "/")))
	}
	return prefixes
}

package unixfs

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dewebprotocol/malt/auth/arcset"
)

var (
	ErrReservedPath = errors.New("unixfs path uses a reserved segment")
	ErrInvalidPath  = errors.New("unixfs path contains an unsupported segment")
)

// ParsePath applies the UnixFS application model's relative-path policy. This
// policy is intentionally stricter than generic MALT arc coordinates.
func ParsePath(path string) ([]string, error) {
	canonical := arcset.CanonicalizePath(path)
	if canonical.IsEmpty() {
		return nil, nil
	}
	segments := canonical.Segments()
	for _, segment := range segments {
		if strings.HasPrefix(segment, "@") {
			return nil, fmt.Errorf("%w: %s", ErrReservedPath, segment)
		}
		if !isPortableSegment(segment) {
			return nil, fmt.Errorf("%w: %s", ErrInvalidPath, segment)
		}
	}
	return segments, nil
}

func isPortableSegment(segment string) bool {
	if segment == "" || segment == "." || segment == ".." {
		return false
	}
	for _, r := range segment {
		switch {
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

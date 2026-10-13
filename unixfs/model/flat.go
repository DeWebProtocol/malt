package unixfs

import (
	"fmt"
	"strings"
)

// FlatPathLabel maps a relative filesystem path to the global flat ArcSet.
// Empty denotes the root manifest payload. A leading slash escapes names that
// start with @ so an actual @payload file cannot shadow the root manifest.
// No valid source path starts with slash, making this mapping injective.
func FlatPathLabel(path string) ([]byte, error) {
	if path == "" {
		return []byte("@payload"), nil
	}
	if _, err := ParseFlatPath(path); err != nil {
		return nil, err
	}
	if strings.HasPrefix(path, "@") {
		path = "/" + path
	}
	return []byte(path), nil
}

// ParseFlatPath accepts lossless imported relative paths, including @ names.
// Interactive staging continues to use ParseCanonicalStagedPath instead.
func ParseFlatPath(path string) ([]string, error) {
	if path == "" {
		return nil, nil
	}
	segments := strings.Split(path, "/")
	for _, segment := range segments {
		if !isPortableSegment(segment) {
			return nil, fmt.Errorf("%w: %s", ErrInvalidPath, path)
		}
	}
	return segments, nil
}

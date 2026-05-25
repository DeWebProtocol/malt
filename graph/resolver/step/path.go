package step

import "github.com/dewebprotocol/malt/auth/arcset"

// SplitPath splits a path into segments.
func SplitPath(path arcset.Path) []string {
	return path.Segments()
}

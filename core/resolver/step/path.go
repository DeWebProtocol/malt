package step

import "strings"

// SplitPath splits a path into segments.
func SplitPath(path string) []string {
	if path == "" {
		return nil
	}
	if path[0] == '/' {
		path = path[1:]
	}
	if path == "" {
		return nil
	}
	return strings.Split(path, "/")
}

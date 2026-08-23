package backup

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func pathWithin(root, target string) (bool, error) {
	if root == "" || target == "" {
		return false, errors.New("filesystem containment paths must not be empty")
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false, err
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return false, err
	}
	relative, err := filepath.Rel(absRoot, absTarget)
	if err != nil {
		return false, err
	}
	return relative == "." ||
		(relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
}

func resolvedPathWithin(root, target string) (bool, error) {
	resolvedRoot, err := resolveExistingAncestors(root)
	if err != nil {
		return false, err
	}
	resolvedTarget, err := resolveExistingAncestors(target)
	if err != nil {
		return false, err
	}
	return pathWithin(resolvedRoot, resolvedTarget)
}

func resolveExistingAncestors(value string) (string, error) {
	if value == "" {
		return "", errors.New("filesystem path must not be empty")
	}
	current, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	var suffix []string
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			for i := len(suffix) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, suffix[i])
			}
			return resolved, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

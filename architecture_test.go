package malt_test

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/dewebprotocol/malt"

func TestProductionImportBoundaries(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Dir(sourceFile)

	tests := []struct {
		name      string
		dir       string
		recursive bool
		forbidden []string
	}{
		{
			name:      "authentication kernel",
			dir:       filepath.Join(root, "auth"),
			recursive: true,
			forbidden: []string{"graph", "runtime", "storage", "layout", "api", "server"},
		},
		{
			name:      "graph ports",
			dir:       filepath.Join(root, "graph"),
			recursive: true,
			forbidden: []string{"runtime", "storage", "layout", "api", "server"},
		},
		{
			name:      "UnixFS adapter",
			dir:       filepath.Join(root, "layout", "unixfs"),
			recursive: true,
			forbidden: []string{"runtime"},
		},
		{
			name:      "module facade",
			dir:       root,
			recursive: false,
			forbidden: []string{"runtime", "storage", "layout", "api", "server"},
		},
		{
			name:      "artifact contract",
			dir:       filepath.Join(root, "artifact"),
			recursive: true,
			forbidden: []string{"graph", "runtime", "storage", "layout", "api", "server"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checkProductionImports(t, tc.dir, tc.recursive, tc.forbidden)
		})
	}
}

func checkProductionImports(t *testing.T, dir string, recursive bool, forbidden []string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != dir && !recursive {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			importPath, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			for _, layer := range forbidden {
				prefix := modulePath + "/" + layer
				if importPath == prefix || strings.HasPrefix(importPath, prefix+"/") {
					t.Errorf("%s imports forbidden layer %q", path, importPath)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

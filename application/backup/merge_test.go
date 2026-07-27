package backup

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestMergePlaintextTreesMergesIndependentChangesAndPreservesNames(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	local := filepath.Join(root, "local")
	remote := filepath.Join(root, "remote")
	destination := filepath.Join(root, "merged")
	writeMergeTree(t, base, map[string]string{
		"shared.txt":   "base",
		"report.local": "unchanged",
	})
	writeMergeTree(t, local, map[string]string{
		"shared.txt":     "base",
		"report.local":   "local edit",
		"local-only.txt": "local",
	})
	writeMergeTree(t, remote, map[string]string{
		"shared.txt":      "remote edit",
		"report.local":    "unchanged",
		"remote-only.txt": "remote",
	})
	conflicts, err := mergePlaintextTrees(context.Background(), base, local, remote, destination)
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 0 {
		t.Fatalf("conflicts = %#v", conflicts)
	}
	for path, want := range map[string]string{
		"shared.txt": "remote edit", "report.local": "local edit",
		"local-only.txt": "local", "remote-only.txt": "remote",
	} {
		body, err := os.ReadFile(filepath.Join(destination, path))
		if err != nil || string(body) != want {
			t.Fatalf("%s = %q, %v; want %q", path, body, err, want)
		}
	}
}

func TestMergePlaintextTreesReportsSamePathConflictWithoutPartialInstall(t *testing.T) {
	root := t.TempDir()
	base := filepath.Join(root, "base")
	local := filepath.Join(root, "local")
	remote := filepath.Join(root, "remote")
	destination := filepath.Join(root, "merged")
	writeMergeTree(t, base, map[string]string{"same.txt": "base"})
	writeMergeTree(t, local, map[string]string{"same.txt": "local"})
	writeMergeTree(t, remote, map[string]string{"same.txt": "remote"})
	conflicts, err := mergePlaintextTrees(context.Background(), base, local, remote, destination)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(conflicts, []string{"same.txt"}) {
		t.Fatalf("conflicts = %#v", conflicts)
	}
	if _, err := os.Stat(destination); !os.IsNotExist(err) {
		t.Fatalf("conflicted merge created destination: %v", err)
	}
}

func writeMergeTree(t *testing.T, root string, values map[string]string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for relative, body := range values {
		target := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

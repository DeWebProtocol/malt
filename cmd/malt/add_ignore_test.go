package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/dewebprotocol/malt/layout/unixfs"
	casmock "github.com/dewebprotocol/malt/storage/cas/mock"
)

func TestAddIgnoreFilterUsesGitignoreAndMaltignore(t *testing.T) {
	root := t.TempDir()
	writeAddIgnoreTestFile(t, root, ".gitignore", "build/\n*.tmp\n")
	writeAddIgnoreTestFile(t, root, ".maltignore", "private.txt\n")
	writeAddIgnoreTestFile(t, root, "build/out.bin", "ignored")
	writeAddIgnoreTestFile(t, root, "note.tmp", "ignored")
	writeAddIgnoreTestFile(t, root, "private.txt", "ignored")
	writeAddIgnoreTestFile(t, root, "keep.txt", "keep")

	filter, err := newAddIgnoreFilter(root, addIgnoreOptions{})
	if err != nil {
		t.Fatalf("new filter: %v", err)
	}
	if err := filter.loadDirectoryRules(root); err != nil {
		t.Fatalf("load root rules: %v", err)
	}

	assertAddIgnored(t, filter, root, "build", true)
	assertAddIgnored(t, filter, root, "build/out.bin", false)
	assertAddIgnored(t, filter, root, "note.tmp", false)
	assertAddIgnored(t, filter, root, "private.txt", false)
	assertAddNotIgnored(t, filter, root, "keep.txt", false)
}

func TestAddIgnoreFilterCanDisableGitignore(t *testing.T) {
	root := t.TempDir()
	writeAddIgnoreTestFile(t, root, ".gitignore", "ignored.txt\n")
	writeAddIgnoreTestFile(t, root, ".maltignore", "malt-only.txt\n")

	filter, err := newAddIgnoreFilter(root, addIgnoreOptions{NoGitignore: true})
	if err != nil {
		t.Fatalf("new filter: %v", err)
	}
	if err := filter.loadDirectoryRules(root); err != nil {
		t.Fatalf("load root rules: %v", err)
	}

	assertAddNotIgnored(t, filter, root, "ignored.txt", false)
	assertAddIgnored(t, filter, root, "malt-only.txt", false)
}

func TestAddIgnoreFilterCanUseExplicitIgnoreFile(t *testing.T) {
	root := t.TempDir()
	external := filepath.Join(t.TempDir(), "extra.ignore")
	if err := os.WriteFile(external, []byte("scratch/\n"), 0o644); err != nil {
		t.Fatalf("write explicit ignore file: %v", err)
	}

	filter, err := newAddIgnoreFilter(root, addIgnoreOptions{IgnoreFiles: []string{external}})
	if err != nil {
		t.Fatalf("new filter: %v", err)
	}

	assertAddIgnored(t, filter, root, "scratch", true)
	assertAddIgnored(t, filter, root, "scratch/cache.bin", false)
	assertAddNotIgnored(t, filter, root, "src/main.go", false)
}

func TestAddIgnoreFilterLetsMaltignoreReincludeGitignoredPath(t *testing.T) {
	root := t.TempDir()
	writeAddIgnoreTestFile(t, root, ".gitignore", "*.log\n")
	writeAddIgnoreTestFile(t, root, ".maltignore", "!keep.log\n")

	filter, err := newAddIgnoreFilter(root, addIgnoreOptions{})
	if err != nil {
		t.Fatalf("new filter: %v", err)
	}
	if err := filter.loadDirectoryRules(root); err != nil {
		t.Fatalf("load root rules: %v", err)
	}

	assertAddIgnored(t, filter, root, "drop.log", false)
	assertAddNotIgnored(t, filter, root, "keep.log", false)
}

func TestAddIgnoreFilterAlwaysExcludesGitDirectory(t *testing.T) {
	root := t.TempDir()
	filter, err := newAddIgnoreFilter(root, addIgnoreOptions{NoGitignore: true, NoMaltignore: true})
	if err != nil {
		t.Fatalf("new filter: %v", err)
	}

	assertAddIgnored(t, filter, root, ".git", true)
	assertAddIgnored(t, filter, root, ".git/config", false)
	assertAddNotIgnored(t, filter, root, ".github/workflows/test.yml", false)
}

func TestStageDirectoryInputPrunesIgnoredDirectories(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)

	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	writeAddIgnoreTestFile(t, repo, ".gitignore", "ignored/\n")
	writeAddIgnoreTestFile(t, repo, "ignored/file.txt", "ignored")
	writeAddIgnoreTestFile(t, repo, "keep/file.txt", "keep")

	staged, err := buildAddStagingTree(ctx, casClient, daemon, []string{repo}, addBuildOptions{})
	if err != nil {
		t.Fatalf("build staging: %v", err)
	}
	if staged.Files != 2 {
		t.Fatalf("staged files = %d, want 2", staged.Files)
	}

	base := filepath.Base(repo)
	mustAddNodeAtPath(t, staged.Root, base+"/keep/file.txt")
	if addNodeExists(staged.Root, base+"/ignored") {
		t.Fatalf("ignored directory should have been pruned: %+v", staged.Root.Children)
	}
}

func TestStageFlatUnixFSDirectoryAppliesMaltignore(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)

	root := newTestRoot(ctx, t, daemon, casClient)

	rootDir := t.TempDir()
	repo := filepath.Join(rootDir, "repo")
	writeAddIgnoreTestFile(t, repo, ".maltignore", "secret.txt\n")
	writeAddIgnoreTestFile(t, repo, "secret.txt", "secret")
	writeAddIgnoreTestFile(t, repo, "keep.txt", "keep")

	result, err := addInputsWithUnixFS(ctx, daemon, casClient, []string{repo}, root, addBuildOptions{
		Target: addTargetMALT,
		Model:  addModelUnixFS,
		Layout: addLayoutFlat,
	})
	if err != nil {
		t.Fatalf("add flat unixfs: %v", err)
	}
	if result.Files != 2 {
		t.Fatalf("files = %d, want 2", result.Files)
	}

	base := filepath.Base(repo)
	if _, err := daemon.Stat(ctx, result.NewRoot, base+"/secret.txt"); err == nil {
		t.Fatal("secret.txt should be ignored")
	}
	if _, err := daemon.Stat(ctx, result.NewRoot, base+"/keep.txt"); err != nil {
		t.Fatalf("keep.txt should be present: %v", err)
	}
}

func TestAddInputsMerkleDAGAppliesIgnoreFilter(t *testing.T) {
	ctx := context.Background()
	casClient := casmock.NewCAS()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	writeAddIgnoreTestFile(t, repo, ".gitignore", "ignored/\n")
	writeAddIgnoreTestFile(t, repo, "ignored/file.txt", "ignored")
	writeAddIgnoreTestFile(t, repo, "keep/file.txt", "keep")

	result, err := addInputsWithUnixFS(ctx, nil, casClient, []string{repo}, "", addBuildOptions{
		Target:     addTargetMerkleDAG,
		Model:      addModelUnixFS,
		FileLayout: addFileLayoutBalanced,
		DirLayout:  addDirLayoutBasic,
	})
	if err != nil {
		t.Fatalf("add merkle dag: %v", err)
	}
	if result.Files != 2 {
		t.Fatalf("files = %d, want 2", result.Files)
	}
}

func writeAddIgnoreTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	name := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(name), err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func assertAddIgnored(t *testing.T, filter *addIgnoreFilter, root, rel string, isDir bool) {
	t.Helper()
	ignored, err := filter.ignored(filepath.Join(root, filepath.FromSlash(rel)), isDir)
	if err != nil {
		t.Fatalf("ignored(%s): %v", rel, err)
	}
	if !ignored {
		t.Fatalf("%s should be ignored", rel)
	}
}

func assertAddNotIgnored(t *testing.T, filter *addIgnoreFilter, root, rel string, isDir bool) {
	t.Helper()
	ignored, err := filter.ignored(filepath.Join(root, filepath.FromSlash(rel)), isDir)
	if err != nil {
		t.Fatalf("ignored(%s): %v", rel, err)
	}
	if ignored {
		t.Fatalf("%s should not be ignored", rel)
	}
}

func addNodeExists(root *unixfs.StagedNode, p string) bool {
	cur := root
	for _, part := range unixfs.SplitStagedPath(p) {
		if cur == nil || cur.Children == nil {
			return false
		}
		cur = cur.Children[part]
	}
	return cur != nil
}

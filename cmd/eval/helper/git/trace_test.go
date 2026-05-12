package git_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	gittrace "github.com/dewebprotocol/malt/cmd/eval/helper/git"
	"github.com/dewebprotocol/malt/cmd/eval/helper/replay"
)

func TestSourceWalkEmitsFirstParentCommitMutationsAndLiveStats(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	ctx := context.Background()
	repo := initTraceRepo(t)
	writeFile(t, repo, "untracked-output.jsonl", "must not enter trace")

	source := gittrace.Source{
		RepoPath: repo,
		Ref:      "HEAD",
		Limit:    3,
	}
	var commits []replay.CommitMutation
	if err := source.Walk(ctx, func(commit replay.CommitMutation) error {
		commits = append(commits, commit)
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}

	if len(commits) != 3 {
		t.Fatalf("commit count = %d, want 3", len(commits))
	}
	if commits[0].Index != 0 || commits[0].Parent != "" {
		t.Fatalf("first commit index/parent = %d/%q, want 0/empty", commits[0].Index, commits[0].Parent)
	}
	if got := commits[0].Mutations[0]; got.Kind != replay.MutationAdd || got.Path != "README.md" {
		t.Fatalf("first mutation = %s %s, want add README.md", got.Kind, got.Path)
	}
	if commits[1].Mutations[0].Kind != replay.MutationModify || commits[1].Mutations[0].Path != "README.md" {
		t.Fatalf("second mutation = %+v, want modify README.md", commits[1].Mutations[0])
	}
	if commits[2].Mutations[0].Kind != replay.MutationRename || commits[2].Mutations[0].OldPath != "README.md" || commits[2].Mutations[0].Path != "docs/README.md" {
		t.Fatalf("third mutation = %+v, want rename README.md -> docs/README.md", commits[2].Mutations[0])
	}
	if commits[2].LiveStats.FileCount != 1 || commits[2].LiveStats.LivePayloadBytes != int64(len("hello world\n")) {
		t.Fatalf("third live stats = %+v, want one renamed file with current bytes", commits[2].LiveStats)
	}
	if len(commits[2].LiveFiles) != 1 || commits[2].LiveFiles[0].Path != "docs/README.md" {
		t.Fatalf("third live files = %+v, want docs/README.md", commits[2].LiveFiles)
	}
}

func TestSourceWalkUsesGitObjectSnapshotWithoutReplayWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	ctx := context.Background()
	repo := initSingleFileRepo(t, "README.md", "hello\n")
	initialWorktrees := worktreePaths(t, repo)

	source := gittrace.Source{
		RepoPath: repo,
		Ref:      "HEAD",
		Limit:    1,
	}
	var sawCommit bool
	if err := source.Walk(ctx, func(commit replay.CommitMutation) error {
		sawCommit = true
		if got := worktreePaths(t, repo); !slices.Equal(got, initialWorktrees) {
			t.Fatalf("worktrees during Walk = %v, want unchanged %v", got, initialWorktrees)
		}
		if commit.Snapshot == nil {
			t.Fatal("snapshot reader is nil")
		}
		if len(commit.LiveFiles) != 1 || commit.LiveFiles[0].Hash == "" {
			t.Fatalf("live files = %+v, want one file with blob hash", commit.LiveFiles)
		}
		data, err := commit.Snapshot.ReadBlob(ctx, commit.LiveFiles[0].Hash)
		if err != nil {
			t.Fatalf("ReadBlob: %v", err)
		}
		if string(data) != "hello\n" {
			t.Fatalf("blob data = %q, want committed contents", string(data))
		}
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if !sawCommit {
		t.Fatal("Walk did not emit a commit")
	}
}

func TestSourceWalkReadsCommittedBlobWhenCheckoutIsDirty(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	ctx := context.Background()
	repo := initSingleFileRepo(t, "file.txt", "committed\n")
	writeFile(t, repo, "file.txt", "dirty working tree\n")

	source := gittrace.Source{
		RepoPath: repo,
		Ref:      "HEAD",
		Limit:    1,
	}
	var got []byte
	if err := source.Walk(ctx, func(commit replay.CommitMutation) error {
		if commit.Snapshot == nil {
			t.Fatal("snapshot reader is nil")
		}
		if len(commit.LiveFiles) != 1 {
			t.Fatalf("live files = %+v, want one file", commit.LiveFiles)
		}
		data, err := commit.Snapshot.ReadBlob(ctx, commit.LiveFiles[0].Hash)
		if err != nil {
			return err
		}
		got = data
		return nil
	}); err != nil {
		t.Fatalf("Walk: %v", err)
	}
	if string(got) != "committed\n" {
		t.Fatalf("blob data = %q, want committed contents", string(got))
	}
	if branch := currentBranch(t, repo); branch != "main" {
		t.Fatalf("source repo branch = %q, want main", branch)
	}
}

func TestSourceWalkDoesNotMutateRepoPathCheckoutOnFailure(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	ctx := context.Background()
	repo := initTraceRepo(t)
	runGit(t, repo, "checkout", "main")

	walkErr := errors.New("stop replay")
	source := gittrace.Source{
		RepoPath: repo,
		Ref:      "HEAD",
		Limit:    2,
	}
	err := source.Walk(ctx, func(commit replay.CommitMutation) error {
		return walkErr
	})
	if !errors.Is(err, walkErr) {
		t.Fatalf("Walk error = %v, want %v", err, walkErr)
	}

	if branch := currentBranch(t, repo); branch != "main" {
		t.Fatalf("source repo branch = %q, want main", branch)
	}
}

func TestCacheNameForURLIncludesFullRepositoryIdentity(t *testing.T) {
	first := gittrace.CacheNameForURL("https://github.com/orgA/project.git")
	second := gittrace.CacheNameForURL("https://github.com/orgB/project.git")
	if first == second {
		t.Fatalf("cache names collide: %q", first)
	}
}

func TestCacheNameForURLIsBoundedForLongLocalPaths(t *testing.T) {
	name := gittrace.CacheNameForURL("C:\\" + strings.Repeat("long-path-segment\\", 20) + "project.git")
	if len(name) > 80 {
		t.Fatalf("cache name length = %d, want <= 80: %q", len(name), name)
	}
}

func TestEnsureCloneRejectsMismatchedExistingOrigin(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
	ctx := context.Background()
	cacheDir := t.TempDir()
	url := "https://github.com/orgA/project.git"
	cachePath := filepath.Join(cacheDir, gittrace.CacheNameForURL(url))
	if err := os.MkdirAll(cachePath, 0755); err != nil {
		t.Fatalf("mkdir cache path: %v", err)
	}
	runGit(t, cachePath, "init")
	runGit(t, cachePath, "remote", "add", "origin", "https://github.com/orgB/project.git")

	if _, err := gittrace.EnsureClone(ctx, url, cacheDir); err == nil {
		t.Fatal("expected mismatched origin error")
	}
}

func initTraceRepo(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "bench@example.test")
	runGit(t, repo, "config", "user.name", "Bench Test")

	writeFile(t, repo, "README.md", "hello\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "initial")

	writeFile(t, repo, "README.md", "hello world\n")
	runGit(t, repo, "add", "README.md")
	runGit(t, repo, "commit", "-m", "modify readme")

	if err := os.MkdirAll(filepath.Join(repo, "docs"), 0755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	if err := os.Rename(filepath.Join(repo, "README.md"), filepath.Join(repo, "docs", "README.md")); err != nil {
		t.Fatalf("rename: %v", err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-m", "rename readme")
	return repo
}

func initSingleFileRepo(t *testing.T, rel, content string) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-b", "main")
	runGit(t, repo, "config", "user.email", "bench@example.test")
	runGit(t, repo, "config", "user.name", "Bench Test")
	writeFile(t, repo, rel, content)
	runGit(t, repo, "add", rel)
	runGit(t, repo, "commit", "-m", "initial")
	return repo
}

func writeFile(t *testing.T, repo, rel, content string) {
	t.Helper()
	path := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func runGit(t *testing.T, repo string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, out)
	}
}

func currentBranch(t *testing.T, repo string) string {
	t.Helper()
	cmd := exec.Command("git", "symbolic-ref", "--short", "HEAD")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("current branch failed: %v\n%s", err, out)
	}
	return string(bytesTrimSpace(out))
}

func worktreePaths(t *testing.T, repo string) []string {
	t.Helper()
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = repo
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("worktree list failed: %v\n%s", err, out)
	}
	var paths []string
	for _, line := range strings.Split(string(out), "\n") {
		path, ok := strings.CutPrefix(line, "worktree ")
		if ok {
			paths = append(paths, filepath.Clean(path))
		}
	}
	return paths
}

func bytesTrimSpace(in []byte) []byte {
	for len(in) > 0 && (in[0] == '\r' || in[0] == '\n' || in[0] == ' ' || in[0] == '\t') {
		in = in[1:]
	}
	for len(in) > 0 {
		last := in[len(in)-1]
		if last != '\r' && last != '\n' && last != ' ' && last != '\t' {
			break
		}
		in = in[:len(in)-1]
	}
	return in
}

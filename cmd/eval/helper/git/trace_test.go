package git_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

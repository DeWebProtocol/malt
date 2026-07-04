package git

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestRevListArgsUseTopoOrder(t *testing.T) {
	args := revListArgs("HEAD", true)
	if !slices.Contains(args, "--topo-order") {
		t.Fatalf("rev-list args = %v, want --topo-order", args)
	}
	if !slices.Contains(args, "--first-parent") {
		t.Fatalf("rev-list args = %v, want --first-parent", args)
	}
}

func TestGitOutputKeepsSuccessfulStderrOutOfStdout(t *testing.T) {
	binDir := t.TempDir()
	gitPath := filepath.Join(binDir, "git")
	script := "#!/bin/sh\nprintf 'raw-output\\n'\nprintf 'warning: rename detection skipped\\n' >&2\n"
	if err := os.WriteFile(gitPath, []byte(script), 0755); err != nil {
		t.Fatalf("write fake git: %v", err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	out, err := gitOutput(context.Background(), "", "diff-tree")
	if err != nil {
		t.Fatalf("gitOutput: %v", err)
	}
	if strings.Contains(out, "warning:") {
		t.Fatalf("gitOutput returned stderr warning in stdout: %q", out)
	}
	if out != "raw-output\n" {
		t.Fatalf("gitOutput = %q, want raw stdout only", out)
	}
}

// Package git extracts deterministic commit-mutation traces from Git repos.
package git

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/dewebprotocol/malt/cmd/eval/helper/replay"
)

// Source streams Git object commit snapshots as replay.CommitMutation values.
type Source struct {
	RepoURL      string
	RepoPath     string
	CloneBaseDir string
	Ref          string
	Limit        int
	FirstParent  bool
}

// Walk visits commits in chronological replay order.
func (s Source) Walk(ctx context.Context, fn func(replay.CommitMutation) error) error {
	if fn == nil {
		return fmt.Errorf("walk callback is nil")
	}
	sourceRepo, err := s.repositoryPath(ctx)
	if err != nil {
		return err
	}
	ref := strings.TrimSpace(s.Ref)
	if ref == "" {
		ref = "HEAD"
	}
	commits, err := s.revList(ctx, sourceRepo, ref)
	if err != nil {
		return err
	}
	snapshot := objectSnapshot{repo: sourceRepo}
	for i, commit := range commits {
		parent, err := firstParent(ctx, sourceRepo, commit)
		if err != nil {
			return err
		}
		mutations, err := mutationsForCommit(ctx, sourceRepo, parent, commit)
		if err != nil {
			return err
		}
		liveFiles, liveStats, skipped, err := scanSnapshot(ctx, sourceRepo, commit)
		if err != nil {
			return err
		}
		enrichMutations(mutations, liveFiles)
		if err := fn(replay.CommitMutation{
			Repo:      repoName(sourceRepo, s.RepoURL),
			Commit:    commit,
			Parent:    parent,
			Index:     i,
			Snapshot:  snapshot,
			Mutations: mutations,
			LiveStats: liveStats,
			LiveFiles: liveFiles,
			Skipped:   skipped,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s Source) repositoryPath(ctx context.Context) (string, error) {
	if strings.TrimSpace(s.RepoPath) != "" {
		return filepath.Abs(s.RepoPath)
	}
	return CloneForReplay(ctx, s.RepoURL, s.CloneBaseDir)
}

// CloneForReplay creates a fresh managed clone for repoURL under baseDir.
func CloneForReplay(ctx context.Context, repoURL, baseDir string) (string, error) {
	if baseDir == "" {
		baseDir = filepath.Join(".eval-cache", "repos")
	}
	path, err := ClonePathForURL(baseDir, repoURL)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	if err := os.RemoveAll(path); err != nil {
		return "", fmt.Errorf("remove stale managed clone %s: %w", path, err)
	}
	if err := runGit(ctx, "", "clone", repoURL, path); err != nil {
		_ = os.RemoveAll(path)
		return "", err
	}
	return path, nil
}

func (s Source) revList(ctx context.Context, repo, ref string) ([]string, error) {
	args := revListArgs(ref, s.FirstParent)
	out, err := gitOutput(ctx, repo, args...)
	if err != nil {
		return nil, err
	}
	lines := strings.Fields(strings.TrimSpace(out))
	if len(lines) == 0 {
		return nil, fmt.Errorf("no commits found for ref %q", ref)
	}
	if s.Limit > 0 && len(lines) > s.Limit {
		lines = lines[:s.Limit]
	}
	return lines, nil
}

func revListArgs(ref string, firstParent bool) []string {
	args := []string{"rev-list", "--topo-order", "--reverse"}
	if firstParent {
		args = append(args, "--first-parent")
	}
	return append(args, ref)
}

func firstParent(ctx context.Context, repo, commit string) (string, error) {
	out, err := gitOutput(ctx, repo, "rev-list", "--parents", "-n", "1", commit)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(out)
	if len(fields) < 2 {
		return "", nil
	}
	return fields[1], nil
}

func mutationsForCommit(ctx context.Context, repo, parent, commit string) ([]replay.FileMutation, error) {
	if parent == "" {
		out, err := gitOutput(ctx, repo, "ls-tree", "-r", "--name-only", commit)
		if err != nil {
			return nil, err
		}
		var mutations []replay.FileMutation
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			mutations = append(mutations, replay.FileMutation{Kind: replay.MutationAdd, Path: filepath.ToSlash(line)})
		}
		return mutations, nil
	}
	out, err := gitOutput(ctx, repo, "diff-tree", "--no-commit-id", "--name-status", "-r", "-M", parent, commit)
	if err != nil {
		return nil, err
	}
	renamedContent, err := renamedContentChanges(ctx, repo, parent, commit)
	if err != nil {
		return nil, err
	}
	var mutations []replay.FileMutation
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			return nil, fmt.Errorf("invalid git diff-tree line %q", line)
		}
		status := fields[0]
		switch status[0] {
		case 'A', 'C':
			mutations = append(mutations, replay.FileMutation{Kind: replay.MutationAdd, Path: filepath.ToSlash(fields[len(fields)-1])})
		case 'M', 'T':
			mutations = append(mutations, replay.FileMutation{Kind: replay.MutationModify, Path: filepath.ToSlash(fields[1])})
		case 'D':
			mutations = append(mutations, replay.FileMutation{Kind: replay.MutationDelete, Path: filepath.ToSlash(fields[1])})
		case 'R':
			if len(fields) < 3 {
				return nil, fmt.Errorf("invalid rename line %q", line)
			}
			oldPath := filepath.ToSlash(fields[1])
			path := filepath.ToSlash(fields[2])
			mutations = append(mutations, replay.FileMutation{
				Kind:           replay.MutationRename,
				OldPath:        oldPath,
				Path:           path,
				ContentChanged: renamedContent[renameKey{oldPath: oldPath, path: path}],
			})
		default:
			mutations = append(mutations, replay.FileMutation{Kind: replay.MutationModify, Path: filepath.ToSlash(fields[len(fields)-1])})
		}
	}
	return mutations, nil
}

type renameKey struct {
	oldPath string
	path    string
}

func renamedContentChanges(ctx context.Context, repo, parent, commit string) (map[renameKey]bool, error) {
	out, err := gitOutput(ctx, repo, "diff-tree", "--no-commit-id", "--raw", "-r", "-M", parent, commit)
	if err != nil {
		return nil, err
	}
	changed := make(map[renameKey]bool)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			return nil, fmt.Errorf("invalid git diff-tree raw line %q", line)
		}
		meta := strings.Fields(fields[0])
		if len(meta) < 5 || !strings.HasPrefix(meta[4], "R") {
			continue
		}
		if len(fields) < 3 {
			return nil, fmt.Errorf("invalid rename raw line %q", line)
		}
		oldPath := filepath.ToSlash(fields[1])
		path := filepath.ToSlash(fields[2])
		changed[renameKey{oldPath: oldPath, path: path}] = meta[2] != meta[3]
	}
	return changed, nil
}

func scanSnapshot(ctx context.Context, repo, commit string) ([]replay.LiveFile, replay.LiveStats, replay.SkipStats, error) {
	var (
		files       []replay.LiveFile
		stats       replay.LiveStats
		skipped     replay.SkipStats
		directories = map[string]struct{}{}
		depthSum    int
	)
	out, err := gitOutput(ctx, repo, "ls-tree", "-r", "--long", commit)
	if err != nil {
		return nil, replay.LiveStats{}, replay.SkipStats{}, err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		head, path, ok := strings.Cut(line, "\t")
		if !ok {
			return nil, replay.LiveStats{}, replay.SkipStats{}, fmt.Errorf("invalid ls-tree line %q", line)
		}
		fields := strings.Fields(head)
		if len(fields) < 4 {
			return nil, replay.LiveStats{}, replay.SkipStats{}, fmt.Errorf("invalid ls-tree metadata %q", head)
		}
		mode, objectType, objectHash, sizeText := fields[0], fields[1], fields[2], fields[3]
		rel := filepath.ToSlash(path)
		if mode == "120000" {
			skipped.SymlinkCount++
			continue
		}
		if objectType != "blob" || !strings.HasPrefix(mode, "100") {
			skipped.OtherCount++
			continue
		}
		size, err := strconv.ParseInt(sizeText, 10, 64)
		if err != nil {
			skipped.OtherCount++
			continue
		}
		files = append(files, replay.LiveFile{
			Path: rel,
			Mode: mode,
			Size: size,
			Hash: objectHash,
		})
		stats.LivePayloadBytes += size
		depth := pathDepth(rel)
		depthSum += depth
		if depth > stats.MaxPathDepth {
			stats.MaxPathDepth = depth
		}
		parent := filepath.ToSlash(filepath.Dir(rel))
		for parent != "." && parent != "" {
			directories[parent] = struct{}{}
			next := filepath.ToSlash(filepath.Dir(parent))
			if next == parent {
				break
			}
			parent = next
		}
	}
	stats.FileCount = len(files)
	stats.DirectoryCount = len(directories)
	stats.PathCount = stats.FileCount + stats.DirectoryCount
	if stats.FileCount > 0 {
		stats.AveragePathDepth = float64(depthSum) / float64(stats.FileCount)
	}
	return files, stats, skipped, nil
}

func enrichMutations(mutations []replay.FileMutation, files []replay.LiveFile) {
	byPath := make(map[string]replay.LiveFile, len(files))
	for _, file := range files {
		byPath[file.Path] = file
	}
	for i := range mutations {
		file, ok := byPath[mutations[i].Path]
		if !ok {
			if mutations[i].Kind == replay.MutationModify {
				mutations[i].Kind = replay.MutationDelete
			}
			continue
		}
		mutations[i].Mode = file.Mode
		mutations[i].Size = file.Size
		mutations[i].Hash = file.Hash
	}
}

func pathDepth(path string) int {
	if path == "" {
		return 0
	}
	return strings.Count(filepath.ToSlash(path), "/") + 1
}

func repoName(path, repoURL string) string {
	if repoID, err := CanonicalRepoIDFromURL(repoURL); err == nil {
		return repoID
	}
	return filepath.Base(path)
}

// CanonicalRepoIDFromURL derives the evaluation repository identity from a
// GitHub HTTPS repository URL.
func CanonicalRepoIDFromURL(repoURL string) (string, error) {
	repo, err := parseGitHubHTTPSRepoURL(repoURL)
	if err != nil {
		return "", err
	}
	return "github.com/" + repo.owner + "/" + repo.name, nil
}

// ClonePathForURL returns the local clone path for a GitHub HTTPS repository
// under baseDir.
func ClonePathForURL(baseDir, repoURL string) (string, error) {
	repo, err := parseGitHubHTTPSRepoURL(repoURL)
	if err != nil {
		return "", err
	}
	return filepath.Join(baseDir, repo.owner, repo.name), nil
}

type githubRepo struct {
	owner string
	name  string
}

func parseGitHubHTTPSRepoURL(repoURL string) (githubRepo, error) {
	trimmed := strings.TrimSpace(repoURL)
	if trimmed == "" {
		return githubRepo{}, fmt.Errorf("repo URL is required")
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return githubRepo{}, fmt.Errorf("parse repo URL %q: %w", repoURL, err)
	}
	if parsed.Scheme != "https" || strings.ToLower(parsed.Host) != "github.com" {
		return githubRepo{}, fmt.Errorf("repo URL %q must be https://github.com/<owner>/<repo>", repoURL)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return githubRepo{}, fmt.Errorf("repo URL %q must not include query or fragment", repoURL)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 {
		return githubRepo{}, fmt.Errorf("repo URL %q must be https://github.com/<owner>/<repo>", repoURL)
	}
	owner := strings.ToLower(strings.TrimSpace(parts[0]))
	name := strings.ToLower(strings.TrimSpace(parts[1]))
	if strings.HasSuffix(name, ".git") {
		name = name[:len(name)-len(".git")]
	}
	if !isGitHubOwnerComponent(owner) || !isGitHubRepoComponent(name) {
		return githubRepo{}, fmt.Errorf("repo URL %q must use safe GitHub owner and repo components", repoURL)
	}
	return githubRepo{owner: owner, name: name}, nil
}

func isGitHubOwnerComponent(value string) bool {
	if value == "" || len(value) > 39 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			continue
		}
		return false
	}
	return true
}

func isGitHubRepoComponent(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

type objectSnapshot struct {
	repo string
}

func (s objectSnapshot) ReadBlob(ctx context.Context, hash string) ([]byte, error) {
	if strings.TrimSpace(hash) == "" {
		return nil, fmt.Errorf("blob hash is empty")
	}
	return gitBlob(ctx, s.repo, hash)
}

func gitOutput(ctx context.Context, repo string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if repo != "" {
		cmd.Dir = repo
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w\n%s", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}

func gitBlob(ctx context.Context, repo, hash string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "cat-file", "blob", hash)
	if repo != "" {
		cmd.Dir = repo
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("git cat-file blob %s failed: %w\n%s", hash, err, stderr.String())
	}
	return out, nil
}

func runGit(ctx context.Context, repo string, args ...string) error {
	_, err := gitOutput(ctx, repo, args...)
	return err
}

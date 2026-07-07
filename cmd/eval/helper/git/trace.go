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
	"sort"
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
	var live *liveIndex
	for i, commit := range commits {
		parent, err := firstParent(ctx, sourceRepo, commit)
		if err != nil {
			return err
		}
		mutations, err := mutationsForCommit(ctx, sourceRepo, parent, commit)
		if err != nil {
			return err
		}
		if live == nil {
			live, err = scanSnapshotIndex(ctx, sourceRepo, commit)
			if err != nil {
				return err
			}
			enrichMutations(mutations, live.filesSlice())
		} else {
			files, skippedTargets, err := changedPathMetadata(ctx, sourceRepo, commit, mutations)
			if err != nil {
				return err
			}
			enrichMutationsFromMap(mutations, files)
			live.apply(mutations, files, skippedTargets)
		}
		liveFiles := live.filesSlice()
		liveStats, skipped := live.stats()
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

// CommitCount returns the number of commits Source.Walk would visit after
// applying ref, first-parent, and limit settings.
func (s Source) CommitCount(ctx context.Context) (int, error) {
	sourceRepo, err := s.repositoryPath(ctx)
	if err != nil {
		return 0, err
	}
	ref := strings.TrimSpace(s.Ref)
	if ref == "" {
		ref = "HEAD"
	}
	commits, err := s.revList(ctx, sourceRepo, ref)
	if err != nil {
		return 0, err
	}
	return len(commits), nil
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
	index, err := scanSnapshotIndex(ctx, repo, commit)
	if err != nil {
		return nil, replay.LiveStats{}, replay.SkipStats{}, err
	}
	files := index.filesSlice()
	stats, skipped := index.stats()
	return files, stats, skipped, nil
}

type skippedKind int

const (
	skippedSymlink skippedKind = iota + 1
	skippedOther
)

type liveIndex struct {
	files    map[string]replay.LiveFile
	symlinks map[string]struct{}
	other    map[string]struct{}
}

func newLiveIndex() *liveIndex {
	return &liveIndex{
		files:    make(map[string]replay.LiveFile),
		symlinks: make(map[string]struct{}),
		other:    make(map[string]struct{}),
	}
}

func scanSnapshotIndex(ctx context.Context, repo, commit string) (*liveIndex, error) {
	index := newLiveIndex()
	out, err := gitOutput(ctx, repo, "ls-tree", "-r", "--long", commit)
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		file, kind, ok, err := parseTreeEntry(line)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		index.setPath(file.Path, file, kind)
	}
	return index, nil
}

func (idx *liveIndex) apply(mutations []replay.FileMutation, files map[string]replay.LiveFile, skipped map[string]skippedKind) {
	if idx == nil {
		return
	}
	for _, mutation := range mutations {
		switch mutation.Kind {
		case replay.MutationDelete:
			idx.deletePath(mutation.Path)
			idx.applyTarget(mutation.Path, files, skipped)
		case replay.MutationRename:
			idx.deletePath(mutation.OldPath)
			idx.applyTarget(mutation.Path, files, skipped)
		case replay.MutationAdd, replay.MutationModify:
			idx.deletePath(mutation.Path)
			idx.applyTarget(mutation.Path, files, skipped)
		default:
			idx.deletePath(mutation.Path)
			idx.applyTarget(mutation.Path, files, skipped)
		}
	}
}

func (idx *liveIndex) applyTarget(path string, files map[string]replay.LiveFile, skipped map[string]skippedKind) {
	if file, ok := files[path]; ok {
		idx.setPath(path, file, 0)
		return
	}
	if kind, ok := skipped[path]; ok {
		idx.setPath(path, replay.LiveFile{Path: path}, kind)
	}
}

func (idx *liveIndex) setPath(path string, file replay.LiveFile, kind skippedKind) {
	idx.deletePath(path)
	switch kind {
	case skippedSymlink:
		idx.symlinks[path] = struct{}{}
	case skippedOther:
		idx.other[path] = struct{}{}
	default:
		idx.files[path] = file
	}
}

func (idx *liveIndex) deletePath(path string) {
	delete(idx.files, path)
	delete(idx.symlinks, path)
	delete(idx.other, path)
}

func (idx *liveIndex) filesSlice() []replay.LiveFile {
	if idx == nil {
		return nil
	}
	files := make([]replay.LiveFile, 0, len(idx.files))
	for _, file := range idx.files {
		files = append(files, file)
	}
	sort.Slice(files, func(i, j int) bool {
		return files[i].Path < files[j].Path
	})
	return files
}

func (idx *liveIndex) stats() (replay.LiveStats, replay.SkipStats) {
	var (
		stats       replay.LiveStats
		directories = map[string]struct{}{}
		depthSum    int
	)
	for _, file := range idx.files {
		stats.LivePayloadBytes += file.Size
		rel := file.Path
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
	stats.FileCount = len(idx.files)
	stats.DirectoryCount = len(directories)
	stats.PathCount = stats.FileCount + stats.DirectoryCount
	if stats.FileCount > 0 {
		stats.AveragePathDepth = float64(depthSum) / float64(stats.FileCount)
	}
	return stats, replay.SkipStats{
		SymlinkCount: len(idx.symlinks),
		OtherCount:   len(idx.other),
	}
}

func changedPathMetadata(ctx context.Context, repo, commit string, mutations []replay.FileMutation) (map[string]replay.LiveFile, map[string]skippedKind, error) {
	paths := changedTargetPaths(mutations)
	if len(paths) == 0 {
		return map[string]replay.LiveFile{}, map[string]skippedKind{}, nil
	}
	args := append([]string{"ls-tree", "--long", commit, "--"}, paths...)
	out, err := gitOutput(ctx, repo, args...)
	if err != nil {
		return nil, nil, err
	}
	files := make(map[string]replay.LiveFile, len(paths))
	skipped := make(map[string]skippedKind)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		file, kind, ok, err := parseTreeEntry(line)
		if err != nil {
			return nil, nil, err
		}
		if !ok {
			continue
		}
		if kind == 0 {
			files[file.Path] = file
		} else {
			skipped[file.Path] = kind
		}
	}
	return files, skipped, nil
}

func changedTargetPaths(mutations []replay.FileMutation) []string {
	seen := make(map[string]struct{}, len(mutations))
	var paths []string
	for _, mutation := range mutations {
		switch mutation.Kind {
		case replay.MutationDelete:
			continue
		}
		if strings.TrimSpace(mutation.Path) == "" {
			continue
		}
		if _, ok := seen[mutation.Path]; ok {
			continue
		}
		seen[mutation.Path] = struct{}{}
		paths = append(paths, mutation.Path)
	}
	sort.Strings(paths)
	return paths
}

func parseTreeEntry(line string) (replay.LiveFile, skippedKind, bool, error) {
	head, path, ok := strings.Cut(line, "\t")
	if !ok {
		return replay.LiveFile{}, 0, false, fmt.Errorf("invalid ls-tree line %q", line)
	}
	fields := strings.Fields(head)
	if len(fields) < 4 {
		return replay.LiveFile{}, 0, false, fmt.Errorf("invalid ls-tree metadata %q", head)
	}
	mode, objectType, objectHash, sizeText := fields[0], fields[1], fields[2], fields[3]
	rel := filepath.ToSlash(path)
	if mode == "120000" {
		return replay.LiveFile{Path: rel}, skippedSymlink, true, nil
	}
	if objectType != "blob" || !strings.HasPrefix(mode, "100") {
		return replay.LiveFile{Path: rel}, skippedOther, true, nil
	}
	size, err := strconv.ParseInt(sizeText, 10, 64)
	if err != nil {
		return replay.LiveFile{Path: rel}, skippedOther, true, nil
	}
	return replay.LiveFile{
		Path: rel,
		Mode: mode,
		Size: size,
		Hash: objectHash,
	}, 0, true, nil
}

func enrichMutations(mutations []replay.FileMutation, files []replay.LiveFile) {
	byPath := make(map[string]replay.LiveFile, len(files))
	for _, file := range files {
		byPath[file.Path] = file
	}
	enrichMutationsFromMap(mutations, byPath)
}

func enrichMutationsFromMap(mutations []replay.FileMutation, byPath map[string]replay.LiveFile) {
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
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s failed: %w\n%s", strings.Join(args, " "), err, stderr.String())
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

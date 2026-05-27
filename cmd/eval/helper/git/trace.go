// Package git extracts deterministic commit-mutation traces from Git repos.
package git

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	RepoURL     string
	RepoPath    string
	CacheDir    string
	Ref         string
	Limit       int
	FirstParent bool
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
	return EnsureClone(ctx, s.RepoURL, s.CacheDir)
}

// EnsureClone returns a local clone for repoURL under cacheDir.
func EnsureClone(ctx context.Context, repoURL, cacheDir string) (string, error) {
	if strings.TrimSpace(repoURL) == "" {
		return "", fmt.Errorf("repo URL is required")
	}
	if cacheDir == "" {
		cacheDir = filepath.Join(".eval-cache", "repos")
	}
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(cacheDir, CacheNameForURL(repoURL))
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		if err := verifyCachedOrigin(ctx, path, repoURL); err != nil {
			return "", err
		}
		if err := runGit(ctx, path, "fetch", "--prune", "origin"); err != nil {
			return "", err
		}
		return path, nil
	}
	if err := runGit(ctx, "", "clone", repoURL, path); err != nil {
		return "", err
	}
	return path, nil
}

func (s Source) revList(ctx context.Context, repo, ref string) ([]string, error) {
	args := revListArgs(ref, s.Limit, s.FirstParent)
	out, err := gitOutput(ctx, repo, args...)
	if err != nil {
		return nil, err
	}
	lines := strings.Fields(strings.TrimSpace(out))
	if len(lines) == 0 {
		return nil, fmt.Errorf("no commits found for ref %q", ref)
	}
	return lines, nil
}

func revListArgs(ref string, limit int, firstParent bool) []string {
	args := []string{"rev-list", "--topo-order", "--reverse"}
	if firstParent {
		args = append(args, "--first-parent")
	}
	if limit > 0 {
		args = append(args, "-n", strconv.Itoa(limit))
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
			mutations = append(mutations, replay.FileMutation{
				Kind:    replay.MutationRename,
				OldPath: filepath.ToSlash(fields[1]),
				Path:    filepath.ToSlash(fields[2]),
			})
		default:
			mutations = append(mutations, replay.FileMutation{Kind: replay.MutationModify, Path: filepath.ToSlash(fields[len(fields)-1])})
		}
	}
	return mutations, nil
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
	if repoURL != "" {
		trimmed := strings.TrimSuffix(repoURL, ".git")
		parts := strings.FieldsFunc(trimmed, func(r rune) bool {
			return r == '/' || r == '\\' || r == ':'
		})
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}
	return filepath.Base(path)
}

// CanonicalRepoIDFromURL derives the evaluation repository identity from a Git
// URL. The identity is semantic result metadata, so it uses host plus the full
// namespace path rather than the local cache path or branch/ref name.
func CanonicalRepoIDFromURL(repoURL string) (string, error) {
	host, path := repoIdentityParts(repoURL)
	path = unescapeRepoPath(path)
	path = strings.TrimRight(strings.TrimSpace(path), `/\`)
	if strings.HasSuffix(strings.ToLower(path), ".git") {
		path = path[:len(path)-len(".git")]
	}
	path = strings.ReplaceAll(path, "\\", "/")
	path = strings.Trim(path, "/")
	if path == "" {
		return "", fmt.Errorf("repo URL %q does not contain a repository path", repoURL)
	}
	filtered := nonEmptyPathParts(path)
	if len(filtered) == 0 {
		return "", fmt.Errorf("repo URL %q does not contain a repository path", repoURL)
	}
	if host = strings.ToLower(strings.TrimSpace(host)); host != "" {
		return host + "/" + strings.ToLower(strings.Join(filtered, "/")), nil
	}
	if hostIndex := firstHostPathSegment(filtered); hostIndex >= 0 && hostIndex < len(filtered)-1 {
		return strings.ToLower(strings.Join(filtered[hostIndex:], "/")), nil
	}
	switch {
	case len(filtered) >= 2:
		return strings.ToLower(filtered[len(filtered)-2] + "/" + filtered[len(filtered)-1]), nil
	case len(filtered) == 1:
		return strings.ToLower(filtered[0]), nil
	default:
		return "", fmt.Errorf("repo URL %q does not contain a repository path", repoURL)
	}
}

func nonEmptyPathParts(path string) []string {
	parts := strings.Split(path, "/")
	filtered := parts[:0]
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			filtered = append(filtered, part)
		}
	}
	return filtered
}

func firstHostPathSegment(parts []string) int {
	for i, part := range parts {
		if strings.Contains(part, ".") {
			return i
		}
	}
	return -1
}

func repoIdentityParts(repoURL string) (string, string) {
	trimmed := strings.TrimSpace(repoURL)
	if !strings.Contains(trimmed, "://") && strings.Contains(trimmed, "@") {
		afterUser := strings.SplitN(trimmed, "@", 2)[1]
		if host, path, ok := strings.Cut(afterUser, ":"); ok {
			return host, path
		}
	}
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Scheme != "" {
		path := parsed.Path
		if path == "" && strings.EqualFold(parsed.Scheme, "file") {
			path = parsed.Opaque
		}
		return parsed.Host, path
	}
	return "", trimmed
}

func unescapeRepoPath(path string) string {
	for i := 0; i < 4; i++ {
		unescaped, err := url.PathUnescape(path)
		if err != nil || unescaped == path {
			return path
		}
		path = unescaped
	}
	return path
}

const (
	cacheNameHashLen = 12
	maxCacheNameLen  = 80
)

// CacheNameForURL returns a deterministic cache directory name for a full repo URL.
func CacheNameForURL(repoURL string) string {
	normalized := normalizeRepoURL(repoURL)
	sum := sha256.Sum256([]byte(normalized))
	prefix := sanitizeCachePrefix(normalized)
	if prefix == "" {
		prefix = "repo"
	}
	hash := hex.EncodeToString(sum[:])[:cacheNameHashLen]
	maxPrefixLen := maxCacheNameLen - cacheNameHashLen - len("-")
	if len(prefix) > maxPrefixLen {
		prefix = strings.Trim(prefix[:maxPrefixLen], "-")
		if prefix == "" {
			prefix = "repo"
		}
	}
	return fmt.Sprintf("%s-%s", prefix, hash)
}

func verifyCachedOrigin(ctx context.Context, path, repoURL string) error {
	out, err := gitOutput(ctx, path, "remote", "get-url", "origin")
	if err != nil {
		return fmt.Errorf("verify cached origin for %s: %w", path, err)
	}
	got := normalizeRepoURL(strings.TrimSpace(out))
	want := normalizeRepoURL(repoURL)
	if got != want {
		return fmt.Errorf("cached repo %s origin mismatch: got %q want %q", path, got, want)
	}
	return nil
}

func normalizeRepoURL(repoURL string) string {
	trimmed := strings.TrimSpace(repoURL)
	trimmed = strings.TrimRight(trimmed, "/")
	if strings.HasSuffix(strings.ToLower(trimmed), ".git") {
		trimmed = trimmed[:len(trimmed)-len(".git")]
	}
	if !strings.Contains(trimmed, "://") && strings.Contains(trimmed, "@") {
		afterUser := strings.SplitN(trimmed, "@", 2)[1]
		host, path, ok := strings.Cut(afterUser, ":")
		if ok {
			return strings.ToLower(host) + "/" + strings.Trim(path, "/")
		}
	}
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Host != "" {
		return strings.ToLower(parsed.Host) + "/" + strings.Trim(parsed.Path, "/")
	}
	return trimmed
}

func sanitizeCachePrefix(value string) string {
	lowered := strings.ToLower(value)
	var builder strings.Builder
	lastDash := false
	for _, r := range lowered {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_'
		if ok {
			builder.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			builder.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(builder.String(), "-")
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

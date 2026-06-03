// Package writetrace runs Git write-trace replay as an eval framework suite.
package writetrace

import (
	"encoding/json"
	"fmt"
	"strings"

	gittrace "github.com/dewebprotocol/malt/cmd/eval/helper/git"
	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/suites/configjson"
)

const SuiteName = "write_trace"

// Config controls the write_trace evaluation suite.
type Config struct {
	RepoURLs          []string   `json:"repo_urls,omitempty"`
	MaxCommitsPerRepo int        `json:"max_commits_per_repo,omitempty"`
	StoreMode         string     `json:"store_mode,omitempty"`
	StoreBackend      string     `json:"store_backend,omitempty"`
	Systems           SystemList `json:"systems,omitempty"`
	FirstParent       bool       `json:"first_parent"`
}

// RepositoryTarget is one repository-level replay target resolved from a repo
// URL list. RepoID is result metadata; StoreName is only a local run key.
type RepositoryTarget struct {
	RepoURL string
	RepoID  string
}

// SystemList accepts either a JSON array or a comma-separated JSON string.
type SystemList []string

// DefaultBenchmarkRepos is the default set of Git repositories used for
// write-trace replay when the plan does not specify repo_urls. The selection
// covers a range of languages, repository sizes, and directory structures.
var DefaultBenchmarkRepos = []string{
	"https://github.com/facebook/react.git",
	"https://github.com/vuejs/vue.git",
	"https://github.com/torvalds/linux.git",
	"https://github.com/sveltejs/svelte.git",
	"https://github.com/golang/go.git",
	"https://github.com/rust-lang/rust.git",
	"https://github.com/ipfs/kubo.git",
	"https://github.com/python/cpython.git",
	"https://github.com/microsoft/vscode.git",
	"https://github.com/nodejs/node.git",
}

// DefaultConfig returns framework-managed write-trace replay defaults.
func DefaultConfig() Config {
	return Config{
		RepoURLs:     DefaultBenchmarkRepos,
		StoreMode:    string(evalstore.StoreModeIsolated),
		StoreBackend: string(evalstore.StoreBackendMemory),
		Systems:      SystemList{"maltflat", "merkledag", "hamt"},
		FirstParent:  true,
	}
}

// ParseConfig decodes a framework suite config over write-command defaults.
func ParseConfig(raw json.RawMessage) (Config, error) {
	cfg := DefaultConfig()
	if len(strings.TrimSpace(string(raw))) == 0 {
		return cfg, nil
	}
	if err := configjson.Decode(raw, SuiteName, &cfg); err != nil {
		return Config{}, err
	}
	cfg.Systems = normalizeSystems(cfg.Systems)
	return cfg, nil
}

// SystemsCSV returns the comma-separated system selection expected by evalwrite.
func (c Config) SystemsCSV() string {
	return strings.Join(normalizeSystems(c.Systems), ",")
}

func (c Config) validate() error {
	_, err := c.RepositoryTargets()
	return err
}

// RepositoryTargets returns normalized repository targets from repo_urls.
// If repo_urls is empty, DefaultBenchmarkRepos is used.
func (c Config) RepositoryTargets() ([]RepositoryTarget, error) {
	if c.MaxCommitsPerRepo < 0 {
		return nil, fmt.Errorf("max_commits_per_repo must be non-negative")
	}
	urls := c.RepoURLs
	if len(urls) == 0 {
		urls = DefaultBenchmarkRepos
	}
	repos := make([]RepositoryTarget, 0, len(urls))
	seen := make(map[string]int, len(urls))
	for i, raw := range urls {
		repoURL := strings.TrimSpace(raw)
		if repoURL == "" {
			return nil, fmt.Errorf("repo_urls[%d] must not be empty", i)
		}
		repoID, err := gittrace.CanonicalRepoIDFromURL(repoURL)
		if err != nil {
			return nil, fmt.Errorf("repo_urls[%d]: %w", i, err)
		}
		if previous, ok := seen[repoID]; ok {
			return nil, fmt.Errorf("repo_urls[%d] duplicates repository %q from repo_urls[%d]", i, repoID, previous)
		}
		seen[repoID] = i
		repos = append(repos, RepositoryTarget{RepoURL: repoURL, RepoID: repoID})
	}
	return repos, nil
}

// StoreName returns a stable filesystem-safe name for this repository target.
func (r RepositoryTarget) StoreName(index int) string {
	label := sanitizeName(r.RepoID)
	if label == "" {
		label = "repo"
	}
	return fmt.Sprintf("%03d-%s", index, label)
}

func (s *SystemList) UnmarshalJSON(data []byte) error {
	text := strings.TrimSpace(string(data))
	if text == "null" {
		*s = nil
		return nil
	}
	if strings.HasPrefix(text, "[") {
		var values []string
		if err := json.Unmarshal(data, &values); err != nil {
			return err
		}
		*s = normalizeSystems(values)
		return nil
	}
	var csv string
	if err := json.Unmarshal(data, &csv); err != nil {
		return err
	}
	*s = parseSystemsCSV(csv)
	return nil
}

func parseSystemsCSV(csv string) SystemList {
	return normalizeSystems(strings.Split(csv, ","))
}

func normalizeSystems(values []string) SystemList {
	out := make(SystemList, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func sanitizeName(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-_.")
}

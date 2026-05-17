// Package writetrace runs Git write-trace replay as an eval framework suite.
package writetrace

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
	"github.com/dewebprotocol/malt/internal/eval/suites/configjson"
)

const SuiteName = "write_trace"

// Config controls the write_trace evaluation suite.
type Config struct {
	RepoURL      string             `json:"repo_url,omitempty"`
	RepoPath     string             `json:"repo_path,omitempty"`
	RepoRef      string             `json:"repo_ref,omitempty"`
	CommitLimit  int                `json:"commit_limit,omitempty"`
	CacheDir     string             `json:"cache_dir,omitempty"`
	StoreDir     string             `json:"store_dir,omitempty"`
	StoreMode    string             `json:"store_mode,omitempty"`
	StoreBackend string             `json:"store_backend,omitempty"`
	Systems      SystemList         `json:"systems,omitempty"`
	FirstParent  bool               `json:"first_parent"`
	Repositories []RepositoryConfig `json:"repositories,omitempty"`
}

// RepositoryConfig describes one Git repository replay target after defaults
// have been applied.
type RepositoryConfig struct {
	Name           string `json:"name,omitempty"`
	RepoURL        string `json:"repo_url,omitempty"`
	RepoPath       string `json:"repo_path,omitempty"`
	RepoRef        string `json:"repo_ref,omitempty"`
	CommitLimit    int    `json:"commit_limit,omitempty"`
	commitLimitSet bool
	CacheDir       string `json:"cache_dir,omitempty"`
	FirstParent    bool   `json:"first_parent"`
	firstParentSet bool
}

type repositoryConfigJSON struct {
	Name        string `json:"name,omitempty"`
	RepoURL     string `json:"repo_url,omitempty"`
	RepoPath    string `json:"repo_path,omitempty"`
	RepoRef     string `json:"repo_ref,omitempty"`
	CommitLimit *int   `json:"commit_limit,omitempty"`
	CacheDir    string `json:"cache_dir,omitempty"`
	FirstParent *bool  `json:"first_parent,omitempty"`
}

// SystemList accepts either a JSON array or a comma-separated JSON string.
type SystemList []string

// DefaultConfig returns the same replay defaults used by malt-eval write.
func DefaultConfig() Config {
	return Config{
		RepoRef:      "HEAD",
		CacheDir:     ".eval-cache/repos",
		StoreDir:     ".eval-cache/write-stores",
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
	_, err := c.RepositoriesOrSingle()
	return err
}

// RepositoriesOrSingle returns normalized repositories. It preserves the
// original single-repository fields for compatibility and applies suite-level
// defaults to repositories[] entries.
func (c Config) RepositoriesOrSingle() ([]RepositoryConfig, error) {
	if c.CommitLimit < 0 {
		return nil, fmt.Errorf("commit_limit must be non-negative")
	}
	if len(c.Repositories) > 0 {
		if strings.TrimSpace(c.RepoURL) != "" || strings.TrimSpace(c.RepoPath) != "" {
			return nil, fmt.Errorf("repositories cannot be combined with repo_url or repo_path")
		}
		repos := make([]RepositoryConfig, 0, len(c.Repositories))
		for i, repo := range c.Repositories {
			repo = c.applyRepositoryDefaults(repo)
			if err := repo.validate(i); err != nil {
				return nil, err
			}
			repos = append(repos, repo)
		}
		return repos, nil
	}
	repo := RepositoryConfig{
		RepoURL:     c.RepoURL,
		RepoPath:    c.RepoPath,
		RepoRef:     c.RepoRef,
		CommitLimit: c.CommitLimit,
		CacheDir:    c.CacheDir,
		FirstParent: c.FirstParent,
	}
	repo = c.applyRepositoryDefaults(repo)
	if err := repo.validate(0); err != nil {
		return nil, err
	}
	return []RepositoryConfig{repo}, nil
}

func (c Config) applyRepositoryDefaults(repo RepositoryConfig) RepositoryConfig {
	if strings.TrimSpace(repo.RepoRef) == "" {
		repo.RepoRef = c.RepoRef
	}
	if !repo.commitLimitSet && c.CommitLimit != 0 {
		repo.CommitLimit = c.CommitLimit
	}
	if strings.TrimSpace(repo.CacheDir) == "" {
		repo.CacheDir = c.CacheDir
	}
	if !repo.firstParentSet {
		repo.FirstParent = c.FirstParent
	}
	return repo
}

func (r RepositoryConfig) validate(index int) error {
	if strings.TrimSpace(r.RepoURL) == "" && strings.TrimSpace(r.RepoPath) == "" {
		return fmt.Errorf("repository %d: one of repo_url or repo_path is required", index)
	}
	if r.CommitLimit < 0 {
		return fmt.Errorf("repository %d: commit_limit must be non-negative", index)
	}
	return nil
}

// StoreName returns a stable filesystem-safe name for this repository.
func (r RepositoryConfig) StoreName(index int) string {
	if name := sanitizeName(r.Name); name != "" {
		return name
	}
	if strings.TrimSpace(r.RepoPath) != "" {
		if name := sanitizeName(filepath.Base(r.RepoPath)); name != "" {
			return name
		}
	}
	if strings.TrimSpace(r.RepoURL) != "" {
		trimmed := strings.TrimSuffix(r.RepoURL, ".git")
		parts := strings.FieldsFunc(trimmed, func(r rune) bool {
			return r == '/' || r == '\\' || r == ':'
		})
		if len(parts) > 0 {
			if name := sanitizeName(parts[len(parts)-1]); name != "" {
				return name
			}
		}
	}
	return fmt.Sprintf("repo-%d", index)
}

func (r *RepositoryConfig) UnmarshalJSON(data []byte) error {
	var parsed repositoryConfigJSON
	if err := json.Unmarshal(data, &parsed); err != nil {
		return err
	}
	r.Name = strings.TrimSpace(parsed.Name)
	r.RepoURL = strings.TrimSpace(parsed.RepoURL)
	r.RepoPath = strings.TrimSpace(parsed.RepoPath)
	r.RepoRef = strings.TrimSpace(parsed.RepoRef)
	r.CacheDir = strings.TrimSpace(parsed.CacheDir)
	if parsed.CommitLimit != nil {
		r.CommitLimit = *parsed.CommitLimit
		r.commitLimitSet = true
	}
	if parsed.FirstParent != nil {
		r.FirstParent = *parsed.FirstParent
		r.firstParentSet = true
	}
	return nil
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

// Package writetrace runs Git write-trace replay as an eval framework suite.
package writetrace

import (
	"encoding/json"
	"fmt"
	"strings"

	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
)

const SuiteName = "write_trace"

// Config controls the write_trace evaluation suite.
type Config struct {
	RepoURL      string     `json:"repo_url,omitempty"`
	RepoPath     string     `json:"repo_path,omitempty"`
	RepoRef      string     `json:"repo_ref,omitempty"`
	CommitLimit  int        `json:"commit_limit,omitempty"`
	CacheDir     string     `json:"cache_dir,omitempty"`
	StoreDir     string     `json:"store_dir,omitempty"`
	StoreMode    string     `json:"store_mode,omitempty"`
	StoreBackend string     `json:"store_backend,omitempty"`
	Systems      SystemList `json:"systems,omitempty"`
	FirstParent  bool       `json:"first_parent"`
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
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse write_trace config: %w", err)
	}
	cfg.Systems = normalizeSystems(cfg.Systems)
	return cfg, nil
}

// SystemsCSV returns the comma-separated system selection expected by evalwrite.
func (c Config) SystemsCSV() string {
	return strings.Join(normalizeSystems(c.Systems), ",")
}

func (c Config) validate() error {
	if strings.TrimSpace(c.RepoURL) == "" && strings.TrimSpace(c.RepoPath) == "" {
		return fmt.Errorf("one of repo_url or repo_path is required")
	}
	if c.CommitLimit < 0 {
		return fmt.Errorf("commit_limit must be non-negative")
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

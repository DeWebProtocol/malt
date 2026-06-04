package readquery

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/framework"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/readbench"
	"github.com/dewebprotocol/malt/config"
)

// Name is the fixed evaluation framework suite name.
const Name = "read_query"

// Suite adapts readbench.Runner to the unified evaluation framework.
type Suite struct{}

// Config is the normalized read_query suite configuration.
type Config struct {
	Systems    []readbench.SystemName `json:"systems"`
	Fixture    string                 `json:"fixture"`
	Depth      int                    `json:"depth"`
	SmallBytes int                    `json:"small_bytes"`
	LargeBytes int                    `json:"large_bytes"`
	Range      string                 `json:"range"`
	Iterations int                    `json:"iterations"`
	APIBaseURL string                 `json:"api_base_url"`
	Arcs       map[string]string      `json:"arcs"`
}

type rawConfig struct {
	Systems    json.RawMessage   `json:"systems"`
	Fixture    string            `json:"fixture"`
	Depth      *int              `json:"depth"`
	SmallBytes *int              `json:"small_bytes"`
	LargeBytes *int              `json:"large_bytes"`
	Range      string            `json:"range"`
	Iterations *int              `json:"iterations"`
	APIBaseURL string            `json:"api_base_url"`
	Arcs       map[string]string `json:"arcs"`
}

// Name returns the fixed suite name expected by framework plans.
func (Suite) Name() string {
	return Name
}

// Run executes the read query suite and writes framework-enveloped records.
func (Suite) Run(ctx context.Context, env framework.Env, raw json.RawMessage) error {
	log := env.Log()
	if err := ctx.Err(); err != nil {
		return err
	}
	cfg, err := parseConfig(raw)
	if err != nil {
		return err
	}
	if err := validateMALTFlatArcs(cfg); err != nil {
		return err
	}
	apiBaseURL, err := resolveAPIBaseURL(cfg)
	if err != nil {
		return err
	}

	log("  systems=%v iterations=%d api=%s", cfg.Systems, cfg.Iterations, apiBaseURL)

	var out bytes.Buffer
	runner := readbench.NewRunner(apiBaseURL)
	if err := runner.RunJSONL(ctx, readbench.RunConfig{
		Systems: cfg.Systems,
		Fixture: readbench.FixtureConfig{
			FixtureName:    cfg.Fixture,
			DirectoryDepth: cfg.Depth,
			SmallFileBytes: cfg.SmallBytes,
			LargeFileBytes: cfg.LargeBytes,
			Arcs:           cfg.Arcs,
		},
		RangeHeader: cfg.Range,
		Iterations:  cfg.Iterations,
	}, &out); err != nil {
		return err
	}
	return writeEnvelopedResults(env, out.Bytes())
}

func parseConfig(raw json.RawMessage) (Config, error) {
	cfg := Config{
		Systems:    readbench.DefaultSystems(),
		Depth:      readbench.DefaultDirectoryDepth,
		SmallBytes: readbench.DefaultSmallFileBytes,
		LargeBytes: readbench.DefaultLargeFileBytes,
		Range:      readbench.DefaultRangeHeader,
		Iterations: readbench.DefaultIterations,
	}
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return cfg, nil
	}

	var parsed rawConfig
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&parsed); err != nil {
		return Config{}, fmt.Errorf("decode read_query config: %w", err)
	}

	systems, err := parseSystems(parsed.Systems)
	if err != nil {
		return Config{}, err
	}
	cfg.Systems = systems
	cfg.Fixture = parsed.Fixture
	if parsed.Depth != nil {
		cfg.Depth = *parsed.Depth
	}
	if parsed.SmallBytes != nil {
		cfg.SmallBytes = *parsed.SmallBytes
	}
	if parsed.LargeBytes != nil {
		cfg.LargeBytes = *parsed.LargeBytes
	}
	if strings.TrimSpace(parsed.Range) != "" {
		cfg.Range = parsed.Range
	}
	if parsed.Iterations != nil {
		cfg.Iterations = *parsed.Iterations
	}
	cfg.APIBaseURL = parsed.APIBaseURL
	cfg.Arcs = parsed.Arcs
	return cfg, nil
}

func parseSystems(raw json.RawMessage) ([]readbench.SystemName, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return readbench.DefaultSystems(), nil
	}

	var csv string
	if len(trimmed) > 0 && trimmed[0] == '"' {
		if err := json.Unmarshal(trimmed, &csv); err != nil {
			return nil, fmt.Errorf("decode systems: %w", err)
		}
		return readbench.ParseSystemsCSV(csv)
	}

	var names []string
	if err := json.Unmarshal(trimmed, &names); err != nil {
		return nil, fmt.Errorf("decode systems: expected string or string array: %w", err)
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no systems selected")
	}
	return readbench.ParseSystemsCSV(strings.Join(names, ","))
}

func validateMALTFlatArcs(cfg Config) error {
	if !systemsInclude(cfg.Systems, readbench.SystemMALTFlat) {
		return nil
	}
	if strings.TrimSpace(cfg.Arcs["@payload"]) == "" {
		return fmt.Errorf(`read_query requires arcs["@payload"] when maltflat is selected`)
	}
	return nil
}

func resolveAPIBaseURL(cfg Config) (string, error) {
	if !systemsInclude(cfg.Systems, readbench.SystemMALTFlat) {
		return cfg.APIBaseURL, nil
	}
	if strings.TrimSpace(cfg.APIBaseURL) != "" {
		return cfg.APIBaseURL, nil
	}
	cfgFile, err := config.Load()
	if err != nil {
		return "", fmt.Errorf("load default config for read_query API base URL: %w", err)
	}
	return cfgFile.APIBaseURL(), nil
}

func systemsInclude(systems []readbench.SystemName, target readbench.SystemName) bool {
	for _, system := range systems {
		if system == target {
			return true
		}
	}
	return false
}

func writeEnvelopedResults(env framework.Env, data []byte) error {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		var result readbench.Result
		if err := json.Unmarshal(scanner.Bytes(), &result); err != nil {
			return fmt.Errorf("decode readbench JSONL result: %w", err)
		}
		if err := env.WriteRecord(Name, result); err != nil {
			return fmt.Errorf("write read_query record: %w", err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan readbench JSONL results: %w", err)
	}
	return nil
}

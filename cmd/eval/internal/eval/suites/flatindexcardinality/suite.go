package flatindexcardinality

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/framework"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/readbench"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/suites/configjson"
)

// Name is the fixed evaluation framework suite name.
const Name = "flat_index_cardinality"

// Suite adapts the flat full-path index cardinality benchmark to the unified
// evaluation framework.
type Suite struct{}

// Config controls the flat_index_cardinality suite.
type Config struct {
	Systems          []string `json:"systems"`
	Dataset          string   `json:"dataset"`
	KeyCounts        []int    `json:"key_counts"`
	PathDepth        int      `json:"path_depth"`
	PathsPerKeyCount int      `json:"paths_per_key_count"`
	CASLatencyMS     []int    `json:"cas_latency_ms"`
	SmallBytes       int      `json:"small_bytes"`
	Iterations       int      `json:"iterations"`
}

// Name returns the fixed suite name expected by framework plans.
func (Suite) Name() string {
	return Name
}

// Run executes the flat index cardinality suite and writes framework-enveloped
// raw records plus a figure-facing aggregate CSV.
func (Suite) Run(ctx context.Context, env framework.Env, raw json.RawMessage) error {
	log := env.Log()
	cfg, err := parseConfig(raw)
	if err != nil {
		return err
	}
	systems, err := parseSystems(cfg.Systems)
	if err != nil {
		return err
	}

	total := 0
	for _, keyCount := range cfg.KeyCounts {
		total += cfg.Iterations * len(cfg.CASLatencyMS) * len(systems) * measuredPathCount(keyCount, cfg.PathsPerKeyCount)
	}
	count := 0
	log("  systems=%v key_counts=%v path_depth=%d cas_latency_ms=%v paths_per_key_count=%d iterations=%d",
		systems, cfg.KeyCounts, cfg.PathDepth, cfg.CASLatencyMS, cfg.PathsPerKeyCount, cfg.Iterations)

	var results []readbench.Result
	for _, keyCount := range cfg.KeyCounts {
		dataset, err := readbench.NewMatrixDataset(readbench.MatrixDatasetConfig{
			Name:          fmt.Sprintf("%s-n%d", cfg.Dataset, keyCount),
			FileCount:     keyCount,
			Depths:        []int{cfg.PathDepth},
			PayloadBytes:  cfg.SmallBytes,
			PathsPerDepth: measuredPathCount(keyCount, cfg.PathsPerKeyCount),
		})
		if err != nil {
			return err
		}
		for _, latencyMS := range cfg.CASLatencyMS {
			materialized := make([]readbench.MatrixSystem, 0, len(systems))
			for _, system := range systems {
				benchSystem, err := readbench.NewMatrixSystem(ctx, system, dataset, latencyMS)
				if err != nil {
					closeMatrixSystems(materialized)
					return err
				}
				materialized = append(materialized, benchSystem)
			}
			if err := runDataset(ctx, env, cfg, dataset, materialized, total, &count, &results); err != nil {
				closeMatrixSystems(materialized)
				return err
			}
			if err := closeMatrixSystems(materialized); err != nil {
				return err
			}
		}
	}
	return writeAggregateCSV(env, Name, aggregateResults(results))
}

func runDataset(ctx context.Context, env framework.Env, cfg Config, dataset *readbench.MatrixDataset, systems []readbench.MatrixSystem, total int, count *int, results *[]readbench.Result) error {
	log := env.Log()
	ops, err := readbench.MatrixOperations(dataset, cfg.PathDepth)
	if err != nil {
		return err
	}
	for iteration := 0; iteration < cfg.Iterations; iteration++ {
		for _, system := range systems {
			for _, op := range ops {
				result, err := system.Measure(ctx, iteration, dataset, op)
				if err != nil {
					return err
				}
				if prover, ok := system.(readbench.MatrixProveSystem); ok {
					proveElapsedNS, err := prover.MeasureProve(ctx, iteration, dataset, op)
					if err != nil {
						return err
					}
					result.ProveElapsedNS = proveElapsedNS
				}
				if err := env.WriteRecord(Name, result); err != nil {
					return err
				}
				if results != nil {
					*results = append(*results, *result)
				}
				*count = *count + 1
				if *count%25 == 0 || *count == total {
					log("  [%d/%d] dataset=%s iter=%d system=%s file_count=%d cas_latency_ms=%d elapsed=%s",
						*count, total, dataset.Name, iteration, system.Name(), dataset.FileCount, result.CASLatencyMS,
						time.Duration(result.ElapsedNS).Round(time.Microsecond))
				}
			}
		}
	}
	return nil
}

func parseConfig(raw json.RawMessage) (Config, error) {
	cfg := Config{
		Systems:          []string{"maltflat", "flathamt"},
		Dataset:          "flat-index-cardinality",
		KeyCounts:        []int{1, 64, 256, 1024, 4096, 16384},
		PathDepth:        4,
		PathsPerKeyCount: 5,
		CASLatencyMS:     []int{0, 25, 200},
		SmallBytes:       1024,
		Iterations:       5,
	}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := configjson.Decode(raw, Name, &cfg); err != nil {
			return Config{}, err
		}
	}
	if strings.TrimSpace(cfg.Dataset) == "" {
		return Config{}, fmt.Errorf("dataset must not be empty")
	}
	keyCounts, err := normalizeKeyCounts(cfg.KeyCounts)
	if err != nil {
		return Config{}, err
	}
	cfg.KeyCounts = keyCounts
	if cfg.PathDepth < 1 {
		return Config{}, fmt.Errorf("path_depth must be >= 1")
	}
	if cfg.PathsPerKeyCount <= 0 {
		return Config{}, fmt.Errorf("paths_per_key_count must be positive")
	}
	if len(cfg.CASLatencyMS) == 0 {
		return Config{}, fmt.Errorf("cas_latency_ms must not be empty")
	}
	for _, latencyMS := range cfg.CASLatencyMS {
		if latencyMS < 0 {
			return Config{}, fmt.Errorf("cas_latency_ms values must be non-negative")
		}
	}
	if cfg.SmallBytes <= 0 {
		return Config{}, fmt.Errorf("small_bytes must be positive")
	}
	if cfg.Iterations < 0 {
		return Config{}, fmt.Errorf("iterations must be non-negative")
	}
	return cfg, nil
}

func parseSystems(values []string) ([]readbench.SystemName, error) {
	if len(values) == 0 {
		return []readbench.SystemName{readbench.SystemMALTFlat, readbench.SystemFlatHAMT}, nil
	}
	systems, err := readbench.ParseSystemsCSV(strings.Join(values, ","))
	if err != nil {
		return nil, err
	}
	for _, system := range systems {
		switch system {
		case readbench.SystemMALTFlat, readbench.SystemFlatHAMT:
		default:
			return nil, fmt.Errorf("system %q is not a flat-index baseline", system)
		}
	}
	return systems, nil
}

func normalizeKeyCounts(keyCounts []int) ([]int, error) {
	if len(keyCounts) == 0 {
		return nil, fmt.Errorf("key_counts must not be empty")
	}
	out := append([]int(nil), keyCounts...)
	slices.Sort(out)
	out = slices.Compact(out)
	for _, keyCount := range out {
		if keyCount < 1 {
			return nil, fmt.Errorf("key_counts values must be positive")
		}
	}
	return out, nil
}

func measuredPathCount(keyCount int, pathsPerKeyCount int) int {
	if keyCount < pathsPerKeyCount {
		return keyCount
	}
	return pathsPerKeyCount
}

func closeMatrixSystems(systems []readbench.MatrixSystem) error {
	var firstErr error
	for _, system := range systems {
		if system == nil {
			continue
		}
		if err := system.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

var _ framework.Suite = Suite{}

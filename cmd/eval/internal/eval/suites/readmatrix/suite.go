package readmatrix

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/framework"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/readbench"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/suites/configjson"
)

// Name is the fixed evaluation framework suite name.
const Name = "read_matrix"

// Suite adapts the fair core read matrix to the unified evaluation framework.
type Suite struct{}

// Config controls the read_matrix suite.
type Config struct {
	Systems       []string `json:"systems"`
	Dataset       string   `json:"dataset"`
	Depths        []int    `json:"depths"`
	CASLatencyMS  []int    `json:"cas_latency_ms"`
	SmallBytes    int      `json:"small_bytes"`
	PathsPerDepth int      `json:"paths_per_depth"`
	Iterations    int      `json:"iterations"`
}

// Name returns the fixed suite name expected by framework plans.
func (Suite) Name() string {
	return Name
}

// Run executes the read matrix suite and writes framework-enveloped records.
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

	total := cfg.Iterations * len(cfg.CASLatencyMS) * len(systems) * len(cfg.Depths) * cfg.PathsPerDepth
	count := 0
	log("  systems=%v depths=%v cas_latency_ms=%v paths_per_depth=%d iterations=%d",
		systems, cfg.Depths, cfg.CASLatencyMS, cfg.PathsPerDepth, cfg.Iterations)

	dataset, err := readbench.NewMatrixDataset(readbench.MatrixDatasetConfig{
		Name:          cfg.Dataset,
		Depths:        cfg.Depths,
		PayloadBytes:  cfg.SmallBytes,
		PathsPerDepth: cfg.PathsPerDepth,
	})
	if err != nil {
		return err
	}
	var results []readbench.Result
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
	return writeAggregateCSV(env, Name, aggregateResults(results))
}

func runDataset(ctx context.Context, env framework.Env, cfg Config, dataset *readbench.MatrixDataset, systems []readbench.MatrixSystem, total int, count *int, results *[]readbench.Result) error {
	log := env.Log()
	for iteration := 0; iteration < cfg.Iterations; iteration++ {
		for _, system := range systems {
			for _, depth := range cfg.Depths {
				ops, err := readbench.MatrixOperations(dataset, depth)
				if err != nil {
					return err
				}
				for _, op := range ops {
					result, err := system.Measure(ctx, iteration, dataset, op)
					if err != nil {
						return err
					}
					if err := env.WriteRecord(Name, result); err != nil {
						return err
					}
					if results != nil {
						*results = append(*results, *result)
					}
					*count = *count + 1
					if *count%25 == 0 || *count == total {
						log("  [%d/%d] dataset=%s iter=%d system=%s depth=%d cas_latency_ms=%d elapsed=%s",
							*count, total, dataset.Name, iteration, system.Name(), depth, result.CASLatencyMS,
							time.Duration(result.ElapsedNS).Round(time.Microsecond))
					}
				}
			}
		}
	}
	return nil
}

func parseConfig(raw json.RawMessage) (Config, error) {
	cfg := Config{
		Systems:       []string{"maltflat", "merkledag", "hamt", "flathamt"},
		Dataset:       "read-matrix",
		Depths:        []int{1, 2, 3, 4, 5, 6},
		CASLatencyMS:  []int{0, 25, 50, 100, 200},
		SmallBytes:    1024,
		PathsPerDepth: 5,
		Iterations:    5,
	}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := configjson.Decode(raw, Name, &cfg); err != nil {
			return Config{}, err
		}
	}
	if strings.TrimSpace(cfg.Dataset) == "" {
		return Config{}, fmt.Errorf("dataset must not be empty")
	}
	if len(cfg.Depths) == 0 {
		return Config{}, fmt.Errorf("depths must not be empty")
	}
	for _, depth := range cfg.Depths {
		if depth < 1 {
			return Config{}, fmt.Errorf("depths must be >= 1")
		}
	}
	if cfg.SmallBytes <= 0 {
		return Config{}, fmt.Errorf("small_bytes must be positive")
	}
	if cfg.PathsPerDepth <= 0 {
		return Config{}, fmt.Errorf("paths_per_depth must be positive")
	}
	if len(cfg.CASLatencyMS) == 0 {
		return Config{}, fmt.Errorf("cas_latency_ms must not be empty")
	}
	for _, latencyMS := range cfg.CASLatencyMS {
		if latencyMS < 0 {
			return Config{}, fmt.Errorf("cas_latency_ms values must be non-negative")
		}
	}
	if cfg.Iterations < 0 {
		return Config{}, fmt.Errorf("iterations must be non-negative")
	}
	return cfg, nil
}

func parseSystems(values []string) ([]readbench.SystemName, error) {
	if len(values) == 0 {
		return []readbench.SystemName{readbench.SystemMALTFlat, readbench.SystemMerkleDAG, readbench.SystemHAMT, readbench.SystemFlatHAMT}, nil
	}
	return readbench.ParseSystemsCSV(strings.Join(values, ","))
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

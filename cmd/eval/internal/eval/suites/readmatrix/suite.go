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
	Systems    []string `json:"systems"`
	Dataset    string   `json:"dataset"`
	FileCounts []int    `json:"file_counts"`
	Depths     []int    `json:"depths"`
	SmallBytes int      `json:"small_bytes"`
	LargeBytes int      `json:"large_bytes"`
	Range      string   `json:"range"`
	Iterations int      `json:"iterations"`
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

	total := cfg.Iterations * len(cfg.FileCounts) * len(systems) * len(cfg.Depths) * 3
	count := 0
	log("  systems=%v file_counts=%v depths=%v iterations=%d", systems, cfg.FileCounts, cfg.Depths, cfg.Iterations)

	for _, fileCount := range cfg.FileCounts {
		dataset, err := readbench.NewMatrixDataset(readbench.MatrixDatasetConfig{
			Name:       cfg.Dataset,
			FileCount:  fileCount,
			Depths:     cfg.Depths,
			SmallBytes: cfg.SmallBytes,
			LargeBytes: cfg.LargeBytes,
		})
		if err != nil {
			return err
		}

		materialized := make([]readbench.MatrixSystem, 0, len(systems))
		for _, system := range systems {
			benchSystem, err := readbench.NewMatrixSystem(ctx, system, dataset)
			if err != nil {
				closeMatrixSystems(materialized)
				return err
			}
			materialized = append(materialized, benchSystem)
		}

		if err := runDataset(ctx, env, cfg, dataset, materialized, total, &count); err != nil {
			closeMatrixSystems(materialized)
			return err
		}
		if err := closeMatrixSystems(materialized); err != nil {
			return err
		}
	}
	return nil
}

func runDataset(ctx context.Context, env framework.Env, cfg Config, dataset *readbench.MatrixDataset, systems []readbench.MatrixSystem, total int, count *int) error {
	log := env.Log()
	for iteration := 0; iteration < cfg.Iterations; iteration++ {
		for _, system := range systems {
			for _, depth := range cfg.Depths {
				ops, err := readbench.MatrixOperations(dataset, depth, cfg.Range)
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
					*count = *count + 1
					if *count%25 == 0 || *count == total {
						log("  [%d/%d] dataset=%s iter=%d system=%s depth=%d workload=%s elapsed=%s",
							*count, total, dataset.Name, iteration, system.Name(), depth, op.Workload,
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
		Systems:    []string{"maltflat", "merkledag", "hamt"},
		Dataset:    "read-matrix",
		FileCounts: []int{32, 128},
		Depths:     []int{2, 4, 8},
		SmallBytes: 1024,
		LargeBytes: readbench.DefaultLargeFileBytes,
		Range:      readbench.DefaultRangeHeader,
		Iterations: 5,
	}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := configjson.Decode(raw, Name, &cfg); err != nil {
			return Config{}, err
		}
	}
	if strings.TrimSpace(cfg.Dataset) == "" {
		return Config{}, fmt.Errorf("dataset must not be empty")
	}
	if len(cfg.FileCounts) == 0 {
		return Config{}, fmt.Errorf("file_counts must not be empty")
	}
	for _, fileCount := range cfg.FileCounts {
		if fileCount <= 0 {
			return Config{}, fmt.Errorf("file_counts must be positive")
		}
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
	if cfg.LargeBytes <= 0 {
		return Config{}, fmt.Errorf("large_bytes must be positive")
	}
	if cfg.Iterations < 0 {
		return Config{}, fmt.Errorf("iterations must be non-negative")
	}
	if strings.TrimSpace(cfg.Range) == "" {
		cfg.Range = readbench.DefaultRangeHeader
	}
	return cfg, nil
}

func parseSystems(values []string) ([]readbench.SystemName, error) {
	if len(values) == 0 {
		return readbench.DefaultSystems(), nil
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

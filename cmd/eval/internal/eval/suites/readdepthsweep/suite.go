// Package readdepthsweep measures resolve latency across different directory
// depths. The CAS latency is fixed (daemon config or zero for local baselines).
// Output: one JSONL record per system × depth × iteration.
package readdepthsweep

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dewebprotocol/malt/cmd/eval/helper/evalcas"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/framework"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/readbench"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/suites/configjson"
)

const suiteName = "read_depth_sweep"

// Suite implements the depth-sweep read latency evaluation.
type Suite struct{}

func (Suite) Name() string { return suiteName }

// Config controls the read_depth_sweep suite.
type Config struct {
	Systems    []string `json:"systems"`
	Depths     []int    `json:"depths"`
	SmallBytes int      `json:"small_bytes"`
	LargeBytes int      `json:"large_bytes"`
	Iterations int      `json:"iterations"`
	APIBaseURL string   `json:"api_base_url"`
}

func (Suite) Run(ctx context.Context, env framework.Env, raw json.RawMessage) error {
	cfg := Config{
		Systems:    []string{"maltflat", "merkledag", "hamt"},
		Depths:     []int{3, 4, 5, 6},
		SmallBytes: 256,
		LargeBytes: 1024*1024 + 1,
		Iterations: 10,
	}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := configjson.Decode(raw, suiteName, &cfg); err != nil {
			return err
		}
	}
	if len(cfg.Depths) == 0 {
		return fmt.Errorf("depths must not be empty")
	}
	maxDepth := 0
	for _, d := range cfg.Depths {
		if d > maxDepth {
			maxDepth = d
		}
	}

	multiFix := readbench.NewMultiDepthFixture(maxDepth, cfg.SmallBytes, cfg.LargeBytes)

	// Determine systems.
	systemSet := make(map[string]bool)
	for _, s := range cfg.Systems {
		systemSet[strings.TrimSpace(s)] = true
	}

	// Build MALT system (local, no daemon).
	var maltSys *readbench.LocalMALTSystem
	if systemSet["maltflat"] {
		store := evalcas.NewNoLatency()
		sys, err := readbench.NewLocalMALTSystem(ctx, store, multiFix)
		if err != nil {
			return fmt.Errorf("create local malt system: %w", err)
		}
		maltSys = sys
	}

	// Build baseline systems (merkledag, hamt) with the same no-latency CAS.
	baselines := make(map[string]*readbench.BaselineSystem)
	for _, name := range []string{"merkledag", "hamt"} {
		if !systemSet[name] {
			continue
		}
		store := evalcas.NewNoLatency()
		bs, err := readbench.NewBaselineSystemWithCAS(ctx, readbench.SystemName(name), multiFix, store)
		if err != nil {
			return fmt.Errorf("create baseline %s: %w", name, err)
		}
		baselines[name] = bs
	}

	// Measure: for each iteration × system × depth, emit one record.
	for iter := 0; iter < cfg.Iterations; iter++ {
		for _, sysName := range cfg.Systems {
			sysName = strings.TrimSpace(sysName)
			for _, depth := range cfg.Depths {
				if depth < 1 || depth > maxDepth {
					continue
				}
				fix := multiFix.Fixtures[depth-1]

				var result *readbench.Result
				var err error
				switch sysName {
				case "maltflat":
					result, err = maltSys.MeasureResolve(ctx, iter, "depth-sweep", fix.SmallPath)
				case "merkledag", "hamt":
					result, err = baselines[sysName].MeasureResolve(ctx, iter, "depth-sweep", fix.SmallPath)
				default:
					return fmt.Errorf("unknown system %q", sysName)
				}
				if err != nil {
					return err
				}
				if err := env.WriteRecord(suiteName, result); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

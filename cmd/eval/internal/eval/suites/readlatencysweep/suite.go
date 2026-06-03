// Package readlatencysweep measures resolve latency across different CAS
// latency values. The directory depth is fixed. Each CAS latency value
// creates a fresh mock CAS and rebuilds all systems.
// Output: one JSONL record per system × cas_latency × iteration.
package readlatencysweep

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/dewebprotocol/malt/cmd/eval/helper/evalcas"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/framework"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/readbench"
	"github.com/dewebprotocol/malt/cmd/eval/internal/eval/suites/configjson"
)

const suiteName = "read_latency_sweep"

// Suite implements the CAS-latency-sweep read latency evaluation.
type Suite struct{}

func (Suite) Name() string { return suiteName }

// Config controls the read_latency_sweep suite.
type Config struct {
	Systems      []string `json:"systems"`
	Depth        int      `json:"depth"`
	CASLatencyMS []int    `json:"cas_latency_ms"`
	SmallBytes   int      `json:"small_bytes"`
	LargeBytes   int      `json:"large_bytes"`
	Iterations   int      `json:"iterations"`
}

func (Suite) Run(ctx context.Context, env framework.Env, raw json.RawMessage) error {
	cfg := Config{
		Systems:      []string{"maltflat", "merkledag", "hamt"},
		Depth:        6,
		CASLatencyMS: []int{0, 5, 10, 20, 50, 100},
		SmallBytes:   256,
		LargeBytes:   1024*1024 + 1,
		Iterations:   10,
	}
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := configjson.Decode(raw, suiteName, &cfg); err != nil {
			return err
		}
	}
	if len(cfg.CASLatencyMS) == 0 {
		return fmt.Errorf("cas_latency_ms must not be empty")
	}
	if cfg.Depth < 1 {
		return fmt.Errorf("depth must be >= 1")
	}

	multiFix := readbench.NewMultiDepthFixture(cfg.Depth, cfg.SmallBytes, cfg.LargeBytes)

	systemSet := make(map[string]bool)
	for _, s := range cfg.Systems {
		systemSet[strings.TrimSpace(s)] = true
	}

	// For each CAS latency, create fresh systems and measure.
	for _, latencyMS := range cfg.CASLatencyMS {
		latency := time.Duration(latencyMS) * time.Millisecond

		// Create MALT system with this latency.
		var maltSys *readbench.LocalMALTSystem
		if systemSet["maltflat"] {
			cas := evalcas.NewWithLatency(latency)
			sys, err := readbench.NewLocalMALTSystem(ctx, cas, multiFix)
			if err != nil {
				return fmt.Errorf("create local malt system (latency=%dms): %w", latencyMS, err)
			}
			maltSys = sys
		}

		// Create baseline systems with this latency.
		baselines := make(map[string]*readbench.BaselineSystem)
		for _, name := range []string{"merkledag", "hamt"} {
			if !systemSet[name] {
				continue
			}
			cas := evalcas.NewWithLatency(latency)
			bs, err := readbench.NewBaselineSystemWithCAS(ctx, readbench.SystemName(name), multiFix, cas)
			if err != nil {
				return fmt.Errorf("create baseline %s (latency=%dms): %w", name, latencyMS, err)
			}
			baselines[name] = bs
		}

		// Measure at the configured depth.
		fix := multiFix.Fixtures[cfg.Depth-1]
		for iter := 0; iter < cfg.Iterations; iter++ {
			for _, sysName := range cfg.Systems {
				sysName = strings.TrimSpace(sysName)
				var result *readbench.Result
				var err error
				switch sysName {
				case "maltflat":
					result, err = maltSys.MeasureResolve(ctx, iter, "latency-sweep", fix.SmallPath)
				case "merkledag", "hamt":
					result, err = baselines[sysName].MeasureResolve(ctx, iter, "latency-sweep", fix.SmallPath)
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

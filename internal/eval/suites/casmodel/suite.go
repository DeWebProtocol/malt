// Package casmodel measures the local CAS mock under fixed latency models.
package casmodel

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dewebprotocol/malt/core/cas"
	casmock "github.com/dewebprotocol/malt/core/cas/mock"
	"github.com/dewebprotocol/malt/core/metrics"
	"github.com/dewebprotocol/malt/internal/eval/framework"
	"github.com/dewebprotocol/malt/internal/eval/suites/configjson"
	cid "github.com/ipfs/go-cid"
)

const suiteName = "cas_model"

// Suite implements the CAS cost model evaluation.
type Suite struct{}

// Config controls CAS latency and dependency-shape dimensions.
type Config struct {
	GetLatencyMS int   `json:"get_latency_ms"`
	PutLatencyMS int   `json:"put_latency_ms"`
	HasLatencyMS int   `json:"has_latency_ms"`
	JitterMS     int   `json:"jitter_ms"`
	ChainLengths []int `json:"chain_lengths"`
	BatchSizes   []int `json:"batch_sizes"`
	Iterations   int   `json:"iterations"`
}

// Result is one measured CAS operation record.
type Result struct {
	Operation           string           `json:"operation"`
	DependencyShape     string           `json:"dependency_shape"`
	Size                int              `json:"size"`
	Iteration           int              `json:"iteration"`
	ElapsedNS           int64            `json:"elapsed_ns"`
	ConfiguredLatencyMS int              `json:"configured_latency_ms"`
	ConfiguredJitterMS  int              `json:"configured_jitter_ms"`
	CAS                 metrics.CASStats `json:"cas"`
}

// Name returns the stable suite name.
func (Suite) Name() string {
	return suiteName
}

// Run executes the configured CAS model matrix and writes raw records.
func (Suite) Run(ctx context.Context, env framework.Env, raw json.RawMessage) error {
	cfg, err := parseConfig(raw)
	if err != nil {
		return err
	}

	for iteration := 0; iteration < cfg.Iterations; iteration++ {
		for _, size := range cfg.ChainLengths {
			for _, op := range []string{"put", "get", "has"} {
				record, err := measure(ctx, cfg, op, "chain", size, iteration)
				if err != nil {
					return err
				}
				if err := env.WriteRecord(suiteName, record); err != nil {
					return err
				}
			}
		}
		for _, size := range cfg.BatchSizes {
			for _, op := range []string{"put", "get", "has"} {
				record, err := measure(ctx, cfg, op, "batch", size, iteration)
				if err != nil {
					return err
				}
				if err := env.WriteRecord(suiteName, record); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func parseConfig(raw json.RawMessage) (Config, error) {
	cfg := Config{
		ChainLengths: []int{1},
		BatchSizes:   []int{1},
		Iterations:   1,
	}
	if len(raw) != 0 {
		if err := configjson.Decode(raw, suiteName, &cfg); err != nil {
			return Config{}, err
		}
	}
	if cfg.ChainLengths == nil {
		cfg.ChainLengths = []int{1}
	}
	if cfg.BatchSizes == nil {
		cfg.BatchSizes = []int{1}
	}
	if cfg.Iterations == 0 {
		cfg.Iterations = 1
	}
	if cfg.GetLatencyMS < 0 || cfg.PutLatencyMS < 0 || cfg.HasLatencyMS < 0 || cfg.JitterMS < 0 {
		return Config{}, fmt.Errorf("latency and jitter values must be non-negative")
	}
	if cfg.Iterations < 0 {
		return Config{}, fmt.Errorf("iterations must be non-negative")
	}
	for _, size := range append(append([]int(nil), cfg.ChainLengths...), cfg.BatchSizes...) {
		if size <= 0 {
			return Config{}, fmt.Errorf("dependency sizes must be positive")
		}
	}
	return cfg, nil
}

func measure(ctx context.Context, cfg Config, operation, shape string, size, iteration int) (Result, error) {
	store := casmock.NewCAS(
		casmock.WithGetLatency(ms(cfg.GetLatencyMS)),
		casmock.WithPutLatency(ms(cfg.PutLatencyMS)),
		casmock.WithHasLatency(ms(cfg.HasLatencyMS)),
		casmock.WithJitter(ms(cfg.JitterMS)),
	)

	payloads := payloadsFor(operation, shape, size, iteration)
	cids, err := seedForRead(ctx, store, operation, payloads)
	if err != nil {
		return Result{}, err
	}
	store.ResetStats()

	start := time.Now()
	switch operation {
	case "put":
		if shape == "batch" {
			blocks := make([]cas.Block, len(payloads))
			for i, payload := range payloads {
				blocks[i] = cas.Block{Data: payload}
			}
			if _, err := store.PutBatch(ctx, blocks); err != nil {
				return Result{}, fmt.Errorf("put batch: %w", err)
			}
			break
		}
		for _, payload := range payloads {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			if _, err := store.Put(ctx, payload); err != nil {
				return Result{}, fmt.Errorf("put chain: %w", err)
			}
		}
	case "get":
		for _, block := range cids {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			if _, err := store.Get(ctx, block); err != nil {
				return Result{}, fmt.Errorf("get %s: %w", shape, err)
			}
		}
	case "has":
		if shape == "batch" {
			if _, err := store.HasBatch(ctx, cids); err != nil {
				return Result{}, fmt.Errorf("has batch: %w", err)
			}
			break
		}
		for _, block := range cids {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			if _, err := store.Has(ctx, block); err != nil {
				return Result{}, fmt.Errorf("has chain: %w", err)
			}
		}
	default:
		return Result{}, fmt.Errorf("unsupported operation %q", operation)
	}
	elapsed := time.Since(start).Nanoseconds()

	return Result{
		Operation:           operation,
		DependencyShape:     shape,
		Size:                size,
		Iteration:           iteration,
		ElapsedNS:           elapsed,
		ConfiguredLatencyMS: configuredLatency(cfg, operation),
		ConfiguredJitterMS:  cfg.JitterMS,
		CAS:                 store.SnapshotStats(),
	}, nil
}

func seedForRead(ctx context.Context, store *casmock.CAS, operation string, payloads [][]byte) ([]cid.Cid, error) {
	if operation == "put" {
		return nil, nil
	}
	cids := make([]cid.Cid, len(payloads))
	for i, payload := range payloads {
		blockCID, err := cas.CIDForBlock(cas.Block{Data: payload})
		if err != nil {
			return nil, err
		}
		store.AddBlock(blockCID, payload)
		cids[i] = blockCID
	}
	return cids, ctx.Err()
}

func payloadsFor(operation, shape string, size, iteration int) [][]byte {
	payloads := make([][]byte, size)
	for i := range payloads {
		payloads[i] = []byte(fmt.Sprintf("cas-model:%s:%s:%d:%d", operation, shape, iteration, i))
	}
	return payloads
}

func configuredLatency(cfg Config, operation string) int {
	switch operation {
	case "get":
		return cfg.GetLatencyMS
	case "put":
		return cfg.PutLatencyMS
	case "has":
		return cfg.HasLatencyMS
	default:
		return 0
	}
}

func ms(value int) time.Duration {
	return time.Duration(value) * time.Millisecond
}

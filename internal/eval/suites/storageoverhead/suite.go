// Package storageoverhead measures persisted and logical storage overhead.
package storageoverhead

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	evalstore "github.com/dewebprotocol/malt/cmd/eval/helper/store"
	"github.com/dewebprotocol/malt/core/arctable/overwrite"
	"github.com/dewebprotocol/malt/core/commitment/ipa"
	"github.com/dewebprotocol/malt/core/structure"
	"github.com/dewebprotocol/malt/core/structure/list"
	listtree "github.com/dewebprotocol/malt/core/structure/list/tree"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	"github.com/dewebprotocol/malt/core/structure/mapping/indexed"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/internal/eval/framework"
	cid "github.com/ipfs/go-cid"
)

const suiteName = "storage_overhead"

// Suite implements the storage overhead evaluation.
type Suite struct{}

// Config controls storage-overhead dimensions.
type Config struct {
	Structures   []string `json:"structures"`
	Sizes        []int    `json:"sizes"`
	PayloadBytes int      `json:"payload_bytes"`
}

// Result is one storage overhead record.
type Result struct {
	Structure           string             `json:"structure"`
	Size                int                `json:"size"`
	PayloadBytes        int                `json:"payload_bytes"`
	Method              string             `json:"method"`
	Accounting          evalstore.Snapshot `json:"accounting"`
	PersistedBytes      uint64             `json:"persisted_bytes"`
	LogicalPayloadBytes uint64             `json:"logical_payload_bytes"`
	LogicalProofBytes   uint64             `json:"logical_proof_bytes"`
	Error               string             `json:"error,omitempty"`
}

// Name returns the stable suite name.
func (Suite) Name() string {
	return suiteName
}

// Run executes the configured storage overhead matrix.
func (Suite) Run(ctx context.Context, env framework.Env, raw json.RawMessage) error {
	cfg, err := parseConfig(raw)
	if err != nil {
		return err
	}

	for _, structureName := range cfg.Structures {
		for _, size := range cfg.Sizes {
			record, err := measure(ctx, structureName, size, cfg.PayloadBytes)
			if err != nil {
				return err
			}
			if err := env.WriteRecord(suiteName, record); err != nil {
				return err
			}
		}
	}
	return nil
}

func parseConfig(raw json.RawMessage) (Config, error) {
	cfg := Config{
		Structures:   []string{"map", "list"},
		Sizes:        []int{1},
		PayloadBytes: 64,
	}
	if len(raw) != 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return Config{}, fmt.Errorf("parse storage_overhead config: %w", err)
		}
	}
	if cfg.Structures == nil {
		cfg.Structures = []string{"map", "list"}
	}
	if cfg.Sizes == nil {
		cfg.Sizes = []int{1}
	}
	if cfg.PayloadBytes <= 0 {
		return Config{}, fmt.Errorf("payload_bytes must be positive")
	}
	for _, size := range cfg.Sizes {
		if size <= 0 {
			return Config{}, fmt.Errorf("sizes must be positive")
		}
	}
	return cfg, nil
}

func measure(ctx context.Context, structureName string, size, payloadBytes int) (Result, error) {
	base := Result{
		Structure:    structureName,
		Size:         size,
		PayloadBytes: payloadBytes,
	}
	switch structureName {
	case "map", "list":
	default:
		base.Method = "unsupported"
		base.Accounting = normalizeSnapshot(evalstore.Snapshot{})
		base.Error = fmt.Sprintf("unsupported structure %q", structureName)
		return base, nil
	}

	factory, err := evalstore.NewFactory(evalstore.FactoryConfig{
		Mode:    evalstore.StoreModeIsolated,
		Backend: evalstore.StoreBackendMemory,
	})
	if err != nil {
		return Result{}, err
	}
	defer factory.Close()
	system, err := factory.NewSystem(ctx, structureName)
	if err != nil {
		return Result{}, err
	}

	values, err := writePayloads(ctx, system, size, payloadBytes)
	if err != nil {
		return Result{}, err
	}
	proof, root, err := commitAndProve(ctx, system, structureName, values)
	if err != nil {
		base.Method = "unsupported"
		base.Accounting = normalizeSnapshot(evalstore.Snapshot{})
		base.Error = err.Error()
		return base, nil
	}
	system.Meter.RecordLogicalBytes(evalstore.CategoryRootHead, len(root.Bytes()))

	accounting := normalizeSnapshot(system.Meter.Snapshot())
	return Result{
		Structure:           structureName,
		Size:                size,
		PayloadBytes:        payloadBytes,
		Method:              "measured",
		Accounting:          accounting,
		PersistedBytes:      accounting.Total.NewPersistedBytes,
		LogicalPayloadBytes: uint64(size * payloadBytes),
		LogicalProofBytes:   uint64(len(proof)),
	}, nil
}

func writePayloads(ctx context.Context, system *evalstore.System, size, payloadBytes int) ([]cid.Cid, error) {
	values := make([]cid.Cid, size)
	for i := range values {
		value, err := system.CAS.Put(ctx, payloadFor(i, payloadBytes))
		if err != nil {
			return nil, err
		}
		values[i] = value
	}
	return values, nil
}

func commitAndProve(ctx context.Context, system *evalstore.System, structureName string, values []cid.Cid) (structure.Proof, cid.Cid, error) {
	scheme, err := ipa.NewScheme()
	if err != nil {
		return nil, cid.Undef, err
	}
	table, err := overwrite.NewArcTable(overwrite.WithKVStore(system.StateKV))
	if err != nil {
		return nil, cid.Undef, err
	}
	defer table.Close()

	namespace := "storage-overhead"
	switch structureName {
	case "map":
		semantics, err := indexed.NewMap(scheme, table)
		if err != nil {
			return nil, cid.Undef, err
		}
		entries := make(map[arcset.Path]cid.Cid, len(values))
		for i, value := range values {
			entries[mapPath(i)] = value
		}
		root, err := semantics.Commit(ctx, namespace, mapping.NewViewFromPaths(entries))
		if err != nil {
			return nil, cid.Undef, err
		}
		key := mapPath(len(values) / 2)
		binding, proof, err := semantics.Prove(ctx, namespace, root, key)
		if err != nil {
			return nil, cid.Undef, err
		}
		ok, err := semantics.Verify(root, key, binding, proof)
		if err != nil {
			return nil, cid.Undef, err
		}
		if !ok {
			return nil, cid.Undef, fmt.Errorf("map proof did not verify")
		}
		return proof, root, nil
	case "list":
		semantics, err := listtree.NewList(scheme, table)
		if err != nil {
			return nil, cid.Undef, err
		}
		root, err := semantics.Commit(ctx, namespace, list.NewViewFromSlice(values))
		if err != nil {
			return nil, cid.Undef, err
		}
		index := uint64(len(values) / 2)
		query, proof, err := semantics.Prove(ctx, namespace, root, index)
		if err != nil {
			return nil, cid.Undef, err
		}
		ok, err := semantics.Verify(root, index, query, proof)
		if err != nil {
			return nil, cid.Undef, err
		}
		if !ok {
			return nil, cid.Undef, fmt.Errorf("list proof did not verify")
		}
		return proof, root, nil
	default:
		return nil, cid.Undef, fmt.Errorf("unsupported structure %q", structureName)
	}
}

func normalizeSnapshot(snapshot evalstore.Snapshot) evalstore.Snapshot {
	if snapshot.Categories == nil {
		snapshot.Categories = map[evalstore.Category]evalstore.Counter{}
	}
	for _, category := range []evalstore.Category{
		evalstore.CategoryCASPayload,
		evalstore.CategoryCASMetadata,
		evalstore.CategoryArcTable,
		evalstore.CategoryCommitment,
		evalstore.CategoryRootHead,
	} {
		if _, ok := snapshot.Categories[category]; !ok {
			snapshot.Categories[category] = evalstore.Counter{}
		}
	}
	return snapshot
}

func payloadFor(index, size int) []byte {
	sum := sha256.Sum256([]byte(fmt.Sprintf("storage-overhead:%d", index)))
	out := make([]byte, size)
	for i := range out {
		out[i] = sum[i%len(sum)]
	}
	return out
}

func mapPath(index int) arcset.Path {
	return arcset.CanonicalizePath(fmt.Sprintf("item/%06d", index))
}

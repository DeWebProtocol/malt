// Package proofoverhead measures local commit/prove/verify costs for semantic structures.
package proofoverhead

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/dewebprotocol/malt/core/arctable"
	"github.com/dewebprotocol/malt/core/arctable/versioned"
	"github.com/dewebprotocol/malt/core/cas"
	"github.com/dewebprotocol/malt/core/commitment"
	"github.com/dewebprotocol/malt/core/commitment/ipa"
	"github.com/dewebprotocol/malt/core/commitment/kzg"
	"github.com/dewebprotocol/malt/core/kvstore/memory"
	"github.com/dewebprotocol/malt/core/structure"
	"github.com/dewebprotocol/malt/core/structure/list"
	listtree "github.com/dewebprotocol/malt/core/structure/list/tree"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	mappingradix "github.com/dewebprotocol/malt/core/structure/mapping/radix"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/internal/eval/framework"
	"github.com/dewebprotocol/malt/internal/eval/suites/configjson"
	cid "github.com/ipfs/go-cid"
)

const suiteName = "proof_overhead"

const (
	arcTableModeVersioned = "versioned"
	mapBackendRadix       = "radix"
	listBackendTree       = "tree"
)

// Suite implements the proof overhead evaluation.
type Suite struct{}

// Config controls proof overhead dimensions.
type Config struct {
	Structures  []string `json:"structures"`
	Sizes       []int    `json:"sizes"`
	Iterations  int      `json:"iterations"`
	Commitments []string `json:"commitment"`
}

// Result is one proof overhead record.
type Result struct {
	Structure       string `json:"structure"`
	Commitment      string `json:"commitment"`
	ArcTableMode    string `json:"arctable_mode,omitempty"`
	MapBackend      string `json:"map_backend,omitempty"`
	ListBackend     string `json:"list_backend,omitempty"`
	Size            int    `json:"size"`
	Iteration       int    `json:"iteration"`
	Method          string `json:"method"`
	CommitElapsedNS int64  `json:"commit_elapsed_ns,omitempty"`
	ProveElapsedNS  int64  `json:"prove_elapsed_ns,omitempty"`
	VerifyElapsedNS int64  `json:"verify_elapsed_ns,omitempty"`
	ProofBytes      int    `json:"proof_bytes"`
	EvidenceCount   int    `json:"evidence_count"`
	Verified        *bool  `json:"verified,omitempty"`
	Error           string `json:"error,omitempty"`
}

// Name returns the stable suite name.
func (Suite) Name() string {
	return suiteName
}

// Run executes the configured proof overhead matrix.
func (Suite) Run(ctx context.Context, env framework.Env, raw json.RawMessage) error {
	cfg, err := parseConfig(raw)
	if err != nil {
		return err
	}

	for iteration := 0; iteration < cfg.Iterations; iteration++ {
		for _, structureName := range cfg.Structures {
			for _, size := range cfg.Sizes {
				for _, commitmentName := range cfg.Commitments {
					record, err := measure(ctx, structureName, commitmentName, size, iteration)
					if err != nil {
						return err
					}
					if err := env.WriteRecord(suiteName, record); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func parseConfig(raw json.RawMessage) (Config, error) {
	cfg := Config{
		Structures:  []string{"map", "list"},
		Sizes:       []int{1},
		Iterations:  1,
		Commitments: []string{"ipa"},
	}
	if len(raw) != 0 {
		var parsed struct {
			Structures []string        `json:"structures"`
			Sizes      []int           `json:"sizes"`
			Iterations int             `json:"iterations"`
			Commitment json.RawMessage `json:"commitment"`
		}
		if err := configjson.Decode(raw, suiteName, &parsed); err != nil {
			return Config{}, err
		}
		cfg.Structures = parsed.Structures
		cfg.Sizes = parsed.Sizes
		cfg.Iterations = parsed.Iterations
		if len(bytes.TrimSpace(parsed.Commitment)) != 0 && !bytes.Equal(bytes.TrimSpace(parsed.Commitment), []byte("null")) {
			commitments, err := parseCommitments(parsed.Commitment)
			if err != nil {
				return Config{}, err
			}
			cfg.Commitments = commitments
		} else {
			cfg.Commitments = nil
		}
	}
	if cfg.Structures == nil {
		cfg.Structures = []string{"map", "list"}
	}
	if cfg.Sizes == nil {
		cfg.Sizes = []int{1}
	}
	if cfg.Iterations == 0 {
		cfg.Iterations = 1
	}
	if cfg.Commitments == nil {
		cfg.Commitments = []string{"ipa"}
	}
	if cfg.Iterations < 0 {
		return Config{}, fmt.Errorf("iterations must be non-negative")
	}
	for _, size := range cfg.Sizes {
		if size <= 0 {
			return Config{}, fmt.Errorf("sizes must be positive")
		}
	}
	return cfg, nil
}

func parseCommitments(raw json.RawMessage) ([]string, error) {
	var single string
	if err := json.Unmarshal(raw, &single); err == nil {
		return []string{single}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, fmt.Errorf("parse proof_overhead commitment: %w", err)
	}
	return many, nil
}

func measure(ctx context.Context, structureName, commitmentName string, size, iteration int) (Result, error) {
	base := Result{
		Structure:    structureName,
		Commitment:   commitmentName,
		ArcTableMode: arcTableModeVersioned,
		Size:         size,
		Iteration:    iteration,
	}
	labelStructureBackend(&base)
	scheme, err := newScheme(commitmentName)
	if err != nil {
		base.Method = "unsupported"
		base.Error = err.Error()
		return base, nil
	}

	var measured Result
	switch structureName {
	case "map":
		measured, err = measureMap(ctx, scheme, commitmentName, size, iteration)
	case "list":
		measured, err = measureList(ctx, scheme, commitmentName, size, iteration)
	default:
		base.Method = "unsupported"
		base.Error = fmt.Sprintf("unsupported structure %q", structureName)
		return base, nil
	}
	if err != nil {
		base.Method = "unsupported"
		base.Error = err.Error()
		return base, nil
	}
	return measured, nil
}

func measureMap(ctx context.Context, scheme commitment.IndexCommitment, commitmentName string, size, iteration int) (Result, error) {
	table, err := newArcTable()
	if err != nil {
		return Result{}, err
	}
	defer table.Close()
	semantics, err := mappingradix.NewMap(scheme, table)
	if err != nil {
		return Result{}, err
	}

	values, err := deterministicCIDs("proof-map", size)
	if err != nil {
		return Result{}, err
	}
	entries := make(map[arcset.Path]cid.Cid, size)
	for i, value := range values {
		entries[mapPath(i)] = value
	}
	view := mapping.NewViewFromPaths(entries)
	key := mapPath(size / 2)

	start := time.Now()
	root, err := semantics.Commit(ctx, namespace(iteration), view)
	commitElapsed := time.Since(start).Nanoseconds()
	if err != nil {
		return Result{}, err
	}

	start = time.Now()
	binding, proof, err := semantics.Prove(ctx, namespace(iteration), root, key)
	proveElapsed := time.Since(start).Nanoseconds()
	if err != nil {
		return Result{}, err
	}

	start = time.Now()
	ok, err := semantics.Verify(root, key, binding, proof)
	verifyElapsed := time.Since(start).Nanoseconds()
	if err != nil {
		return Result{}, err
	}
	return measuredResult("map", commitmentName, size, iteration, commitElapsed, proveElapsed, verifyElapsed, proof, ok), nil
}

func measureList(ctx context.Context, scheme commitment.IndexCommitment, commitmentName string, size, iteration int) (Result, error) {
	table, err := newArcTable()
	if err != nil {
		return Result{}, err
	}
	defer table.Close()
	semantics, err := listtree.NewList(scheme, table)
	if err != nil {
		return Result{}, err
	}

	values, err := deterministicCIDs("proof-list", size)
	if err != nil {
		return Result{}, err
	}
	index := uint64(size / 2)

	start := time.Now()
	root, err := semantics.Commit(ctx, namespace(iteration), list.NewViewFromSlice(values))
	commitElapsed := time.Since(start).Nanoseconds()
	if err != nil {
		return Result{}, err
	}

	start = time.Now()
	query, proof, err := semantics.Prove(ctx, namespace(iteration), root, index)
	proveElapsed := time.Since(start).Nanoseconds()
	if err != nil {
		return Result{}, err
	}

	start = time.Now()
	ok, err := semantics.Verify(root, index, query, proof)
	verifyElapsed := time.Since(start).Nanoseconds()
	if err != nil {
		return Result{}, err
	}
	return measuredResult("list", commitmentName, size, iteration, commitElapsed, proveElapsed, verifyElapsed, proof, ok), nil
}

func measuredResult(structureName, commitmentName string, size, iteration int, commitElapsed, proveElapsed, verifyElapsed int64, proof structure.Proof, verified bool) Result {
	result := Result{
		Structure:       structureName,
		Commitment:      commitmentName,
		ArcTableMode:    arcTableModeVersioned,
		Size:            size,
		Iteration:       iteration,
		Method:          "measured",
		CommitElapsedNS: commitElapsed,
		ProveElapsedNS:  proveElapsed,
		VerifyElapsedNS: verifyElapsed,
		ProofBytes:      len(proof),
		EvidenceCount:   evidenceCount(structureName, proof),
		Verified:        &verified,
	}
	labelStructureBackend(&result)
	return result
}

func labelStructureBackend(result *Result) {
	switch result.Structure {
	case "map":
		result.MapBackend = mapBackendRadix
	case "list":
		result.ListBackend = listBackendTree
	}
}

func newScheme(name string) (commitment.IndexCommitment, error) {
	switch name {
	case "ipa":
		return ipa.NewScheme()
	case "kzg":
		return kzg.NewScheme()
	default:
		return nil, fmt.Errorf("unsupported commitment %q", name)
	}
}

func newArcTable() (arctable.ArcTable, error) {
	return versioned.NewArcTable(versioned.WithKVStore(memory.New()))
}

func deterministicCIDs(prefix string, size int) ([]cid.Cid, error) {
	values := make([]cid.Cid, size)
	for i := range values {
		value, err := cas.CIDForBlock(cas.Block{Data: []byte(fmt.Sprintf("%s:%06d", prefix, i))})
		if err != nil {
			return nil, err
		}
		values[i] = value
	}
	return values, nil
}

func mapPath(index int) arcset.Path {
	return arcset.CanonicalizePath(fmt.Sprintf("item/%06d", index))
}

func namespace(iteration int) string {
	return fmt.Sprintf("proof-overhead-%d", iteration)
}

func evidenceCount(structureName string, proof []byte) int {
	if len(proof) == 0 {
		return 0
	}
	if structureName != "list" {
		return 1
	}
	var envelope struct {
		LengthProof []byte `json:"length_proof"`
		Steps       []struct {
			Proof []byte `json:"proof"`
		} `json:"steps"`
	}
	if err := json.Unmarshal(proof, &envelope); err != nil {
		return 1
	}
	count := 0
	if len(envelope.LengthProof) > 0 {
		count++
	}
	for _, step := range envelope.Steps {
		if len(step.Proof) > 0 {
			count++
		}
	}
	return count
}

package sce

import (
	"fmt"

	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

type proverFunc func(path string) (cid.Cid, []byte, error)
type verifierFunc func(path string, value cid.Cid, proof []byte) (bool, error)

func batchProve(paths []string, prove proverFunc) (map[string]arcset.BatchProofEntry, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("paths is empty")
	}

	results := make(map[string]arcset.BatchProofEntry, len(paths))
	for _, path := range paths {
		target, proof, err := prove(path)
		if err != nil {
			return nil, fmt.Errorf("failed to prove path %s: %w", path, err)
		}
		results[path] = arcset.BatchProofEntry{
			Target: target,
			Proof:  proof,
		}
	}
	return results, nil
}

func batchVerify(proofs map[string]arcset.BatchProofEntry, verify verifierFunc) (bool, error) {
	for path, entry := range proofs {
		ok, err := verify(path, entry.Target, entry.Proof)
		if err != nil {
			return false, fmt.Errorf("failed to verify path %s: %w", path, err)
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

func aggregateProve(paths []string, prove proverFunc) (*arcset.AggregatedProof, error) {
	if len(paths) == 0 {
		return nil, fmt.Errorf("paths is empty")
	}

	targets := make([]cid.Cid, len(paths))
	proofs := make([][]byte, len(paths))
	for i, path := range paths {
		target, proof, err := prove(path)
		if err != nil {
			return nil, fmt.Errorf("failed to prove path %s: %w", path, err)
		}
		targets[i] = target
		proofs[i] = append([]byte(nil), proof...)
	}

	return &arcset.AggregatedProof{
		Paths:   append([]string(nil), paths...),
		Targets: targets,
		Proofs:  proofs,
	}, nil
}

func aggregateVerify(aggProof *arcset.AggregatedProof, verify verifierFunc) (bool, error) {
	if aggProof == nil || len(aggProof.Paths) == 0 {
		return false, fmt.Errorf("invalid aggregated proof")
	}
	if len(aggProof.Paths) != len(aggProof.Targets) || len(aggProof.Paths) != len(aggProof.Proofs) {
		return false, fmt.Errorf("aggregated proof size mismatch")
	}

	for i, path := range aggProof.Paths {
		ok, err := verify(path, aggProof.Targets[i], aggProof.Proofs[i])
		if err != nil {
			return false, fmt.Errorf("failed to verify path %s: %w", path, err)
		}
		if !ok {
			return false, nil
		}
	}
	return true, nil
}

func findPathIndex(paths []string, path string) (int, bool) {
	low, high := 0, len(paths)-1
	for low <= high {
		mid := (low + high) / 2
		if paths[mid] == path {
			return mid, true
		}
		if paths[mid] < path {
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return -1, false
}

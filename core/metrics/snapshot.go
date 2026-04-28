package metrics

import (
	"sync/atomic"

	"github.com/dewebprotocol/malt/core/types/prooflist"
)

// Snapshot is a point-in-time view of node-local evaluation counters.
type Snapshot struct {
	CAS      CASStats      `json:"cas"`
	ArcTable ArcTableStats `json:"arctable"`
	Proof    ProofStats    `json:"proof"`
}

// ProofStats is a point-in-time snapshot of verifier proof byte counters.
type ProofStats struct {
	ProofListCount uint64 `json:"proof_list_count"`
	StepCount      uint64 `json:"step_count"`
	EvidenceBytes  uint64 `json:"evidence_bytes"`
	ProofBytes     uint64 `json:"proof_bytes"`
	TotalBytes     uint64 `json:"total_bytes"`
}

// ProofStatsRecorder records proof-list shape and byte counters.
type ProofStatsRecorder struct {
	proofListCount atomic.Uint64
	stepCount      atomic.Uint64
	evidenceBytes  atomic.Uint64
	proofBytes     atomic.Uint64
}

// RecordProofList records the available byte accounting for one ProofList.
func (r *ProofStatsRecorder) RecordProofList(pl prooflist.ProofList) {
	r.proofListCount.Add(1)
	r.stepCount.Add(uint64(len(pl.Steps)))
	for _, step := range pl.Steps {
		if len(step.Evidence) > 0 {
			r.evidenceBytes.Add(uint64(len(step.Evidence)))
		}
		if len(step.Proof) > 0 {
			r.proofBytes.Add(uint64(len(step.Proof)))
		}
	}
}

// Snapshot returns the current proof counters.
func (r *ProofStatsRecorder) Snapshot() ProofStats {
	evidenceBytes := r.evidenceBytes.Load()
	proofBytes := r.proofBytes.Load()
	return ProofStats{
		ProofListCount: r.proofListCount.Load(),
		StepCount:      r.stepCount.Load(),
		EvidenceBytes:  evidenceBytes,
		ProofBytes:     proofBytes,
		TotalBytes:     evidenceBytes + proofBytes,
	}
}

// Reset clears all proof counters.
func (r *ProofStatsRecorder) Reset() {
	r.proofListCount.Store(0)
	r.stepCount.Store(0)
	r.evidenceBytes.Store(0)
	r.proofBytes.Store(0)
}

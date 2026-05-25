package metrics

import (
	"testing"

	"github.com/dewebprotocol/malt/auth/proof/prooflist"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func metricsTestCID(t *testing.T, seed string) cid.Cid {
	t.Helper()
	sum, err := mh.Sum([]byte(seed), mh.SHA2_256, -1)
	if err != nil {
		t.Fatalf("mh.Sum failed: %v", err)
	}
	return cid.NewCidV1(cid.Raw, sum)
}

func TestProofStatsRecorderCountsProofListBytesAndReset(t *testing.T) {
	root := metricsTestCID(t, "root")
	target := metricsTestCID(t, "target")

	var recorder ProofStatsRecorder
	recorder.RecordProofList(prooflist.ProofList{
		Root: root,
		Steps: []prooflist.Step{
			{
				Kind:     prooflist.KindMapStep,
				From:     root,
				Target:   target,
				Evidence: []byte{1, 2, 3},
				Proof:    []byte{4, 5},
			},
			{
				Kind:     prooflist.KindPayloadBinding,
				From:     target,
				Target:   root,
				Evidence: []byte{6},
			},
		},
	})

	stats := recorder.Snapshot()
	if stats.ProofListCount != 1 {
		t.Fatalf("ProofListCount = %d, want 1", stats.ProofListCount)
	}
	if stats.StepCount != 2 {
		t.Fatalf("StepCount = %d, want 2", stats.StepCount)
	}
	if stats.EvidenceBytes != 4 {
		t.Fatalf("EvidenceBytes = %d, want 4", stats.EvidenceBytes)
	}
	if stats.ProofBytes != 2 {
		t.Fatalf("ProofBytes = %d, want 2", stats.ProofBytes)
	}
	if stats.TotalBytes != 6 {
		t.Fatalf("TotalBytes = %d, want 6", stats.TotalBytes)
	}

	recorder.Reset()
	if got := recorder.Snapshot(); got != (ProofStats{}) {
		t.Fatalf("stats after reset = %+v, want zero", got)
	}
}

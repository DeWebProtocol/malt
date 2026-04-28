package api

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/core/cas"
	"github.com/dewebprotocol/malt/core/metrics"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/types/prooflist"
	cid "github.com/ipfs/go-cid"
)

func TestNodeMetricsSnapshotAndReset(t *testing.T) {
	node, err := NewNode(WithConfig(testConfig(t)))
	if err != nil {
		t.Fatalf("NewNode failed: %v", err)
	}
	defer node.Close()

	ctx := context.Background()
	blocks, ok := node.CAS().(cas.Client)
	if !ok {
		t.Fatalf("node CAS = %T, want cas.Client", node.CAS())
	}
	payload := []byte("metrics payload")
	payloadCID, err := blocks.Put(ctx, payload)
	if err != nil {
		t.Fatalf("put payload: %v", err)
	}
	if _, err := blocks.Get(ctx, payloadCID); err != nil {
		t.Fatalf("get payload: %v", err)
	}

	g, err := node.NewGraph("metrics")
	if err != nil {
		t.Fatalf("NewGraph failed: %v", err)
	}
	root, err := g.Commit(ctx, arcset.NewSetFrom(map[string]cid.Cid{
		"@payload": payloadCID,
		"name":     newTestCID("name"),
	}))
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}
	if _, err := g.Snapshot(ctx, root); err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	node.RecordProofList(prooflist.ProofList{
		Root: root,
		Steps: []prooflist.Step{{
			Kind:     prooflist.KindMapStep,
			From:     root,
			Target:   payloadCID,
			Evidence: []byte{1, 2},
			Proof:    []byte{3},
		}},
	})

	snapshot := node.MetricsSnapshot()
	if snapshot.CAS.PutCount != 1 {
		t.Fatalf("CAS PutCount = %d, want 1", snapshot.CAS.PutCount)
	}
	if snapshot.CAS.GetCount != 1 {
		t.Fatalf("CAS GetCount = %d, want 1", snapshot.CAS.GetCount)
	}
	if snapshot.CAS.BytesPut != uint64(len(payload)) {
		t.Fatalf("CAS BytesPut = %d, want %d", snapshot.CAS.BytesPut, len(payload))
	}
	if snapshot.CAS.BytesGet != uint64(len(payload)) {
		t.Fatalf("CAS BytesGet = %d, want %d", snapshot.CAS.BytesGet, len(payload))
	}
	if snapshot.ArcTable.UpdateCount == 0 {
		t.Fatalf("ArcTable UpdateCount = %d, want > 0", snapshot.ArcTable.UpdateCount)
	}
	if snapshot.ArcTable.SnapshotCount == 0 {
		t.Fatalf("ArcTable SnapshotCount = %d, want > 0", snapshot.ArcTable.SnapshotCount)
	}
	if snapshot.Proof.ProofListCount != 1 || snapshot.Proof.TotalBytes != 3 {
		t.Fatalf("Proof stats = %+v, want one prooflist with 3 bytes", snapshot.Proof)
	}

	node.ResetMetrics()
	if got := node.MetricsSnapshot(); got != (metrics.Snapshot{}) {
		t.Fatalf("metrics after reset = %+v, want zero", got)
	}
}

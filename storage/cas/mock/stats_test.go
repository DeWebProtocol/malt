package mock

import (
	"context"
	"testing"
)

func TestCASStatsCountOperationsAndBytes(t *testing.T) {
	ctx := context.Background()
	cas := NewCAS()

	payload := []byte("instrumented block")
	blockCID, err := cas.Put(ctx, payload)
	if err != nil {
		t.Fatalf("put block: %v", err)
	}
	if _, err := cas.Get(ctx, blockCID); err != nil {
		t.Fatalf("get block: %v", err)
	}
	if ok, err := cas.Has(ctx, blockCID); err != nil {
		t.Fatalf("has block: %v", err)
	} else if !ok {
		t.Fatal("expected block to exist")
	}

	stats := cas.SnapshotStats()
	if stats.PutCount != 1 {
		t.Fatalf("PutCount = %d, want 1", stats.PutCount)
	}
	if stats.GetCount != 1 {
		t.Fatalf("GetCount = %d, want 1", stats.GetCount)
	}
	if stats.HasCount != 1 {
		t.Fatalf("HasCount = %d, want 1", stats.HasCount)
	}
	if stats.BytesPut != uint64(len(payload)) {
		t.Fatalf("BytesPut = %d, want %d", stats.BytesPut, len(payload))
	}
	if stats.BytesGet != uint64(len(payload)) {
		t.Fatalf("BytesGet = %d, want %d", stats.BytesGet, len(payload))
	}
}

func TestCASStatsResetClearsCounters(t *testing.T) {
	ctx := context.Background()
	cas := NewCAS()

	blockCID, err := cas.Put(ctx, []byte("reset me"))
	if err != nil {
		t.Fatalf("put block: %v", err)
	}
	if _, err := cas.Get(ctx, blockCID); err != nil {
		t.Fatalf("get block: %v", err)
	}

	cas.ResetStats()

	stats := cas.SnapshotStats()
	if stats != (CASStats{}) {
		t.Fatalf("stats after reset = %+v, want zero", stats)
	}
}

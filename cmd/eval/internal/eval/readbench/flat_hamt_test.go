package readbench

import (
	"context"
	"testing"
)

func TestFlatHAMTMatrixSystemLooksUpFullPathKeys(t *testing.T) {
	ctx := context.Background()
	dataset, err := NewMatrixDataset(MatrixDatasetConfig{
		Name:          "flat-hamt-unit",
		Depths:        []int{1, 4},
		PayloadBytes:  32,
		PathsPerDepth: 1,
	})
	if err != nil {
		t.Fatalf("NewMatrixDataset() error = %v", err)
	}
	system, err := NewMatrixSystem(ctx, SystemFlatHAMT, dataset, 0)
	if err != nil {
		t.Fatalf("NewMatrixSystem(flat hamt) error = %v", err)
	}
	defer system.Close()

	var getCounts []uint64
	for _, depth := range []int{1, 4} {
		ops, err := MatrixOperations(dataset, depth)
		if err != nil {
			t.Fatalf("MatrixOperations(%d) error = %v", depth, err)
		}
		result, err := system.Measure(ctx, 0, dataset, ops[0])
		if err != nil {
			t.Fatalf("Measure(depth=%d) error = %v", depth, err)
		}
		if result.System != SystemFlatHAMT {
			t.Fatalf("system = %q, want %q", result.System, SystemFlatHAMT)
		}
		if result.Target == "" {
			t.Fatalf("depth %d resolved empty target", depth)
		}
		if result.ContentBytes == nil || *result.ContentBytes != 32 {
			t.Fatalf("depth %d content_bytes = %v, want 32", depth, result.ContentBytes)
		}
		getCounts = append(getCounts, result.CAS.GetCount)
	}
	if getCounts[1] > getCounts[0]+1 {
		t.Fatalf("flat HAMT CAS get count grew with path depth: depth1=%d depth4=%d", getCounts[0], getCounts[1])
	}
}

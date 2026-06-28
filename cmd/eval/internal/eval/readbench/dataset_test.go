package readbench

import "testing"

func TestMatrixDatasetDepthCountsRootToTargetEdgesAndSamplesPaths(t *testing.T) {
	dataset, err := NewMatrixDataset(MatrixDatasetConfig{
		Name:          "matrix-unit",
		Depths:        []int{1, 3},
		PayloadBytes:  16,
		PathsPerDepth: 3,
	})
	if err != nil {
		t.Fatalf("NewMatrixDataset() error = %v", err)
	}

	if got := dataset.LookupPaths[1]; len(got) != 3 || got[0] != "lookup-00.txt" || got[2] != "lookup-02.txt" {
		t.Fatalf("depth 1 lookup paths = %v", got)
	}
	if got := dataset.LookupPaths[3]; len(got) != 3 || got[0] != "dir00/dir01/lookup-00.txt" {
		t.Fatalf("depth 3 lookup paths = %v", got)
	}
	if dataset.FileCount != 6 {
		t.Fatalf("file_count = %d, want one lookup file per depth sample", dataset.FileCount)
	}
}

func TestMatrixOperationsReturnsAllPathSamplesAtDepth(t *testing.T) {
	dataset, err := NewMatrixDataset(MatrixDatasetConfig{
		Depths:        []int{2},
		PayloadBytes:  16,
		PathsPerDepth: 2,
	})
	if err != nil {
		t.Fatalf("NewMatrixDataset() error = %v", err)
	}

	ops, err := MatrixOperations(dataset, 2)
	if err != nil {
		t.Fatalf("MatrixOperations() error = %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("operation count = %d, want 2", len(ops))
	}
	for i, op := range ops {
		if op.PathDepth != 2 {
			t.Fatalf("op %d depth = %d, want 2", i, op.PathDepth)
		}
		if op.PathSample != i+1 {
			t.Fatalf("op %d path sample = %d, want %d", i, op.PathSample, i+1)
		}
	}
	if ops[0].Path != "dir00/lookup-00.txt" || ops[1].Path != "dir00/lookup-01.txt" {
		t.Fatalf("operation paths = %q, %q", ops[0].Path, ops[1].Path)
	}
}

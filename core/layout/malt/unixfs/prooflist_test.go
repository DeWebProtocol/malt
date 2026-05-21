package unixfs_test

import (
	"bytes"
	"context"
	"testing"

	"github.com/dewebprotocol/malt/core/layout/malt/unixfs"
	"github.com/dewebprotocol/malt/core/structure/list"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/types/prooflist"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func testCID(t *testing.T, seed string) cid.Cid {
	t.Helper()
	sum, err := mh.Sum([]byte(seed), mh.SHA2_256, -1)
	if err != nil {
		t.Fatalf("mh.Sum failed: %v", err)
	}
	return cid.NewCidV1(cid.Raw, sum)
}

func TestProofListFromStepsClassifiesTerminalPayloadBinding(t *testing.T) {
	root := testCID(t, "root")
	fileRoot := testCID(t, "file")
	payload := testCID(t, "payload")
	steps := []unixfs.Step{
		{
			Root:   root,
			Path:   arcset.CanonicalizePath("dir"),
			Target: fileRoot,
			Proof:  []byte("dir proof"),
		},
		{
			Root:   fileRoot,
			Path:   arcset.CanonicalizePath("@payload"),
			Target: payload,
			Proof:  []byte("payload proof"),
		},
	}

	pl, err := unixfs.ProofListFromSteps(root, "dir/file.txt", steps)
	if err != nil {
		t.Fatalf("ProofListFromSteps failed: %v", err)
	}
	if err := pl.ValidateShape(prooflist.RequireSteps()); err != nil {
		t.Fatalf("ValidateShape failed: %v", err)
	}
	if pl.Query != "dir/file.txt" {
		t.Fatalf("query = %q, want dir/file.txt", pl.Query)
	}
	if len(pl.Steps) != 2 {
		t.Fatalf("steps len = %d, want 2", len(pl.Steps))
	}
	if pl.Steps[0].Kind != prooflist.KindMapStep {
		t.Fatalf("step 0 kind = %q, want %q", pl.Steps[0].Kind, prooflist.KindMapStep)
	}
	if pl.Steps[1].Kind != prooflist.KindPayloadBinding {
		t.Fatalf("step 1 kind = %q, want %q", pl.Steps[1].Kind, prooflist.KindPayloadBinding)
	}
	if pl.Steps[1].Path != "@payload" {
		t.Fatalf("step 1 path = %q, want @payload", pl.Steps[1].Path)
	}
	if pl.Steps[1].EvidenceKind != "structure" || pl.Steps[1].EvidenceBackend != "map" {
		t.Fatalf("step 1 evidence labels = %q/%q, want structure/map", pl.Steps[1].EvidenceKind, pl.Steps[1].EvidenceBackend)
	}
	if !bytes.Equal(pl.Steps[1].Proof, []byte("payload proof")) {
		t.Fatalf("terminal proof bytes were not preserved")
	}
}

func TestAppendListIndexStepsClassifiesKnownListQueries(t *testing.T) {
	listRoot := testCID(t, "list")
	chunk0 := testCID(t, "chunk-0")
	chunk1 := testCID(t, "chunk-1")

	pl, err := unixfs.ProofListFromSteps(listRoot, "blob.bin[0:8]", nil)
	if err != nil {
		t.Fatalf("ProofListFromSteps failed: %v", err)
	}
	err = unixfs.AppendListIndexSteps(pl, "blob.bin[0:8]", []unixfs.ListIndexStep{
		{
			Root:   listRoot,
			Index:  0,
			Target: chunk0,
			Proof:  []byte("index 0 proof"),
		},
		{
			Root:   listRoot,
			Index:  1,
			Target: chunk1,
			Proof:  []byte("index 1 proof"),
		},
	})
	if err != nil {
		t.Fatalf("AppendListIndexSteps failed: %v", err)
	}
	if err := pl.ValidateShape(prooflist.RequireSteps()); err != nil {
		t.Fatalf("ValidateShape failed: %v", err)
	}
	if len(pl.Steps) != 2 {
		t.Fatalf("steps len = %d, want 2", len(pl.Steps))
	}
	for i, step := range pl.Steps {
		if step.Kind != prooflist.KindListIndex {
			t.Fatalf("step %d kind = %q, want %q", i, step.Kind, prooflist.KindListIndex)
		}
		if step.Index == nil || *step.Index != uint64(i) {
			t.Fatalf("step %d index = %v, want %d", i, step.Index, i)
		}
		if step.Coordinate != string(rune('0'+i)) {
			t.Fatalf("step %d coordinate = %q, want %d", i, step.Coordinate, i)
		}
		if step.EvidenceKind != "structure" || step.EvidenceBackend != "list" {
			t.Fatalf("step %d evidence labels = %q/%q, want structure/list", i, step.EvidenceKind, step.EvidenceBackend)
		}
	}
	if !bytes.Equal(pl.Steps[1].Proof, []byte("index 1 proof")) {
		t.Fatalf("list index proof bytes were not preserved")
	}
}

func TestAppendListRangeStepClassifiesMeasuredListQuery(t *testing.T) {
	listRoot := testCID(t, "list")
	chunk0 := testCID(t, "chunk-0")
	chunk1 := testCID(t, "chunk-1")
	start := uint64(2)
	end := uint64(6)

	pl, err := unixfs.ProofListFromSteps(listRoot, "blob.bin", nil)
	if err != nil {
		t.Fatalf("ProofListFromSteps failed: %v", err)
	}
	err = unixfs.AppendListRangeStep(pl, "blob.bin", listRoot, start, end, list.RangeResult{
		Metadata: list.RangeMetadata{
			ChildCount: 2,
			TotalSize:  8,
			ChunkSize:  4,
		},
		Segments: []cid.Cid{chunk0, chunk1},
	}, []byte("range proof"))
	if err != nil {
		t.Fatalf("AppendListRangeStep failed: %v", err)
	}
	if err := pl.ValidateShape(prooflist.RequireSteps()); err != nil {
		t.Fatalf("ValidateShape failed: %v", err)
	}
	if len(pl.Steps) != 1 {
		t.Fatalf("steps len = %d, want 1", len(pl.Steps))
	}
	step := pl.Steps[0]
	if step.Kind != prooflist.KindListRange {
		t.Fatalf("step kind = %q, want %q", step.Kind, prooflist.KindListRange)
	}
	if step.Start == nil || *step.Start != start {
		t.Fatalf("step start = %v, want %d", step.Start, start)
	}
	if step.End == nil || *step.End != end {
		t.Fatalf("step end = %v, want %d", step.End, end)
	}
	if step.ChildCount == nil || *step.ChildCount != 2 {
		t.Fatalf("step child count = %v, want 2", step.ChildCount)
	}
	if step.TotalSize == nil || *step.TotalSize != 8 {
		t.Fatalf("step total size = %v, want 8", step.TotalSize)
	}
	if step.ChunkSize == nil || *step.ChunkSize != 4 {
		t.Fatalf("step chunk size = %v, want 4", step.ChunkSize)
	}
	if len(step.Segments) != 2 || !step.Segments[0].Equals(chunk0) || !step.Segments[1].Equals(chunk1) {
		t.Fatalf("step segments = %v, want [%s %s]", step.Segments, chunk0, chunk1)
	}
	if step.EvidenceKind != "structure" || step.EvidenceBackend != "measured_list" {
		t.Fatalf("step evidence labels = %q/%q, want structure/measured_list", step.EvidenceKind, step.EvidenceBackend)
	}
	if !bytes.Equal(step.Proof, []byte("range proof")) {
		t.Fatalf("list range proof bytes were not preserved")
	}
}

func TestListIndexStepsForFileRangeReturnsComposedIndexEvidence(t *testing.T) {
	ctx := context.Background()
	layout := newLayout(t, 4)

	root, err := layout.AddFile(ctx, cid.Undef, "blob.bin", []byte("abcdefghijkl"))
	if err != nil {
		t.Fatalf("AddFile failed: %v", err)
	}

	steps, err := layout.ListIndexStepsForFileRange(ctx, root, "blob.bin", 3, 6)
	if err != nil {
		t.Fatalf("ListIndexStepsForFileRange failed: %v", err)
	}
	if len(steps) != 3 {
		t.Fatalf("steps len = %d, want 3", len(steps))
	}
	for i, step := range steps {
		if step.Index != uint64(i) {
			t.Fatalf("step %d index = %d, want %d", i, step.Index, i)
		}
		if !step.Root.Defined() {
			t.Fatalf("step %d root is undefined", i)
		}
		if !step.Target.Defined() {
			t.Fatalf("step %d target is undefined", i)
		}
		if len(step.Proof) == 0 {
			t.Fatalf("step %d proof is empty", i)
		}
	}
}

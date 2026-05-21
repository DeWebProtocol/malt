package gateway_test

import (
	"context"
	"strconv"
	"testing"

	"github.com/dewebprotocol/malt/core/gateway"
	"github.com/dewebprotocol/malt/core/structure/list"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

func TestExecutorAppliesMapDelta(t *testing.T) {
	ctx := context.Background()
	exec := newExecutor(t)
	payload := testCID("payload")
	oldChild := testCID("old-child")
	newChild := testCID("new-child")
	baseRoot, err := exec.Maps.Commit(ctx, exec.Namespace, mapping.NewViewFromPaths(map[arcset.Path]cid.Cid{
		arcset.CanonicalizePath("@payload"): payload,
		arcset.CanonicalizePath("child"):    oldChild,
	}))
	if err != nil {
		t.Fatalf("Commit base map failed: %v", err)
	}

	receipt, err := exec.Apply(ctx, gateway.SemanticMutation{
		BaseRoot: baseRoot,
		Deltas: []gateway.ArcSetDelta{{
			Object: baseRoot,
			Kind:   arcset.KindMap,
			Changes: mustCanonicalDelta(t, arcset.KindMap, []deltaChangeSpec{{
				Path:   "child",
				Before: arcset.NewMapTarget(oldChild),
				After:  arcset.NewMapTarget(newChild),
			}}),
		}},
	})
	if err != nil {
		t.Fatalf("Apply delta failed: %v", err)
	}
	if receipt.DeltaCount != 1 || receipt.ArcCount != 1 {
		t.Fatalf("receipt counts = deltas %d arcs %d, want 1/1", receipt.DeltaCount, receipt.ArcCount)
	}

	binding, proof, err := exec.Maps.Prove(ctx, exec.Namespace, receipt.NewRoot, arcset.CanonicalizePath("child"))
	if err != nil {
		t.Fatalf("Prove child failed: %v", err)
	}
	if !binding.Present || !binding.Value.Equals(newChild) {
		t.Fatalf("child binding = %+v, want %s", binding, newChild)
	}
	ok, err := exec.Maps.Verify(receipt.NewRoot, arcset.CanonicalizePath("child"), binding, proof)
	if err != nil {
		t.Fatalf("Verify child failed: %v", err)
	}
	if !ok {
		t.Fatal("child proof did not verify")
	}
}

func TestExecutorAppliesListAppendDelta(t *testing.T) {
	ctx := context.Background()
	exec := newExecutor(t)
	first := testCID("first")
	second := testCID("second")
	third := testCID("third")
	baseRoot, err := exec.Lists.Commit(ctx, exec.Namespace, list.NewViewFromSlice([]cid.Cid{first, second}))
	if err != nil {
		t.Fatalf("Commit base list failed: %v", err)
	}

	receipt, err := exec.Apply(ctx, gateway.SemanticMutation{
		BaseRoot: baseRoot,
		Deltas: []gateway.ArcSetDelta{{
			Object: baseRoot,
			Kind:   arcset.KindList,
			Changes: mustCanonicalDelta(t, arcset.KindList, []deltaChangeSpec{{
				Index: uint64Ptr(2),
				After: arcset.NewCASTarget(third),
			}}),
		}},
	})
	if err != nil {
		t.Fatalf("Apply list append delta failed: %v", err)
	}

	query, proof, err := exec.Lists.Prove(ctx, exec.Namespace, receipt.NewRoot, 2)
	if err != nil {
		t.Fatalf("Prove appended index failed: %v", err)
	}
	if query.Length != 3 || !query.Key.Equals(third) {
		t.Fatalf("query = %+v, want length 3 key %s", query, third)
	}
	ok, err := exec.Lists.Verify(receipt.NewRoot, 2, query, proof)
	if err != nil {
		t.Fatalf("Verify appended index failed: %v", err)
	}
	if !ok {
		t.Fatal("appended index proof did not verify")
	}
}

func TestExecutorAppliesFixedMeasuredListAppendDeltaAcrossGrowth(t *testing.T) {
	ctx := context.Background()
	exec := newExecutor(t)
	chunkSize := uint64(4)
	chunks := make([]cid.Cid, 256)
	for i := range chunks {
		chunks[i] = testCID("fixed-chunk-" + strconv.Itoa(i))
	}
	baseRoot, err := exec.Lists.(interface {
		CommitFixed(context.Context, string, []cid.Cid, uint64, uint64) (cid.Cid, error)
	}).CommitFixed(ctx, exec.Namespace, chunks[:255], chunkSize, uint64(255)*chunkSize)
	if err != nil {
		t.Fatalf("CommitFixed base failed: %v", err)
	}
	expectedRoot, err := exec.Lists.(interface {
		CommitFixed(context.Context, string, []cid.Cid, uint64, uint64) (cid.Cid, error)
	}).CommitFixed(ctx, exec.Namespace, chunks, chunkSize, uint64(256)*chunkSize)
	if err != nil {
		t.Fatalf("CommitFixed expected failed: %v", err)
	}

	receipt, err := exec.Apply(ctx, gateway.SemanticMutation{
		BaseRoot: baseRoot,
		Deltas: []gateway.ArcSetDelta{{
			Object:       baseRoot,
			ExpectedRoot: expectedRoot,
			Kind:         arcset.KindList,
			Changes: mustCanonicalDelta(t, arcset.KindList, []deltaChangeSpec{{
				Index: uint64Ptr(255),
				After: arcset.NewCASTarget(chunks[255]),
			}}),
			Commit: gateway.CommitDescriptor{
				FixedList: &gateway.FixedListCommit{
					TotalSize: uint64(256) * chunkSize,
					ChunkSize: chunkSize,
				},
			},
		}},
	})
	if err != nil {
		t.Fatalf("Apply fixed list append delta failed: %v", err)
	}
	if !receipt.NewRoot.Equals(expectedRoot) {
		t.Fatalf("new root = %s, want %s", receipt.NewRoot, expectedRoot)
	}

	measured := exec.Lists.(list.MeasuredSemantics)
	result, proof, err := measured.ProveRange(ctx, exec.Namespace, receipt.NewRoot, uint64(255)*chunkSize, nil)
	if err != nil {
		t.Fatalf("ProveRange appended chunk failed: %v", err)
	}
	if result.Metadata.ChildCount != 256 || result.Metadata.TotalSize != uint64(256)*chunkSize {
		t.Fatalf("metadata = %+v, want count=256 total=%d", result.Metadata, uint64(256)*chunkSize)
	}
	ok, err := measured.VerifyRange(receipt.NewRoot, uint64(255)*chunkSize, nil, result, proof)
	if err != nil {
		t.Fatalf("VerifyRange appended chunk failed: %v", err)
	}
	if !ok {
		t.Fatal("appended range proof did not verify")
	}
}

type deltaChangeSpec struct {
	Path   string
	Index  *uint64
	Before arcset.TargetRef
	After  arcset.TargetRef
}

func mustCanonicalDelta(t *testing.T, kind arcset.Kind, specs []deltaChangeSpec) *arcset.CanonicalArcDelta {
	t.Helper()

	changes := make([]arcset.ArcChange, 0, len(specs))
	for _, spec := range specs {
		var coord arcset.CanonicalCoordinate
		var err error
		switch kind {
		case arcset.KindMap:
			coord, err = arcset.NewMapCoordinate(spec.Path)
		case arcset.KindList:
			coord, err = arcset.NewListCoordinate(int64(*spec.Index))
		default:
			t.Fatalf("unsupported kind %q", kind)
		}
		if err != nil {
			t.Fatalf("coordinate: %v", err)
		}
		change := arcset.ArcChange{Coordinate: coord}
		if spec.Before.CID().Defined() {
			before := spec.Before
			change.Before = &before
		}
		if spec.After.CID().Defined() {
			after := spec.After
			change.After = &after
		}
		changes = append(changes, change)
	}
	delta, err := arcset.NewCanonicalArcDelta(kind, changes)
	if err != nil {
		t.Fatalf("NewCanonicalArcDelta failed: %v", err)
	}
	return delta
}

func uint64Ptr(v uint64) *uint64 {
	return &v
}

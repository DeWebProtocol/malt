package unixfs_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt/core/structure"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/layout/unixfs"
	cid "github.com/ipfs/go-cid"
)

func TestSmallFileMutationPlanIncludesMapPayload(t *testing.T) {
	ctx := context.Background()
	layout := newLayout(t, 8)

	root, err := layout.AddFile(ctx, cid.Undef, "hello.txt", []byte("hello"))
	if err != nil {
		t.Fatalf("AddFile failed: %v", err)
	}

	plan, err := layout.MutationPlanForPath(ctx, root, "hello.txt")
	if err != nil {
		t.Fatalf("MutationPlanForPath failed: %v", err)
	}
	if !plan.BaseRoot.Equals(root) {
		t.Fatalf("plan BaseRoot = %s, want %s", plan.BaseRoot, root)
	}
	if len(plan.Deltas) != 1 {
		t.Fatalf("delta count = %d, want 1", len(plan.Deltas))
	}
	delta := plan.Deltas[0]
	if delta.Kind != arcset.KindMap {
		t.Fatalf("delta kind = %q, want map", delta.Kind)
	}
	if delta.Changes.Kind() != arcset.KindMap {
		t.Fatalf("delta changes kind = %q, want map", delta.Changes.Kind())
	}
	if !hasAfterChange(delta.Changes, "@payload", arcset.TargetKindCAS) {
		t.Fatal("file map delta missing CAS @payload binding")
	}
}

func TestMutationPlanBuildsWriterMutation(t *testing.T) {
	payload := testCID(t, "payload")
	expectedRoot := testCID(t, "expected-root")
	fallbackRoot := testCID(t, "fallback-root")

	coord, err := arcset.NewMapCoordinate("@payload")
	if err != nil {
		t.Fatalf("NewMapCoordinate: %v", err)
	}
	after := arcset.NewCASTarget(payload)
	changes, err := arcset.NewCanonicalArcDelta(arcset.KindMap, []arcset.ArcChange{{
		Coordinate: coord,
		After:      &after,
	}})
	if err != nil {
		t.Fatalf("NewCanonicalArcDelta: %v", err)
	}

	plan := &unixfs.MutationPlan{
		Deltas: []unixfs.MutationDelta{{
			ExpectedRoot: expectedRoot,
			Kind:         arcset.KindMap,
			Changes:      changes,
		}},
	}
	mut := plan.WriterMutation(fallbackRoot)
	if !mut.BaseRoot.Equals(fallbackRoot) {
		t.Fatalf("BaseRoot = %s, want fallback %s", mut.BaseRoot, fallbackRoot)
	}
	if len(mut.Deltas) != 1 {
		t.Fatalf("delta count = %d, want 1", len(mut.Deltas))
	}
	if !mut.Deltas[0].ExpectedRoot.Equals(expectedRoot) {
		t.Fatalf("ExpectedRoot = %s, want %s", mut.Deltas[0].ExpectedRoot, expectedRoot)
	}
	if mut.Deltas[0].Kind != arcset.KindMap || mut.Deltas[0].Changes != changes {
		t.Fatalf("unexpected writer delta: %+v", mut.Deltas[0])
	}
}

func TestLargeFileMutationPlanIncludesFileMapAndOrderedList(t *testing.T) {
	ctx := context.Background()
	layout := newLayout(t, 4)

	root, err := layout.AddFile(ctx, cid.Undef, "blob.bin", []byte("abcdefghijkl"))
	if err != nil {
		t.Fatalf("AddFile failed: %v", err)
	}

	plan, err := layout.MutationPlanForPath(ctx, root, "blob.bin")
	if err != nil {
		t.Fatalf("MutationPlanForPath failed: %v", err)
	}
	if len(plan.Deltas) != 2 {
		t.Fatalf("delta count = %d, want 2", len(plan.Deltas))
	}
	if plan.Deltas[0].Kind != arcset.KindList {
		t.Fatalf("delta 0 kind = %q, want list", plan.Deltas[0].Kind)
	}
	if plan.Deltas[1].Kind != arcset.KindMap {
		t.Fatalf("delta 1 kind = %q, want map", plan.Deltas[1].Kind)
	}
	if !hasAfterChange(plan.Deltas[1].Changes, "@payload", arcset.TargetKindList) {
		t.Fatal("file map delta missing list @payload binding")
	}
	if !plan.Deltas[0].ExpectedRoot.Defined() {
		t.Fatal("list delta expected root is undefined")
	}
	if plan.Deltas[0].FixedList == nil {
		t.Fatal("large file list delta missing fixed-list commit metadata")
	}
	if plan.Deltas[0].FixedList.TotalSize != 12 || plan.Deltas[0].FixedList.ChunkSize != 4 {
		t.Fatalf("fixed list metadata = %+v, want total=12 chunk=4", plan.Deltas[0].FixedList)
	}

	got := changeCoordinates(plan.Deltas[0].Changes)
	want := []string{"0", "1", "2"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("list coordinates = %#v, want %#v", got, want)
	}
	for _, change := range plan.Deltas[0].Changes.Changes() {
		if change.After == nil || change.After.Kind() != arcset.TargetKindCAS {
			t.Fatalf("list change %s after target kind = %v, want cas", change.Coordinate.String(), change.After)
		}
	}

	mut := plan.WriterMutation(root)
	if !mut.Deltas[len(mut.Deltas)-1].ExpectedRoot.Equals(plan.Deltas[1].ExpectedRoot) {
		t.Fatal("writer mutation should end on the file map root, not the payload list root")
	}
}

func TestDirectoryMutationPlanSortsEntriesAndRejectsReservedPath(t *testing.T) {
	ctx := context.Background()
	layout := newLayout(t, 8)

	root, err := layout.AddFile(ctx, cid.Undef, "dir/b.txt", []byte("b"))
	if err != nil {
		t.Fatalf("AddFile(b) failed: %v", err)
	}
	root, err = layout.AddFile(ctx, root, "dir/a.txt", []byte("a"))
	if err != nil {
		t.Fatalf("AddFile(a) failed: %v", err)
	}

	if _, err := layout.MutationPlanForPath(ctx, root, "dir/@payload"); !errors.Is(err, unixfs.ErrReservedPath) {
		t.Fatalf("reserved path error = %v, want ErrReservedPath", err)
	}

	plan, err := layout.MutationPlanForPath(ctx, root, "dir")
	if err != nil {
		t.Fatalf("MutationPlanForPath(dir) failed: %v", err)
	}
	if len(plan.Deltas) != 1 {
		t.Fatalf("delta count = %d, want 1", len(plan.Deltas))
	}
	got := changeCoordinates(plan.Deltas[0].Changes)
	want := []string{"@payload", "@type", "a.txt", "b.txt"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("directory map coordinates = %#v, want %#v", got, want)
	}
}

func TestRootMutationPlanIncludesDescendantsBeforeRoot(t *testing.T) {
	ctx := context.Background()
	layout := newLayout(t, 4)

	root, err := layout.AddFile(ctx, cid.Undef, "docs/readme.txt", []byte("hello world"))
	if err != nil {
		t.Fatalf("AddFile failed: %v", err)
	}

	plan, err := layout.MutationPlanForRoot(ctx, cid.Undef, root)
	if err != nil {
		t.Fatalf("MutationPlanForRoot failed: %v", err)
	}
	if !plan.BaseRoot.Equals(cid.Undef) {
		t.Fatalf("plan BaseRoot = %s, want undefined", plan.BaseRoot)
	}
	if len(plan.Deltas) != 4 {
		t.Fatalf("delta count = %d, want 4", len(plan.Deltas))
	}
	if plan.Deltas[0].Kind != arcset.KindList {
		t.Fatalf("delta 0 kind = %q, want list payload first", plan.Deltas[0].Kind)
	}
	if plan.Deltas[len(plan.Deltas)-1].Kind != arcset.KindMap {
		t.Fatalf("last delta kind = %q, want root map", plan.Deltas[len(plan.Deltas)-1].Kind)
	}
	if !plan.Deltas[len(plan.Deltas)-1].ExpectedRoot.Equals(root) {
		t.Fatalf("last delta expected root = %s, want root %s", plan.Deltas[len(plan.Deltas)-1].ExpectedRoot, root)
	}
}

func TestRootMutationPlanPropagatesOldRootNodeTypeError(t *testing.T) {
	ctx := context.Background()
	oldTypeErr := errors.New("old root type lookup failed")
	failMap := &failingProveMap{err: oldTypeErr}
	layout := newLayoutWithMapDecorator(t, 8, newSpyBatchCAS(), func(inner mapping.Semantics) mapping.Semantics {
		failMap.Semantics = inner
		return failMap
	})

	oldRoot, err := layout.AddFile(ctx, cid.Undef, "hello.txt", []byte("old"))
	if err != nil {
		t.Fatalf("AddFile old failed: %v", err)
	}
	newRoot, err := layout.AddFile(ctx, oldRoot, "hello.txt", []byte("new"))
	if err != nil {
		t.Fatalf("AddFile new failed: %v", err)
	}
	failMap.root = oldRoot

	plan, err := layout.MutationPlanForRoot(ctx, oldRoot, newRoot)
	if err == nil {
		t.Fatalf("MutationPlanForRoot succeeded with %d deltas, want old root type error", len(plan.Deltas))
	}
	if !errors.Is(err, oldTypeErr) {
		t.Fatalf("MutationPlanForRoot error = %v, want %v", err, oldTypeErr)
	}
}

func TestRootMutationPlanFallsBackWhenOldRootNodeTypeNotFound(t *testing.T) {
	ctx := context.Background()
	failMap := &failingProveMap{err: fmt.Errorf("old root missing: %w", unixfs.ErrNotFound)}
	layout := newLayoutWithMapDecorator(t, 8, newSpyBatchCAS(), func(inner mapping.Semantics) mapping.Semantics {
		failMap.Semantics = inner
		return failMap
	})

	oldRoot, err := layout.AddFile(ctx, cid.Undef, "hello.txt", []byte("old"))
	if err != nil {
		t.Fatalf("AddFile old failed: %v", err)
	}
	newRoot, err := layout.AddFile(ctx, oldRoot, "hello.txt", []byte("new"))
	if err != nil {
		t.Fatalf("AddFile new failed: %v", err)
	}
	failMap.root = oldRoot

	plan, err := layout.MutationPlanForRoot(ctx, oldRoot, newRoot)
	if err != nil {
		t.Fatalf("MutationPlanForRoot failed: %v", err)
	}
	if len(plan.Deltas) == 0 {
		t.Fatal("MutationPlanForRoot produced no creation deltas")
	}
	for i, delta := range plan.Deltas {
		if delta.Object.Defined() {
			t.Fatalf("delta %d object = %s, want undefined creation delta object", i, delta.Object)
		}
	}
}

func TestLargeFileAppendMutationPlanReusesOldMeasuredList(t *testing.T) {
	ctx := context.Background()
	layout := newLayout(t, 4)
	oldData := strings.Repeat("a", 255*4)
	newData := oldData + "bbbb"

	oldRoot, err := layout.AddFile(ctx, cid.Undef, "blob.bin", []byte(oldData))
	if err != nil {
		t.Fatalf("AddFile old failed: %v", err)
	}
	oldResolution, err := layout.Resolve(ctx, oldRoot, "blob.bin")
	if err != nil {
		t.Fatalf("Resolve old failed: %v", err)
	}
	newRoot, err := layout.AddFile(ctx, oldRoot, "blob.bin", []byte(newData))
	if err != nil {
		t.Fatalf("AddFile new failed: %v", err)
	}
	newResolution, err := layout.Resolve(ctx, newRoot, "blob.bin")
	if err != nil {
		t.Fatalf("Resolve new failed: %v", err)
	}

	plan, err := layout.MutationPlanForRoot(ctx, oldRoot, newRoot)
	if err != nil {
		t.Fatalf("MutationPlanForRoot failed: %v", err)
	}
	var listDelta *unixfs.MutationDelta
	for i := range plan.Deltas {
		if plan.Deltas[i].Kind == arcset.KindList {
			listDelta = &plan.Deltas[i]
			break
		}
	}
	if listDelta == nil {
		t.Fatal("plan missing list delta")
	}
	if !listDelta.Object.Equals(oldResolution.Payload) {
		t.Fatalf("list delta object = %s, want old payload %s", listDelta.Object, oldResolution.Payload)
	}
	if !listDelta.ExpectedRoot.Equals(newResolution.Payload) {
		t.Fatalf("list delta expected root = %s, want new payload %s", listDelta.ExpectedRoot, newResolution.Payload)
	}
	if listDelta.FixedList == nil || listDelta.FixedList.TotalSize != uint64(len(newData)) || listDelta.FixedList.ChunkSize != 4 {
		t.Fatalf("fixed list metadata = %+v, want total=%d chunk=4", listDelta.FixedList, len(newData))
	}
	got := changeCoordinates(listDelta.Changes)
	want := []string{"255"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("list delta coordinates = %#v, want %#v", got, want)
	}
	change := listDelta.Changes.Changes()[0]
	if change.Before != nil {
		t.Fatalf("append change before = %v, want nil", change.Before)
	}
	if change.After == nil || change.After.Kind() != arcset.TargetKindCAS {
		t.Fatalf("append change after = %v, want CAS target", change.After)
	}
}

type failingProveMap struct {
	mapping.Semantics
	root cid.Cid
	err  error
}

func (m *failingProveMap) Prove(ctx context.Context, namespace string, root cid.Cid, key arcset.Path) (mapping.Binding, structure.Proof, error) {
	if m.root.Defined() && root.Equals(m.root) {
		return mapping.Binding{}, nil, m.err
	}
	return m.Semantics.Prove(ctx, namespace, root, key)
}

func hasAfterChange(delta *arcset.CanonicalArcDelta, coordinate string, targetKind arcset.TargetKind) bool {
	for _, change := range delta.Changes() {
		if change.Coordinate.String() == coordinate && change.After != nil && change.After.Kind() == targetKind {
			return true
		}
	}
	return false
}

func changeCoordinates(delta *arcset.CanonicalArcDelta) []string {
	changes := delta.Changes()
	out := make([]string, 0, len(changes))
	for _, change := range changes {
		out = append(out, change.Coordinate.String())
	}
	return out
}

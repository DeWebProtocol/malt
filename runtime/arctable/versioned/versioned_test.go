package versioned

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt/auth/arcset"
	arctablepkg "github.com/dewebprotocol/malt/runtime/arctable"
	"github.com/dewebprotocol/malt/runtime/arctable/bloom"
	"github.com/dewebprotocol/malt/storage/kv"
	"github.com/dewebprotocol/malt/storage/kv/memory"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

type getErrorKV struct {
	kvstore.KVStore
	failKey string
	err     error
}

func (kv *getErrorKV) Get(ctx context.Context, key []byte) ([]byte, error) {
	if string(key) == kv.failKey {
		return nil, kv.err
	}
	return kv.KVStore.Get(ctx, key)
}

func newTestCID(data []byte) cid.Cid {
	mhash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cid.Raw, mhash)
}

func testPathSlice(paths []string) []arcset.Path {
	out := make([]arcset.Path, len(paths))
	for i, path := range paths {
		out[i] = arcset.CanonicalizePath(path)
	}
	return out
}

// === Versioned ArcTable Tests ===

func TestVersionedArcTableNew(t *testing.T) {
	kv := memory.New()

	// Valid creation
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}
	if arctable == nil {
		t.Error("arctable should not be nil")
	}

	// Nil KVStore
	_, err = NewArcTable()
	if err == nil {
		t.Error("expected error for nil KVStore")
	}
}

func TestVersionedArcTableUpdate(t *testing.T) {
	kv := memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	namespace := "versioned-graph"
	root1 := newTestCID([]byte("root1"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// Create first version (no parent)
	arcs1 := map[string]cid.Cid{
		"a": target1,
		"b": target2,
	}
	err = arctable.Update(ctx, namespace, root1, cid.Undef, arcset.NewSetFrom(arcs1))
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Get at root1
	got, err := arctable.Get(ctx, namespace, root1, arcset.CanonicalizePath("a"))
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !got.Equals(target1) {
		t.Error("wrong value for 'a'")
	}

	got, err = arctable.Get(ctx, namespace, root1, arcset.CanonicalizePath("b"))
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("wrong value for 'b'")
	}
}

func TestVersionedArcTableVersionChain(t *testing.T) {
	kv := memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	namespace := "chain-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	root3 := newTestCID([]byte("root3"))

	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))
	target3 := newTestCID([]byte("target3"))

	// Version 1: a -> target1, b -> target2
	arcs1 := map[string]cid.Cid{
		"a": target1,
		"b": target2,
	}
	err = arctable.Update(ctx, namespace, root1, cid.Undef, arcset.NewSetFrom(arcs1))
	if err != nil {
		t.Fatalf("Update v1 failed: %v", err)
	}

	// Version 2: a -> target3 (override), b unchanged
	arcs2 := map[string]cid.Cid{
		"a": target3,
	}
	err = arctable.Update(ctx, namespace, root2, root1, arcset.NewSetFrom(arcs2))
	if err != nil {
		t.Fatalf("Update v2 failed: %v", err)
	}

	// Version 3: c -> target3 (new), a and b unchanged
	arcs3 := map[string]cid.Cid{
		"c": target3,
	}
	err = arctable.Update(ctx, namespace, root3, root2, arcset.NewSetFrom(arcs3))
	if err != nil {
		t.Fatalf("Update v3 failed: %v", err)
	}

	// Test resolution at root3

	// a should resolve to target3 (overridden at v2)
	got, err := arctable.Get(ctx, namespace, root3, arcset.CanonicalizePath("a"))
	if err != nil {
		t.Fatalf("Get a at root3 failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("a at root3 should be target3")
	}

	// b should resolve to target2 (from v1)
	got, err = arctable.Get(ctx, namespace, root3, arcset.CanonicalizePath("b"))
	if err != nil {
		t.Fatalf("Get b at root3 failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("b at root3 should be target2")
	}

	// c should resolve to target3 (new at v3)
	got, err = arctable.Get(ctx, namespace, root3, arcset.CanonicalizePath("c"))
	if err != nil {
		t.Fatalf("Get c at root3 failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("c at root3 should be target3")
	}

	// Test resolution at root2

	// a at root2 should be target3
	got, err = arctable.Get(ctx, namespace, root2, arcset.CanonicalizePath("a"))
	if err != nil {
		t.Fatalf("Get a at root2 failed: %v", err)
	}
	if !got.Equals(target3) {
		t.Error("a at root2 should be target3")
	}

	// b at root2 should be target2
	got, err = arctable.Get(ctx, namespace, root2, arcset.CanonicalizePath("b"))
	if err != nil {
		t.Fatalf("Get b at root2 failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("b at root2 should be target2")
	}

	// c at root2 should not exist
	_, err = arctable.Get(ctx, namespace, root2, arcset.CanonicalizePath("c"))
	if err == nil {
		t.Error("c at root2 should not exist")
	}

	// Test resolution at root1

	got, err = arctable.Get(ctx, namespace, root1, arcset.CanonicalizePath("a"))
	if err != nil {
		t.Fatalf("Get a at root1 failed: %v", err)
	}
	if !got.Equals(target1) {
		t.Error("a at root1 should be target1")
	}
}

func TestVersionedArcTableGetParent(t *testing.T) {
	kv := memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	namespace := "parent-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))

	arcs1 := map[string]cid.Cid{
		"a": newTestCID([]byte("target1")),
	}
	arctable.Update(ctx, namespace, root1, cid.Undef, arcset.NewSetFrom(arcs1))

	arcs2 := map[string]cid.Cid{
		"b": newTestCID([]byte("target2")),
	}
	arctable.Update(ctx, namespace, root2, root1, arcset.NewSetFrom(arcs2))

	// GetParent
	parent, err := arctable.GetParent(ctx, namespace, root2)
	if err != nil {
		t.Fatalf("GetParent failed: %v", err)
	}
	if !parent.Equals(root1) {
		t.Error("parent of root2 should be root1")
	}

	// First version has no parent
	parent, err = arctable.GetParent(ctx, namespace, root1)
	if err != nil {
		t.Fatalf("GetParent root1 failed: %v", err)
	}
	if parent != cid.Undef {
		t.Error("root1 should have no parent")
	}
}

func TestVersionedArcTableSnapshot(t *testing.T) {
	kv := memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	namespace := "snapshot-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))

	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	arcs1 := map[string]cid.Cid{
		"a": target1,
	}
	arctable.Update(ctx, namespace, root1, cid.Undef, arcset.NewSetFrom(arcs1))

	arcs2 := map[string]cid.Cid{
		"b": target2,
	}
	arctable.Update(ctx, namespace, root2, root1, arcset.NewSetFrom(arcs2))

	// Snapshot at root2
	snapshot, err := arctable.Snapshot(ctx, namespace, root2)
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	got, ok := snapshot.Get(arcset.CanonicalizePath("a"))
	if !ok {
		t.Error("expected to find 'a' at root2 snapshot")
	}
	if !got.Equals(target1) {
		t.Error("wrong value for 'a'")
	}

	got, ok = snapshot.Get(arcset.CanonicalizePath("b"))
	if !ok {
		t.Error("expected to find 'b' at root2 snapshot")
	}
	if !got.Equals(target2) {
		t.Error("wrong value for 'b'")
	}

	// Len
	if snapshot.Len() != 2 {
		t.Errorf("expected Len 2, got %d", snapshot.Len())
	}
}

func TestVersionedArcTableBatchGet(t *testing.T) {
	kv := memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	namespace := "batchget-graph"
	root1 := newTestCID([]byte("root1"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))
	target3 := newTestCID([]byte("target3"))

	// Setup arcs at root1
	arctable.Update(ctx, namespace, root1, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{
		"a": target1,
		"b": target2,
		"c": target3,
	}))

	// Test: all paths found
	results, err := arctable.BatchGet(ctx, namespace, root1, testPathSlice([]string{"a", "b", "c"}))
	if err != nil {
		t.Fatalf("BatchGet failed: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results, got %d", len(results))
	}
	if !results["a"].Equals(target1) {
		t.Error("wrong value for 'a'")
	}
	if !results["b"].Equals(target2) {
		t.Error("wrong value for 'b'")
	}
	if !results["c"].Equals(target3) {
		t.Error("wrong value for 'c'")
	}

	// Test: some paths not found
	results, err = arctable.BatchGet(ctx, namespace, root1, testPathSlice([]string{"a", "notexist", "b"}))
	if err != nil {
		t.Fatalf("BatchGet with missing paths failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results (missing path omitted), got %d", len(results))
	}

	// Test: empty paths
	results, err = arctable.BatchGet(ctx, namespace, root1, testPathSlice([]string{}))
	if err != nil {
		t.Fatalf("BatchGet with empty paths failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty paths, got %d", len(results))
	}

	// Test: all paths not found
	results, err = arctable.BatchGet(ctx, namespace, root1, testPathSlice([]string{"x", "y", "z"}))
	if err != nil {
		t.Fatalf("BatchGet with all missing paths failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestVersionedArcTableBatchGetVersionChain(t *testing.T) {
	kv := memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	namespace := "batchget-chain-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	root3 := newTestCID([]byte("root3"))

	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))
	target3 := newTestCID([]byte("target3"))
	target4 := newTestCID([]byte("target4"))

	// v1: a, b
	arctable.Update(ctx, namespace, root1, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{
		"a": target1,
		"b": target2,
	}))

	// v2: c (new), a overridden
	arctable.Update(ctx, namespace, root2, root1, arcset.NewSetFrom(map[string]cid.Cid{
		"a": target3,
		"c": target3,
	}))

	// v3: d (new)
	arctable.Update(ctx, namespace, root3, root2, arcset.NewSetFrom(map[string]cid.Cid{
		"d": target4,
	}))

	// BatchGet at root3 should find all paths
	results, err := arctable.BatchGet(ctx, namespace, root3, testPathSlice([]string{"a", "b", "c", "d"}))
	if err != nil {
		t.Fatalf("BatchGet root3 failed: %v", err)
	}
	if len(results) != 4 {
		t.Errorf("expected 4 results, got %d", len(results))
	}

	// a should be target3 (overridden at v2)
	if !results["a"].Equals(target3) {
		t.Error("'a' should be target3 (overridden)")
	}

	// b should be target2 (from v1)
	if !results["b"].Equals(target2) {
		t.Error("'b' should be target2 (from v1)")
	}

	// c should be target3 (from v2)
	if !results["c"].Equals(target3) {
		t.Error("'c' should be target3 (from v2)")
	}

	// d should be target4 (from v3)
	if !results["d"].Equals(target4) {
		t.Error("'d' should be target4 (from v3)")
	}

	// BatchGet at root2 should not find 'd'
	results, err = arctable.BatchGet(ctx, namespace, root2, testPathSlice([]string{"a", "b", "c", "d"}))
	if err != nil {
		t.Fatalf("BatchGet root2 failed: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results at root2, got %d", len(results))
	}
	if results["d"] != cid.Undef {
		t.Error("'d' should not be found at root2")
	}

	// BatchGet at root1 should find original 'a'
	results, err = arctable.BatchGet(ctx, namespace, root1, testPathSlice([]string{"a", "b", "c"}))
	if err != nil {
		t.Fatalf("BatchGet root1 failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results at root1, got %d", len(results))
	}
	if !results["a"].Equals(target1) {
		t.Error("'a' at root1 should be target1 (original)")
	}
	if !results["b"].Equals(target2) {
		t.Error("'b' at root1 should be target2")
	}
	if results["c"] != cid.Undef {
		t.Error("'c' should not be found at root1")
	}
}

func TestVersionedArcTableBatchGetWithTombstone(t *testing.T) {
	kv := memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	namespace := "batchget-tombstone-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	root3 := newTestCID([]byte("root3"))

	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))
	target3 := newTestCID([]byte("target3"))

	// v1: a, b, c
	arctable.Update(ctx, namespace, root1, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{
		"a": target1,
		"b": target2,
		"c": target3,
	}))

	// v2: delete 'a' (tombstone), add 'd'
	arctable.Update(ctx, namespace, root2, root1, arcset.NewSetFrom(map[string]cid.Cid{
		"a": cid.Undef, // tombstone
		"d": target3,
	}))

	// v3: delete 'b' (tombstone)
	arctable.Update(ctx, namespace, root3, root2, arcset.NewSetFrom(map[string]cid.Cid{
		"b": cid.Undef, // tombstone
	}))

	// BatchGet at root3: a and b deleted, c and d exist
	results, err := arctable.BatchGet(ctx, namespace, root3, testPathSlice([]string{"a", "b", "c", "d"}))
	if err != nil {
		t.Fatalf("BatchGet root3 failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results (a,b deleted), got %d", len(results))
	}
	if results["a"] != cid.Undef {
		t.Error("'a' should be deleted (tombstone)")
	}
	if results["b"] != cid.Undef {
		t.Error("'b' should be deleted (tombstone)")
	}
	if !results["c"].Equals(target3) {
		t.Error("'c' should still exist")
	}
	if !results["d"].Equals(target3) {
		t.Error("'d' should exist")
	}

	// BatchGet at root2: only 'a' deleted
	results, err = arctable.BatchGet(ctx, namespace, root2, testPathSlice([]string{"a", "b", "c", "d"}))
	if err != nil {
		t.Fatalf("BatchGet root2 failed: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results at root2, got %d", len(results))
	}
	if results["a"] != cid.Undef {
		t.Error("'a' should be deleted at root2")
	}
	if !results["b"].Equals(target2) {
		t.Error("'b' should still exist at root2")
	}

	// BatchGet at root1: all exist
	results, err = arctable.BatchGet(ctx, namespace, root1, testPathSlice([]string{"a", "b", "c"}))
	if err != nil {
		t.Fatalf("BatchGet root1 failed: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results at root1, got %d", len(results))
	}
	if !results["a"].Equals(target1) {
		t.Error("'a' should exist at root1")
	}
}

func TestVersionedArcTableBatchGetMultipleNamespaces(t *testing.T) {
	kv := memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	root1a := newTestCID([]byte("root1a"))
	root1b := newTestCID([]byte("root1b"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// Different namespaces
	arctable.Update(ctx, "namespace1", root1a, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{
		"a": target1,
		"b": target1,
	}))
	arctable.Update(ctx, "namespace2", root1b, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{
		"a": target2,
		"b": target2,
	}))

	// BatchGet in different namespaces should be independent
	results1, _ := arctable.BatchGet(ctx, "namespace1", root1a, testPathSlice([]string{"a", "b"}))
	results2, _ := arctable.BatchGet(ctx, "namespace2", root1b, testPathSlice([]string{"a", "b"}))

	if len(results1) != 2 || len(results2) != 2 {
		t.Error("expected 2 results in each namespace")
	}

	if !results1["a"].Equals(target1) {
		t.Error("namespace1 should have target1")
	}
	if !results2["a"].Equals(target2) {
		t.Error("namespace2 should have target2")
	}
}

func TestVersionedArcTableBatchGetParentReadError(t *testing.T) {
	kv := memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	namespace := "batchget-parent-error-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	if err := arctable.Update(ctx, namespace, root1, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{
		"a": target1,
	})); err != nil {
		t.Fatalf("Update root1 failed: %v", err)
	}
	if err := arctable.Update(ctx, namespace, root2, root1, arcset.NewSetFrom(map[string]cid.Cid{
		"b": target2,
	})); err != nil {
		t.Fatalf("Update root2 failed: %v", err)
	}

	parentErr := errors.New("injected parent read failure")
	prevKey := arctablepkg.VersionedArcKey(namespace, root2, PreviousArc)
	failingTable, err := NewArcTable(WithKVStore(&getErrorKV{
		KVStore: kv,
		failKey: string(prevKey),
		err:     parentErr,
	}))
	if err != nil {
		t.Fatalf("NewArcTable with failing kv failed: %v", err)
	}

	results, err := failingTable.BatchGet(ctx, namespace, root2, testPathSlice([]string{"a"}))
	if !errors.Is(err, parentErr) {
		t.Fatalf("BatchGet error = %v, want parent read error", err)
	}
	if results != nil {
		t.Fatalf("BatchGet results = %v, want nil on parent read error", results)
	}
}

func TestVersionedArcTableBatchGetInvalidParentCID(t *testing.T) {
	kv := memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	namespace := "batchget-invalid-parent-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	if err := arctable.Update(ctx, namespace, root1, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{
		"a": target1,
	})); err != nil {
		t.Fatalf("Update root1 failed: %v", err)
	}
	if err := arctable.Update(ctx, namespace, root2, root1, arcset.NewSetFrom(map[string]cid.Cid{
		"b": target2,
	})); err != nil {
		t.Fatalf("Update root2 failed: %v", err)
	}

	prevKey := arctablepkg.VersionedArcKey(namespace, root2, PreviousArc)
	if err := kv.Put(ctx, prevKey, []byte("not-a-cid")); err != nil {
		t.Fatalf("corrupt @previous failed: %v", err)
	}

	results, err := arctable.BatchGet(ctx, namespace, root2, testPathSlice([]string{"a"}))
	if err == nil {
		t.Fatal("BatchGet succeeded, want invalid parent CID error")
	}
	if !strings.Contains(err.Error(), "invalid @previous CID") {
		t.Fatalf("BatchGet error = %v, want invalid @previous CID", err)
	}
	if results != nil {
		t.Fatalf("BatchGet results = %v, want nil on invalid parent CID", results)
	}
}

func TestVersionedArcTableDeleteViaUpdate(t *testing.T) {
	kv := memory.New()
	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	namespace := "delete-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	root3 := newTestCID([]byte("root3"))

	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// v1: a, b
	arcs1 := map[string]cid.Cid{
		"a": target1,
		"b": target2,
	}
	arctable.Update(ctx, namespace, root1, cid.Undef, arcset.NewSetFrom(arcs1))

	// v2: delete 'a' using cid.Undef (tombstone)
	arcs2 := map[string]cid.Cid{
		"a": cid.Undef, // tombstone - marks 'a' as deleted
	}
	arctable.Update(ctx, namespace, root2, root1, arcset.NewSetFrom(arcs2))

	// At root2, 'a' should not be found (tombstone stops the search)
	_, err = arctable.Get(ctx, namespace, root2, arcset.CanonicalizePath("a"))
	if err == nil {
		t.Error("'a' should be deleted at root2")
	}

	// 'b' should still be accessible (from root1)
	got, err := arctable.Get(ctx, namespace, root2, arcset.CanonicalizePath("b"))
	if err != nil {
		t.Fatalf("Get b at root2 failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("b at root2 should be target2")
	}

	// At root1, 'a' should still exist (tombstone is at root2, not root1)
	got, err = arctable.Get(ctx, namespace, root1, arcset.CanonicalizePath("a"))
	if err != nil {
		t.Fatalf("Get a at root1 failed: %v", err)
	}
	if !got.Equals(target1) {
		t.Error("a at root1 should be target1")
	}

	// v3: add 'c', tombstone for 'a' should still be effective
	arcs3 := map[string]cid.Cid{
		"c": target1,
	}
	arctable.Update(ctx, namespace, root3, root2, arcset.NewSetFrom(arcs3))

	// At root3, 'a' should still not be found
	_, err = arctable.Get(ctx, namespace, root3, arcset.CanonicalizePath("a"))
	if err == nil {
		t.Error("'a' should be deleted at root3")
	}

	// 'b' and 'c' should work
	got, err = arctable.Get(ctx, namespace, root3, arcset.CanonicalizePath("b"))
	if err != nil {
		t.Fatalf("Get b at root3 failed: %v", err)
	}
	if !got.Equals(target2) {
		t.Error("b at root3 should be target2")
	}

	got, err = arctable.Get(ctx, namespace, root3, arcset.CanonicalizePath("c"))
	if err != nil {
		t.Fatalf("Get c at root3 failed: %v", err)
	}
	if !got.Equals(target1) {
		t.Error("c at root3 should be target1")
	}
}

func TestVersionedArcTableMultipleNamespaces(t *testing.T) {
	kv := memory.New() // Shared KVStore

	arctable, err := NewArcTable(WithKVStore(kv))
	if err != nil {
		t.Fatalf("NewArcTable failed: %v", err)
	}

	ctx := context.Background()
	root1a := newTestCID([]byte("root1a"))
	root2a := newTestCID([]byte("root2a"))

	target1 := newTestCID([]byte("target1"))
	target2 := newTestCID([]byte("target2"))

	// Create versions in different namespaces
	arctable.Update(ctx, "namespace1", root1a, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{"a": target1}))
	arctable.Update(ctx, "namespace2", root2a, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{"a": target2}))

	// Should be independent
	got1, _ := arctable.Get(ctx, "namespace1", root1a, arcset.CanonicalizePath("a"))
	got2, _ := arctable.Get(ctx, "namespace2", root2a, arcset.CanonicalizePath("a"))

	if got1.Equals(got2) {
		t.Error("different namespaces should have independent values")
	}

	if !got1.Equals(target1) {
		t.Error("namespace1 should have target1")
	}

	if !got2.Equals(target2) {
		t.Error("namespace2 should have target2")
	}
}

// === Bloom Filter Tests ===

func TestVersionedArcTableWithBloomCache(t *testing.T) {
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	arctable, err := NewArcTableWithBloomCache(kv, bc)
	if err != nil {
		t.Fatalf("NewArcTableWithBloomCache failed: %v", err)
	}

	ctx := context.Background()
	namespace := "bloom-graph"
	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))

	// Create namespace
	err = arctable.CreateNamespace(ctx, namespace, &bloom.NamespaceConfig{
		ExpectedItems:     1000,
		FalsePositiveRate: 0.01,
	})
	if err != nil {
		t.Fatalf("CreateNamespace failed: %v", err)
	}

	// Add arc
	arctable.Update(ctx, namespace, root, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{"path/a": target}))

	// MightContain should return true for existing path
	if !arctable.MightContain(ctx, namespace, arcset.CanonicalizePath("path/a")) {
		t.Error("MightContain should return true for existing path")
	}
}

func TestVersionedArcTableMightContainBatch(t *testing.T) {
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	arctable, _ := NewArcTableWithBloomCache(kv, bc)

	ctx := context.Background()
	namespace := "batch-bloom-graph"
	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))

	// Create namespace
	arctable.CreateNamespace(ctx, namespace, nil)

	// Add arcs
	paths := []string{"a", "b", "c"}
	arcs := make(map[string]cid.Cid)
	for _, p := range paths {
		arcs[p] = target
	}
	arctable.Update(ctx, namespace, root, cid.Undef, arcset.NewSetFrom(arcs))

	// Batch check
	results := arctable.MightContainBatch(ctx, namespace, testPathSlice([]string{"a", "b", "c", "nonexistent"}))
	if len(results) != 4 {
		t.Errorf("expected 4 results, got %d", len(results))
	}

	// Existing paths should return true
	for _, p := range paths {
		if !results[arcset.CanonicalizePath(p)] {
			t.Errorf("expected true for %s", p)
		}
	}
}

func TestVersionedArcTableBloomFilterOptimization(t *testing.T) {
	// Test that bloom filter actually skips kvstore lookup for non-existent paths
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	arctable, _ := NewArcTableWithBloomCache(kv, bc)

	ctx := context.Background()
	namespace := "optimized-graph"
	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))

	// Create namespace
	arctable.CreateNamespace(ctx, namespace, nil)

	// Add arcs
	arctable.Update(ctx, namespace, root, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{"existing": target}))

	// Get for existing path should work
	got, err := arctable.Get(ctx, namespace, root, arcset.CanonicalizePath("existing"))
	if err != nil {
		t.Fatalf("Get existing failed: %v", err)
	}
	if !got.Equals(target) {
		t.Error("wrong value")
	}

	// Get for path that definitely doesn't exist (bloom says no)
	// should return ErrNotFound without version chain walk
	_, err = arctable.Get(ctx, namespace, root, arcset.CanonicalizePath("definitely-not-exist"))
	if err == nil {
		t.Error("expected error for non-existent path")
	}
}

func TestVersionedArcTableBloomUpdateOnUpdate(t *testing.T) {
	// Test that bloom filter is updated when arcs are added
	kv := memory.New()
	bc := bloom.NewBloomCache(kv, 100)
	arctable, _ := NewArcTableWithBloomCache(kv, bc)

	ctx := context.Background()
	namespace := "update-bloom-graph"
	root1 := newTestCID([]byte("root1"))
	root2 := newTestCID([]byte("root2"))
	target := newTestCID([]byte("target"))

	// Create namespace
	arctable.CreateNamespace(ctx, namespace, nil)

	// First update
	arctable.Update(ctx, namespace, root1, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{"a": target}))

	// Second update (adds new paths)
	arctable.Update(ctx, namespace, root2, root1, arcset.NewSetFrom(map[string]cid.Cid{"b": target}))

	// Both paths should be in bloom
	if !arctable.MightContain(ctx, namespace, arcset.CanonicalizePath("a")) {
		t.Error("'a' should be in bloom")
	}
	if !arctable.MightContain(ctx, namespace, arcset.CanonicalizePath("b")) {
		t.Error("'b' should be in bloom")
	}
}

func TestVersionedArcTableWithoutBloomCache(t *testing.T) {
	kv := memory.New()
	arctable, _ := NewArcTable(WithKVStore(kv))

	ctx := context.Background()
	namespace := "no-bloom-graph"
	root := newTestCID([]byte("root"))
	target := newTestCID([]byte("target"))

	// Add arc
	arctable.Update(ctx, namespace, root, cid.Undef, arcset.NewSetFrom(map[string]cid.Cid{"a": target}))

	// CreateNamespace should fail (no bloom cache)
	err := arctable.CreateNamespace(ctx, namespace, nil)
	if err == nil {
		t.Error("CreateNamespace should fail without bloom cache")
	}

	// MightContain should return true (bloom disabled)
	if !arctable.MightContain(ctx, namespace, arcset.CanonicalizePath("any-path")) {
		t.Error("MightContain should return true when bloom disabled")
	}

	// MightContainBatch should return all true
	results := arctable.MightContainBatch(ctx, namespace, testPathSlice([]string{"a", "b", "c"}))
	for p, v := range results {
		if !v {
			t.Errorf("expected true for %s when bloom disabled", p)
		}
	}
}

// === Benchmarks ===

// setupVersionChain creates a chain of versions and returns the latest root
func setupVersionChain(ctx context.Context, arctable *ArcTable, namespace string, chainLength int) cid.Cid {
	var prevRoot cid.Cid
	var latestRoot cid.Cid

	for i := 0; i < chainLength; i++ {
		root := newTestCID([]byte(fmt.Sprintf("root%d", i)))
		arcs := map[string]cid.Cid{
			fmt.Sprintf("v%d_arc", i): newTestCID([]byte(fmt.Sprintf("target%d", i))),
		}
		arctable.Update(ctx, namespace, root, prevRoot, arcset.NewSetFrom(arcs))
		prevRoot = root
		latestRoot = root
	}
	return latestRoot
}

func BenchmarkVersionedArcTableGet(b *testing.B) {
	kv := memory.New()
	arctable, _ := NewArcTable(WithKVStore(kv))
	ctx := context.Background()
	namespace := "bench-graph"

	// Test different version chain lengths
	chainLengths := []int{1, 10, 50, 100}
	for _, length := range chainLengths {
		b.Run(fmt.Sprintf("chain_%d", length), func(b *testing.B) {
			latestRoot := setupVersionChain(ctx, arctable, namespace, length)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Query an arc that exists at the first version (requires full chain walk)
				arctable.Get(ctx, namespace, latestRoot, arcset.CanonicalizePath("v0_arc"))
			}
		})
	}
}

func BenchmarkVersionedArcTableGetLatestVersion(b *testing.B) {
	kv := memory.New()
	arctable, _ := NewArcTable(WithKVStore(kv))
	ctx := context.Background()
	namespace := "bench-graph"

	// Test Get performance for arc at latest version (no chain walk needed)
	chainLengths := []int{1, 10, 50, 100}
	for _, length := range chainLengths {
		b.Run(fmt.Sprintf("chain_%d", length), func(b *testing.B) {
			latestRoot := setupVersionChain(ctx, arctable, namespace, length)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				// Query an arc at the latest version (direct lookup)
				arctable.Get(ctx, namespace, latestRoot, arcset.CanonicalizePath(fmt.Sprintf("v%d_arc", length-1)))
			}
		})
	}
}

func BenchmarkVersionedArcTableUpdate(b *testing.B) {
	kv := memory.New()
	arctable, _ := NewArcTable(WithKVStore(kv))
	ctx := context.Background()
	namespace := "bench-graph"

	batchSizes := []int{1, 10, 100}
	for _, size := range batchSizes {
		b.Run(fmt.Sprintf("batch_%d", size), func(b *testing.B) {
			// Initial setup
			initialRoot := newTestCID([]byte("initial"))
			initialArcs := map[string]cid.Cid{
				"a": newTestCID([]byte("init")),
			}
			arctable.Update(ctx, namespace, initialRoot, cid.Undef, arcset.NewSetFrom(initialArcs))

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				newRoot := newTestCID([]byte(fmt.Sprintf("root%d", i)))
				arcs := make(map[string]cid.Cid)
				for j := 0; j < size; j++ {
					arcs[fmt.Sprintf("arc%d", j)] = newTestCID([]byte(fmt.Sprintf("val%d_%d", i, j)))
				}
				arctable.Update(ctx, namespace, newRoot, initialRoot, arcset.NewSetFrom(arcs))
			}
		})
	}
}

func BenchmarkVersionedArcTableSnapshot(b *testing.B) {
	kv := memory.New()
	arctable, _ := NewArcTable(WithKVStore(kv))
	ctx := context.Background()
	namespace := "bench-graph"

	chainLengths := []int{1, 10, 20}
	for _, length := range chainLengths {
		b.Run(fmt.Sprintf("chain_%d", length), func(b *testing.B) {
			latestRoot := setupVersionChain(ctx, arctable, namespace, length)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				snapshot, _ := arctable.Snapshot(ctx, namespace, latestRoot)
				snapshot.Get(arcset.CanonicalizePath("v0_arc"))
			}
		})
	}
}

func BenchmarkVersionedArcTableIterate(b *testing.B) {
	kv := memory.New()
	arctable, _ := NewArcTable(WithKVStore(kv))
	ctx := context.Background()
	namespace := "bench-graph"

	chainLengths := []int{1, 10, 20}
	for _, length := range chainLengths {
		b.Run(fmt.Sprintf("chain_%d", length), func(b *testing.B) {
			latestRoot := setupVersionChain(ctx, arctable, namespace, length)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				iter := arctable.Iterate(ctx, namespace, latestRoot)
				for {
					_, _, ok := iter.Next()
					if !ok {
						break
					}
				}
				iter.Close()
			}
		})
	}
}

func BenchmarkVersionedArcTableGetParent(b *testing.B) {
	kv := memory.New()
	arctable, _ := NewArcTable(WithKVStore(kv))
	namespace := "bench-graph"

	chainLengths := []int{10, 50, 100}
	for _, length := range chainLengths {
		b.Run(fmt.Sprintf("chain_%d", length), func(b *testing.B) {
			ctx := context.Background()
			latestRoot := setupVersionChain(ctx, arctable, namespace, length)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				arctable.GetParent(ctx, namespace, latestRoot)
			}
		})
	}
}

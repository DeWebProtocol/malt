package radix_test

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"testing"
	"time"

	"github.com/dewebprotocol/malt/auth/arcset"
	materializer "github.com/dewebprotocol/malt/auth/arcset/materializer"
	materialmemory "github.com/dewebprotocol/malt/auth/arcset/materializer/memory"
	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/auth/commitment/ipa"
	"github.com/dewebprotocol/malt/auth/commitment/kzg"
	"github.com/dewebprotocol/malt/auth/semantic/mapping"
	mappingradix "github.com/dewebprotocol/malt/auth/semantic/mapping/radix"
	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

type schemeFactory func(t *testing.T) commitment.IndexCommitment

const testNamespace = "map-radix-semantic-test"

func mappingSchemes() map[string]schemeFactory {
	return map[string]schemeFactory{
		"ipa": func(t *testing.T) commitment.IndexCommitment {
			t.Helper()
			scheme, err := ipa.NewScheme()
			if err != nil {
				t.Fatalf("ipa.NewScheme failed: %v", err)
			}
			return scheme
		},
		"kzg": func(t *testing.T) commitment.IndexCommitment {
			t.Helper()
			scheme, err := kzg.NewScheme()
			if err != nil {
				t.Fatalf("kzg.NewScheme failed: %v", err)
			}
			return scheme
		},
	}
}

func newMap(t *testing.T, factory schemeFactory, store *materialmemory.Store) mapping.Semantics {
	t.Helper()
	if store == nil {
		store = materialmemory.New(true)
	}
	semantic, err := mappingradix.NewMap(factory(t), store)
	if err != nil {
		t.Fatalf("radix.NewMap failed: %v", err)
	}
	return semantic
}

func fakeCID(seed string) cid.Cid {
	sum, err := mh.Sum([]byte(seed), mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cid.Raw, sum)
}

func TestMapCommitProveVerify(t *testing.T) {
	ctx := context.Background()
	view := mapping.NewViewFrom(map[string]cid.Cid{
		"b/c":      fakeCID("value-bc"),
		"a":        fakeCID("value-a"),
		"@payload": fakeCID("value-payload"),
	})

	for name, factory := range mappingSchemes() {
		t.Run(name, func(t *testing.T) {
			semantic := newMap(t, factory, nil)

			root, err := semantic.Commit(ctx, testNamespace, view)
			if err != nil {
				t.Fatalf("Commit failed: %v", err)
			}

			key := arcset.CanonicalizePath("b/c")
			binding, proof, err := semantic.Prove(ctx, testNamespace, root, key)
			if err != nil {
				t.Fatalf("Prove failed: %v", err)
			}
			if !binding.Present {
				t.Fatal("expected membership binding")
			}
			if !binding.Value.Equals(fakeCID("value-bc")) {
				t.Fatalf("binding value mismatch: %s", binding.Value)
			}

			ok, err := semantic.Verify(root, key, binding, proof)
			if err != nil {
				t.Fatalf("Verify failed: %v", err)
			}
			if !ok {
				t.Fatal("expected proof to verify")
			}

			ok, err = semantic.Verify(root, arcset.CanonicalizePath("a"), binding, proof)
			if err == nil && ok {
				t.Fatal("expected proof to be path-bound")
			}
		})
	}
}

func TestProveWithTimingsExcludesArcSetLoadTime(t *testing.T) {
	ctx := context.Background()
	base := materialmemory.New(true)
	loadDelay := 120 * time.Millisecond
	openDelay := 5 * time.Millisecond
	table := delayedMaterializer{inner: base, delay: loadDelay}
	semantic, err := mappingradix.NewMap(fakeTimingScheme{proveDelay: openDelay}, table)
	if err != nil {
		t.Fatalf("radix.NewMap failed: %v", err)
	}
	view := mapping.NewViewFrom(map[string]cid.Cid{
		"a": fakeCID("value-a"),
		"b": fakeCID("value-b"),
	})
	root, err := semantic.Commit(ctx, testNamespace+"-timing", view)
	if err != nil {
		t.Fatalf("Commit failed: %v", err)
	}

	binding, proof, timings, err := semantic.ProveWithTimings(ctx, testNamespace+"-timing", root, arcset.CanonicalizePath("b"))
	if err != nil {
		t.Fatalf("ProveWithTimings failed: %v", err)
	}
	if !binding.Present || !binding.Value.Equals(fakeCID("value-b")) || len(proof) == 0 {
		t.Fatalf("proof result = binding %+v proof bytes %d", binding, len(proof))
	}
	if timings.LoadElapsedNS < int64(loadDelay) {
		t.Fatalf("LoadElapsedNS = %d, want at least injected load delay %s", timings.LoadElapsedNS, loadDelay)
	}
	if timings.OpenElapsedNS <= 0 {
		t.Fatalf("OpenElapsedNS = %d, want positive open/prove time", timings.OpenElapsedNS)
	}
	if timings.OpenElapsedNS >= int64(loadDelay/2) {
		t.Fatalf("OpenElapsedNS = %d includes too much load delay, want below %s", timings.OpenElapsedNS, loadDelay/2)
	}
	if timings.OpenCount != 1 {
		t.Fatalf("OpenCount = %d, want one radix open at this cardinality", timings.OpenCount)
	}
}

func TestMapUpdateReplaceInsertDelete(t *testing.T) {
	ctx := context.Background()
	initialEntries := map[string]cid.Cid{
		"a": fakeCID("value-a"),
		"c": fakeCID("value-c"),
	}

	for name, factory := range mappingSchemes() {
		t.Run(name, func(t *testing.T) {
			semantic := newMap(t, factory, nil)
			initialView := mapping.NewViewFrom(initialEntries)

			root, err := semantic.Commit(ctx, testNamespace, initialView)
			if err != nil {
				t.Fatalf("Commit(initial) failed: %v", err)
			}

			replacement := fakeCID("value-c2")
			replacedRoot, err := semantic.Update(
				ctx,
				testNamespace,
				root,
				arcset.CanonicalizePath("c"),
				initialEntries["c"],
				replacement,
			)
			if err != nil {
				t.Fatalf("Update(replace) failed: %v", err)
			}

			replacedView := mapping.NewViewFrom(map[string]cid.Cid{
				"a": initialEntries["a"],
				"c": replacement,
			})
			expectedReplacedRoot, err := semantic.Commit(ctx, testNamespace, replacedView)
			if err != nil {
				t.Fatalf("Commit(replaced) failed: %v", err)
			}
			if !replacedRoot.Equals(expectedReplacedRoot) {
				t.Fatalf("replace root mismatch: got %s want %s", replacedRoot, expectedReplacedRoot)
			}

			inserted := fakeCID("value-b")
			insertedRoot, err := semantic.Update(
				ctx,
				testNamespace,
				replacedRoot,
				arcset.CanonicalizePath("b"),
				cid.Undef,
				inserted,
			)
			if err != nil {
				t.Fatalf("Update(insert) failed: %v", err)
			}

			insertedView := mapping.NewViewFrom(map[string]cid.Cid{
				"a": initialEntries["a"],
				"b": inserted,
				"c": replacement,
			})
			expectedInsertedRoot, err := semantic.Commit(ctx, testNamespace, insertedView)
			if err != nil {
				t.Fatalf("Commit(inserted) failed: %v", err)
			}
			if !insertedRoot.Equals(expectedInsertedRoot) {
				t.Fatalf("insert root mismatch: got %s want %s", insertedRoot, expectedInsertedRoot)
			}

			deletedRoot, err := semantic.Update(
				ctx,
				testNamespace,
				insertedRoot,
				arcset.CanonicalizePath("a"),
				initialEntries["a"],
				cid.Undef,
			)
			if err != nil {
				t.Fatalf("Update(delete) failed: %v", err)
			}

			deletedView := mapping.NewViewFrom(map[string]cid.Cid{
				"b": inserted,
				"c": replacement,
			})
			expectedDeletedRoot, err := semantic.Commit(ctx, testNamespace, deletedView)
			if err != nil {
				t.Fatalf("Commit(deleted) failed: %v", err)
			}
			if !deletedRoot.Equals(expectedDeletedRoot) {
				t.Fatalf("delete root mismatch: got %s want %s", deletedRoot, expectedDeletedRoot)
			}
		})
	}
}

type delayedMaterializer struct {
	inner materializer.Store
	delay time.Duration
}

func (d delayedMaterializer) Get(ctx context.Context, namespace string, root cid.Cid, path arcset.Path) (cid.Cid, error) {
	time.Sleep(d.delay)
	return d.inner.Get(ctx, namespace, root, path)
}

func (d delayedMaterializer) BatchGet(ctx context.Context, namespace string, root cid.Cid, paths []arcset.Path) (map[arcset.Path]cid.Cid, error) {
	time.Sleep(d.delay)
	return d.inner.BatchGet(ctx, namespace, root, paths)
}

func (d delayedMaterializer) Update(ctx context.Context, namespace string, newRoot, oldRoot cid.Cid, arcs arcset.ArcSet) error {
	return d.inner.Update(ctx, namespace, newRoot, oldRoot, arcs)
}

func (d delayedMaterializer) Snapshot(ctx context.Context, namespace string, root cid.Cid) (arcset.ArcSet, error) {
	return d.inner.Snapshot(ctx, namespace, root)
}

func (d delayedMaterializer) Iterate(ctx context.Context, namespace string, root cid.Cid) arcset.Iterator {
	return d.inner.Iterate(ctx, namespace, root)
}

type fakeTimingScheme struct {
	proveDelay time.Duration
}

func (s fakeTimingScheme) MaxValues() int { return 256 }

func (s fakeTimingScheme) Commit(values []commitment.Cell) (cid.Cid, error) {
	return fakeCommitRoot(values)
}

func (s fakeTimingScheme) Prove(values []commitment.Cell, index uint64) (cid.Cid, commitment.Cell, []byte, error) {
	time.Sleep(s.proveDelay)
	if index >= uint64(len(values)) {
		return cid.Undef, nil, nil, fmt.Errorf("index %d out of range", index)
	}
	root, err := s.Commit(values)
	if err != nil {
		return cid.Undef, nil, nil, err
	}
	return root, commitment.NewCell(values[index]), []byte("proof"), nil
}

func (s fakeTimingScheme) BatchProve(values []commitment.Cell, indices []uint64) (cid.Cid, []commitment.Cell, []byte, error) {
	root, err := s.Commit(values)
	if err != nil {
		return cid.Undef, nil, nil, err
	}
	proved := make([]commitment.Cell, len(indices))
	for i, index := range indices {
		if index >= uint64(len(values)) {
			return cid.Undef, nil, nil, fmt.Errorf("index %d out of range", index)
		}
		proved[i] = commitment.NewCell(values[index])
	}
	return root, proved, []byte("batch-proof"), nil
}

func (s fakeTimingScheme) VerifyIndex(cid.Cid, uint64, commitment.Cell, []byte) (bool, error) {
	return true, nil
}

func (s fakeTimingScheme) BatchVerify(cid.Cid, []uint64, []commitment.Cell, []byte) (bool, error) {
	return true, nil
}

func (s fakeTimingScheme) VerifyProof(cid.Cid, commitment.Cell, []byte) (bool, error) {
	return true, nil
}

func (s fakeTimingScheme) Replace(values []commitment.Cell, index uint64, _, newValue commitment.Cell) (cid.Cid, error) {
	next := commitment.CloneCells(values)
	if index >= uint64(len(next)) {
		return cid.Undef, fmt.Errorf("index %d out of range", index)
	}
	next[index] = commitment.NewCell(newValue)
	return s.Commit(next)
}

func fakeCommitRoot(values []commitment.Cell) (cid.Cid, error) {
	h := sha256.New()
	var lenBuf [4]byte
	for _, value := range values {
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(value)))
		h.Write(lenBuf[:])
		h.Write(value)
	}
	return maltcid.NewMapIPACid(h.Sum(nil))
}

func TestMapUpdateRejectsInconsistentOldValue(t *testing.T) {
	ctx := context.Background()
	view := mapping.NewViewFrom(map[string]cid.Cid{
		"a": fakeCID("value-a"),
	})

	for name, factory := range mappingSchemes() {
		t.Run(name, func(t *testing.T) {
			semantic := newMap(t, factory, nil)
			root, err := semantic.Commit(ctx, testNamespace, view)
			if err != nil {
				t.Fatalf("Commit failed: %v", err)
			}

			_, err = semantic.Update(
				ctx,
				testNamespace,
				root,
				arcset.CanonicalizePath("a"),
				fakeCID("wrong-old"),
				fakeCID("value-a2"),
			)
			if err == nil {
				t.Fatal("expected old-value mismatch error")
			}
		})
	}
}

func TestMapRestartSafeProveAndUpdate(t *testing.T) {
	ctx := context.Background()
	initial := mapping.NewViewFrom(map[string]cid.Cid{
		"a":       fakeCID("value-a"),
		"aa":      fakeCID("value-aa"),
		"aa/beta": fakeCID("value-aa-beta"),
	})

	for name, factory := range mappingSchemes() {
		t.Run(name, func(t *testing.T) {
			store := materialmemory.New(true)
			semantic := newMap(t, factory, store)

			root, err := semantic.Commit(ctx, testNamespace, initial)
			if err != nil {
				t.Fatalf("Commit failed: %v", err)
			}

			restarted := newMap(t, factory, store)
			key := arcset.CanonicalizePath("aa/beta")
			binding, proof, err := restarted.Prove(ctx, testNamespace, root, key)
			if err != nil {
				t.Fatalf("Prove after restart failed: %v", err)
			}
			if !binding.Present || !binding.Value.Equals(fakeCID("value-aa-beta")) {
				t.Fatalf("unexpected binding after restart: %+v", binding)
			}

			ok, err := restarted.Verify(root, key, binding, proof)
			if err != nil {
				t.Fatalf("Verify after restart failed: %v", err)
			}
			if !ok {
				t.Fatal("expected restarted proof to verify")
			}

			updatedRoot, err := restarted.Update(
				ctx,
				testNamespace,
				root,
				arcset.CanonicalizePath("a"),
				fakeCID("value-a"),
				fakeCID("value-a2"),
			)
			if err != nil {
				t.Fatalf("Update after restart failed: %v", err)
			}

			expectedRoot, err := restarted.Commit(ctx, testNamespace, mapping.NewViewFrom(map[string]cid.Cid{
				"a":       fakeCID("value-a2"),
				"aa":      fakeCID("value-aa"),
				"aa/beta": fakeCID("value-aa-beta"),
			}))
			if err != nil {
				t.Fatalf("Commit(expected) failed: %v", err)
			}
			if !updatedRoot.Equals(expectedRoot) {
				t.Fatalf("restart-safe update root mismatch: got %s want %s", updatedRoot, expectedRoot)
			}
		})
	}
}

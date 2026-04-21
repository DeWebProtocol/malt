package indexed_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/core/commitment"
	"github.com/dewebprotocol/malt/core/commitment/ipa"
	"github.com/dewebprotocol/malt/core/commitment/kzg"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	mappingindexed "github.com/dewebprotocol/malt/core/structure/mapping/indexed"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

type schemeFactory func(t *testing.T) commitment.IndexCommitment

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

func newMap(t *testing.T, factory schemeFactory) mapping.Semantic {
	t.Helper()
	semantic, err := mappingindexed.NewMap(factory(t))
	if err != nil {
		t.Fatalf("indexed.NewMap failed: %v", err)
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
			semantic := newMap(t, factory)

			root, err := semantic.Commit(ctx, view)
			if err != nil {
				t.Fatalf("Commit failed: %v", err)
			}

			key := arcset.CanonicalizePath("b/c")
			binding, proof, err := semantic.Prove(ctx, root, view, key)
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

func TestMapUpdateReplaceInsertDelete(t *testing.T) {
	ctx := context.Background()
	initialEntries := map[string]cid.Cid{
		"a": fakeCID("value-a"),
		"c": fakeCID("value-c"),
	}

	for name, factory := range mappingSchemes() {
		t.Run(name, func(t *testing.T) {
			semantic := newMap(t, factory)
			initialView := mapping.NewViewFrom(initialEntries)

			root, err := semantic.Commit(ctx, initialView)
			if err != nil {
				t.Fatalf("Commit(initial) failed: %v", err)
			}

			replacement := fakeCID("value-c2")
			replacedRoot, err := semantic.Update(
				ctx,
				root,
				initialView,
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
			expectedReplacedRoot, err := semantic.Commit(ctx, replacedView)
			if err != nil {
				t.Fatalf("Commit(replaced) failed: %v", err)
			}
			if !replacedRoot.Equals(expectedReplacedRoot) {
				t.Fatalf("replace root mismatch: got %s want %s", replacedRoot, expectedReplacedRoot)
			}

			inserted := fakeCID("value-b")
			insertedRoot, err := semantic.Update(
				ctx,
				replacedRoot,
				replacedView,
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
			expectedInsertedRoot, err := semantic.Commit(ctx, insertedView)
			if err != nil {
				t.Fatalf("Commit(inserted) failed: %v", err)
			}
			if !insertedRoot.Equals(expectedInsertedRoot) {
				t.Fatalf("insert root mismatch: got %s want %s", insertedRoot, expectedInsertedRoot)
			}

			deletedRoot, err := semantic.Update(
				ctx,
				insertedRoot,
				insertedView,
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
			expectedDeletedRoot, err := semantic.Commit(ctx, deletedView)
			if err != nil {
				t.Fatalf("Commit(deleted) failed: %v", err)
			}
			if !deletedRoot.Equals(expectedDeletedRoot) {
				t.Fatalf("delete root mismatch: got %s want %s", deletedRoot, expectedDeletedRoot)
			}
		})
	}
}

func TestMapUpdateRejectsInconsistentOldValue(t *testing.T) {
	ctx := context.Background()
	view := mapping.NewViewFrom(map[string]cid.Cid{
		"a": fakeCID("value-a"),
	})

	for name, factory := range mappingSchemes() {
		t.Run(name, func(t *testing.T) {
			semantic := newMap(t, factory)
			root, err := semantic.Commit(ctx, view)
			if err != nil {
				t.Fatalf("Commit failed: %v", err)
			}

			_, err = semantic.Update(
				ctx,
				root,
				view,
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

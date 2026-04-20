package tree_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/core/sce/commitment"
	"github.com/dewebprotocol/malt/core/sce/commitment/ipa"
	"github.com/dewebprotocol/malt/core/sce/commitment/kzg"
	"github.com/dewebprotocol/malt/core/structure/list"
	"github.com/dewebprotocol/malt/core/structure/list/tree"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

func newPayloadCID(data []byte) cid.Cid {
	sum, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		panic(err)
	}
	return cid.NewCidV1(cid.Raw, sum)
}

func listBackends(t *testing.T) map[string]commitment.ListBackend {
	t.Helper()
	ipaBackend, err := ipa.NewScheme()
	if err != nil {
		t.Fatalf("ipa.NewScheme failed: %v", err)
	}
	kzgBackend, err := kzg.NewScheme()
	if err != nil {
		t.Fatalf("kzg.NewScheme failed: %v", err)
	}
	return map[string]commitment.ListBackend{
		"ipa": ipaBackend,
		"kzg": kzgBackend,
	}
}

func makeValues(count int) []cid.Cid {
	values := make([]cid.Cid, count)
	for i := range values {
		values[i] = newPayloadCID([]byte{byte(i), byte(i >> 8), byte(i >> 16)})
	}
	return values
}

func TestTreeListSemanticProofs(t *testing.T) {
	ctx := context.Background()
	values := makeValues(300)
	view := list.NewViewFromSlice(values)

	for name, backend := range listBackends(t) {
		t.Run(name, func(t *testing.T) {
			semantic, err := tree.New(backend)
			if err != nil {
				t.Fatalf("tree.New failed: %v", err)
			}

			root, err := semantic.Commit(ctx, view)
			if err != nil {
				t.Fatalf("Commit failed: %v", err)
			}

			query, proof, err := semantic.Prove(ctx, root, view, 257)
			if err != nil {
				t.Fatalf("Prove failed: %v", err)
			}
			if query.Length != uint64(len(values)) || !query.Key.Equals(values[257]) {
				t.Fatalf("unexpected query: %+v", query)
			}

			ok, err := semantic.Verify(root, 257, query, proof)
			if err != nil {
				t.Fatalf("Verify failed: %v", err)
			}
			if !ok {
				t.Fatal("expected present proof to verify")
			}

			absent, absentProof, err := semantic.Prove(ctx, root, view, 999)
			if err != nil {
				t.Fatalf("Prove absent failed: %v", err)
			}
			if absent.Length != uint64(len(values)) || absent.Key.Defined() {
				t.Fatalf("unexpected absent query: %+v", absent)
			}

			ok, err = semantic.Verify(root, 999, absent, absentProof)
			if err != nil {
				t.Fatalf("Verify absent failed: %v", err)
			}
			if !ok {
				t.Fatal("expected absent proof to verify")
			}
		})
	}
}

func TestTreeListSemanticUpdates(t *testing.T) {
	ctx := context.Background()
	initial := makeValues(300)

	for name, backend := range listBackends(t) {
		t.Run(name, func(t *testing.T) {
			semantic, err := tree.New(backend)
			if err != nil {
				t.Fatalf("tree.New failed: %v", err)
			}

			view := list.NewViewFromSlice(initial)
			root, err := semantic.Commit(ctx, view)
			if err != nil {
				t.Fatalf("Commit failed: %v", err)
			}

			replacement := newPayloadCID([]byte("replacement"))
			replacedRoot, err := semantic.Replace(ctx, root, view, 257, initial[257], replacement)
			if err != nil {
				t.Fatalf("Replace failed: %v", err)
			}

			replacedValues := append([]cid.Cid(nil), initial...)
			replacedValues[257] = replacement
			replacedView := list.NewViewFromSlice(replacedValues)
			query, proof, err := semantic.Prove(ctx, replacedRoot, replacedView, 257)
			if err != nil {
				t.Fatalf("Prove replaced failed: %v", err)
			}
			ok, err := semantic.Verify(replacedRoot, 257, query, proof)
			if err != nil || !ok {
				t.Fatalf("Verify replaced failed: ok=%v err=%v", ok, err)
			}

			appended := newPayloadCID([]byte("appended"))
			appendedRoot, newIndex, err := semantic.Append(ctx, replacedRoot, replacedView, appended)
			if err != nil {
				t.Fatalf("Append failed: %v", err)
			}
			if newIndex != uint64(len(replacedValues)) {
				t.Fatalf("unexpected append index %d", newIndex)
			}

			appendedValues := append(append([]cid.Cid(nil), replacedValues...), appended)
			appendedView := list.NewViewFromSlice(appendedValues)
			query, proof, err = semantic.Prove(ctx, appendedRoot, appendedView, newIndex)
			if err != nil {
				t.Fatalf("Prove appended failed: %v", err)
			}
			ok, err = semantic.Verify(appendedRoot, newIndex, query, proof)
			if err != nil || !ok {
				t.Fatalf("Verify appended failed: ok=%v err=%v", ok, err)
			}

			truncatedRoot, err := semantic.Truncate(ctx, appendedRoot, appendedView, 128)
			if err != nil {
				t.Fatalf("Truncate failed: %v", err)
			}

			truncatedView := list.NewViewFromSlice(appendedValues[:128])
			absent, absentProof, err := semantic.Prove(ctx, truncatedRoot, truncatedView, 129)
			if err != nil {
				t.Fatalf("Prove truncated absence failed: %v", err)
			}
			ok, err = semantic.Verify(truncatedRoot, 129, absent, absentProof)
			if err != nil || !ok {
				t.Fatalf("Verify truncated absence failed: ok=%v err=%v", ok, err)
			}
		})
	}
}

package indexed_test

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt/core/sce/commitment"
	"github.com/dewebprotocol/malt/core/sce/commitment/ipa"
	"github.com/dewebprotocol/malt/core/sce/commitment/kzg"
	"github.com/dewebprotocol/malt/core/structure/list"
	"github.com/dewebprotocol/malt/core/structure/list/indexed"
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

func TestIndexedListSemanticProofs(t *testing.T) {
	ctx := context.Background()
	values := []cid.Cid{
		newPayloadCID([]byte("v0")),
		newPayloadCID([]byte("v1")),
		newPayloadCID([]byte("v2")),
	}
	view := list.NewViewFromSlice(values)

	for name, backend := range listBackends(t) {
		t.Run(name, func(t *testing.T) {
			semantic, err := indexed.New(backend)
			if err != nil {
				t.Fatalf("indexed.New failed: %v", err)
			}

			root, err := semantic.Commit(ctx, view)
			if err != nil {
				t.Fatalf("Commit failed: %v", err)
			}

			query, proof, err := semantic.Prove(ctx, root, view, 1)
			if err != nil {
				t.Fatalf("Prove failed: %v", err)
			}
			if !query.Present || query.Length != 3 || !query.Value.Equals(values[1]) {
				t.Fatalf("unexpected query: %+v", query)
			}

			ok, err := semantic.Verify(root, 1, query, proof)
			if err != nil {
				t.Fatalf("Verify failed: %v", err)
			}
			if !ok {
				t.Fatal("expected present proof to verify")
			}

			absent, absentProof, err := semantic.Prove(ctx, root, view, 9)
			if err != nil {
				t.Fatalf("Prove absent failed: %v", err)
			}
			if absent.Present || absent.Length != 3 {
				t.Fatalf("unexpected absent query: %+v", absent)
			}

			ok, err = semantic.Verify(root, 9, absent, absentProof)
			if err != nil {
				t.Fatalf("Verify absent failed: %v", err)
			}
			if !ok {
				t.Fatal("expected absent proof to verify")
			}
		})
	}
}

func TestIndexedListSemanticUpdates(t *testing.T) {
	ctx := context.Background()
	initial := []cid.Cid{
		newPayloadCID([]byte("a")),
		newPayloadCID([]byte("b")),
		newPayloadCID([]byte("c")),
	}

	for name, backend := range listBackends(t) {
		t.Run(name, func(t *testing.T) {
			semantic, err := indexed.New(backend)
			if err != nil {
				t.Fatalf("indexed.New failed: %v", err)
			}

			view := list.NewViewFromSlice(initial)
			root, err := semantic.Commit(ctx, view)
			if err != nil {
				t.Fatalf("Commit failed: %v", err)
			}

			replacement := newPayloadCID([]byte("b2"))
			replacedRoot, err := semantic.Replace(ctx, root, view, 1, initial[1], replacement)
			if err != nil {
				t.Fatalf("Replace failed: %v", err)
			}

			replacedView := list.NewViewFromSlice([]cid.Cid{initial[0], replacement, initial[2]})
			query, proof, err := semantic.Prove(ctx, replacedRoot, replacedView, 1)
			if err != nil {
				t.Fatalf("Prove replaced failed: %v", err)
			}
			ok, err := semantic.Verify(replacedRoot, 1, query, proof)
			if err != nil || !ok {
				t.Fatalf("Verify replaced failed: ok=%v err=%v", ok, err)
			}

			appended := newPayloadCID([]byte("d"))
			appendedRoot, newIndex, err := semantic.Append(ctx, replacedRoot, replacedView, appended)
			if err != nil {
				t.Fatalf("Append failed: %v", err)
			}
			if newIndex != 3 {
				t.Fatalf("unexpected append index %d", newIndex)
			}

			appendedView := list.NewViewFromSlice([]cid.Cid{initial[0], replacement, initial[2], appended})
			query, proof, err = semantic.Prove(ctx, appendedRoot, appendedView, 3)
			if err != nil {
				t.Fatalf("Prove appended failed: %v", err)
			}
			ok, err = semantic.Verify(appendedRoot, 3, query, proof)
			if err != nil || !ok {
				t.Fatalf("Verify appended failed: ok=%v err=%v", ok, err)
			}

			truncatedRoot, err := semantic.Truncate(ctx, appendedRoot, appendedView, 2)
			if err != nil {
				t.Fatalf("Truncate failed: %v", err)
			}

			truncatedView := list.NewViewFromSlice([]cid.Cid{initial[0], replacement})
			absent, absentProof, err := semantic.Prove(ctx, truncatedRoot, truncatedView, 2)
			if err != nil {
				t.Fatalf("Prove truncated absence failed: %v", err)
			}
			ok, err = semantic.Verify(truncatedRoot, 2, absent, absentProof)
			if err != nil || !ok {
				t.Fatalf("Verify truncated absence failed: ok=%v err=%v", ok, err)
			}
		})
	}
}

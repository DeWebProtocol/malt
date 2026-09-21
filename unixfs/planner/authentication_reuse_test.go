package planner

import (
	"reflect"
	"testing"

	"github.com/dewebprotocol/malt-client/journal"
	"github.com/dewebprotocol/malt-client/unixfs"
	cid "github.com/ipfs/go-cid"
)

func TestPlannerReusesPersistedDirectoryStates(t *testing.T) {
	for _, backend := range []string{"kzg", "ipa"} {
		for _, layout := range []unixfs.LayoutKind{unixfs.LayoutFlatV1, unixfs.LayoutHybridV1, unixfs.LayoutRootedV1} {
			for _, swap := range []bool{true, false} {
				name := "converge"
				if swap {
					name = "swap"
				}
				t.Run(backend+"/"+string(layout)+"/"+name, func(t *testing.T) {
					f, creator := newPlannerEnvironment(t, layout, plannerScheme(t, backend))
					x, y := f.blocks.putRaw(t, []byte("X")), f.blocks.putRaw(t, []byte("Y"))
					tree := unixfs.NewStagedDirectory()
					for path, payload := range map[string]cid.Cid{"a/file": x, "b/file": y} {
						if err := unixfs.SetStagedFile(tree, path, payload); err != nil {
							t.Fatal(err)
						}
					}
					projection, err := unixfs.NewLayout(layout)
					if err != nil {
						t.Fatal(err)
					}
					original, err := projection.Materialize(t.Context(), creator, f.blocks, tree)
					if err != nil {
						t.Fatal(err)
					}
					f.root = original.Key
					a, _ := f.resolve(t, f.root, "a")
					b, _ := f.resolve(t, f.root, "b")
					oldA, oldB := f.candidates[a.KeyString()], f.candidates[b.KeyString()]
					nextB := y
					if swap {
						nextB = x
					}
					ops := []journal.Operation{
						plannerOperation(f.root, 1, journal.KindWrite, "a/file", "", y),
						plannerOperation(f.root, 2, journal.KindWrite, "b/file", "", nextB),
					}
					candidates, _, root, err := f.planner(t).Plan(t.Context(), f.root, ops)
					if err != nil {
						t.Fatal(err)
					}
					// Validate the real Core batch, not just each candidate in isolation.
					f.install(t, candidates, root)
					if len(candidates) != 1 || candidates[0].Root != root.String() {
						t.Fatalf("expected only the changed parent, got %d candidates", len(candidates))
					}
					for path, want := range map[string]cid.Cid{"a/file": y, "b/file": nextB} {
						got, found := f.resolve(t, root, path)
						if !found || !got.Equals(want) {
							t.Fatalf("wrong updated %s", path)
						}
					}
					got, _ := f.resolve(t, f.root, "a/file")
					if !got.Equals(x) {
						t.Fatal("update changed the original snapshot")
					}
					if layout != unixfs.LayoutFlatV1 {
						gotA, _ := f.resolve(t, root, "a")
						gotB, _ := f.resolve(t, root, "b")
						wantB := b
						if swap {
							wantB = a
						}
						if !gotA.Equals(b) || !gotB.Equals(wantB) {
							t.Fatal("existing directory Roots were not reused")
						}
						if !reflect.DeepEqual(oldA, f.candidates[a.KeyString()]) || !reflect.DeepEqual(oldB, f.candidates[b.KeyString()]) {
							t.Fatal("reuse replaced persisted candidate lineage")
						}
					}
				})
			}
		}
	}
}

func TestPlannerCanSelectPersistedDirectoryAsFinalRoot(t *testing.T) {
	for _, layout := range []unixfs.LayoutKind{unixfs.LayoutHybridV1, unixfs.LayoutRootedV1} {
		t.Run(string(layout), func(t *testing.T) {
			f, creator := newPlannerEnvironment(t, layout, plannerScheme(t, "ipa"))
			payload := f.blocks.putRaw(t, []byte("X"))
			tree := unixfs.NewStagedDirectory()
			if err := unixfs.SetStagedFile(tree, "a/file", payload); err != nil {
				t.Fatal(err)
			}
			projection, _ := unixfs.NewLayout(layout)
			original, err := projection.Materialize(t.Context(), creator, f.blocks, tree)
			if err != nil {
				t.Fatal(err)
			}
			f.root = original.Key
			child, _ := f.resolve(t, f.root, "a")
			old := f.candidates[child.KeyString()]
			ops := []journal.Operation{
				plannerOperation(f.root, 1, journal.KindRename, "a/file", "file", cid.Undef),
				plannerOperation(f.root, 2, journal.KindUnlink, "a", "", cid.Undef),
			}
			candidates, _, root, err := f.planner(t).Plan(t.Context(), f.root, ops)
			if err != nil {
				t.Fatal(err)
			}
			f.install(t, candidates, root)
			if !root.Equals(child) || len(candidates) != 1 || !reflect.DeepEqual(candidates[0], old) {
				t.Fatal("final receipt must name the reused persisted candidate without rewriting its lineage")
			}
		})
	}
}

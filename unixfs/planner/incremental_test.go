package planner

import (
	"context"
	"testing"

	"github.com/dewebprotocol/malt-client/journal"
	"github.com/dewebprotocol/malt-client/unixfs"
	"github.com/dewebprotocol/malt-core/protocol"
	cid "github.com/ipfs/go-cid"
)

type countingCandidateSource struct {
	CandidateSource
	reads map[string]int
}

func (s *countingCandidateSource) AuthenticationCandidate(ctx context.Context, root cid.Cid) (*protocol.AuthenticationCandidate, error) {
	s.reads[root.KeyString()]++
	return s.CandidateSource.AuthenticationCandidate(ctx, root)
}

func TestPrepareLoadsAffectedDirectoriesAndRetainsVerifiedWriters(t *testing.T) {
	for _, layout := range []unixfs.LayoutKind{unixfs.LayoutFlatV1, unixfs.LayoutHybridV1, unixfs.LayoutRootedV1} {
		t.Run(string(layout), func(t *testing.T) {
			f, creator := newPlannerEnvironment(t, layout, plannerScheme(t, "ipa"))
			payload := f.blocks.putRaw(t, []byte("old"))
			tree := unixfs.NewStagedDirectory()
			for _, path := range []string{"hot/file", "cold/deep/file"} {
				if err := unixfs.SetStagedFile(tree, path, payload); err != nil {
					t.Fatal(err)
				}
			}
			projection, _ := unixfs.NewLayout(layout)
			original, err := projection.Materialize(t.Context(), creator, f.blocks, tree)
			if err != nil {
				t.Fatal(err)
			}
			f.root = original.Key
			cold, _ := f.resolve(t, f.root, "cold")
			// Unvisited subtrees need neither their candidate nor their manifest.
			if layout == unixfs.LayoutFlatV1 {
				delete(f.blocks.values, cold.KeyString())
			} else {
				delete(f.candidates, cold.KeyString())
			}
			source := &countingCandidateSource{CandidateSource: f, reads: map[string]int{}}
			p, err := New(layout, f.blocks, source, f.engine)
			if err != nil {
				t.Fatal(err)
			}
			body := f.blocks.putRaw(t, []byte("new"))
			ops := []journal.Operation{plannerOperation(f.root, 1, journal.KindWrite, "hot/file", "", body)}
			before := f.blocks.putWithCodecCalls
			first, err := p.Prepare(t.Context(), f.root, ops)
			if err != nil {
				t.Fatal(err)
			}
			second, err := p.Prepare(t.Context(), f.root, ops)
			if err != nil {
				t.Fatal(err)
			}
			if !first.Root.Equals(second.Root) || f.blocks.putWithCodecCalls != before {
				t.Fatal("preparation changed identity or performed remote writes")
			}
			want := 2
			if layout == unixfs.LayoutFlatV1 {
				want = 1
			}
			if len(source.reads) != want {
				t.Fatalf("loaded %d directories, want %d", len(source.reads), want)
			}
			for _, count := range source.reads {
				if count != 1 {
					t.Fatal("verified before-image was downloaded again")
				}
			}
			if source.reads[cold.KeyString()] != 0 {
				t.Fatal("untouched directory was loaded")
			}
		})
	}
}

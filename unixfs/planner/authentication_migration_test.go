package planner

import (
	"testing"

	"github.com/dewebprotocol/malt-client/unixfs"
)

// These exact roots were checked against the pre-migration implementation at
// runtime ab962c1 / Core a4526f8. Current fixtures construct and verify them
// exclusively through typed authentication, with no retired API dependency.
func TestPlannerPinnedProjectionRoots(t *testing.T) {
	vectors := []struct {
		backend    string
		layout     unixfs.LayoutKind
		base, root string
	}{
		{"kzg", unixfs.LayoutFlatV1, "bagayfqabaazacmfze4exzhvviw3sb7h4ehs62tn7ykojeik36kt5f6usrqfbhv4wfb7vul5jv7dwhcq6lg6dpduih6aa", "bagayfqabaazacmegan6r54akwdmlask2dx3zpxqgsggdv4zc6ye3bwh33cvglx5ot2cg33qhwy7x5jkryjtv2utlev6q"},
		{"kzg", unixfs.LayoutHybridV1, "bagayfqabaazacmemxjwhgyl5ip5pzyevwne6dizdq32tks56nazob2ejnfk4fuzcnnlzmffwmpfssr3kmb3g4b5lkhva", "bagayfqabaazacmexxooangyfu4pjzu2figy3jksylpmismk5dffb6jg4nukalgs3r6a4hlqolabiltjinvaqutidczsq"},
		{"ipa", unixfs.LayoutFlatV1, "bagayfqabaaraeicgadr257uwj6q4srhzcq263wx2up52elb2ceywlqifdiqbqkyrde", "bagayfqabaaraeia5w73pqwtr446izozdarmfqbzunvmto5j5t4skxktq4p7xdnfwri"},
		{"ipa", unixfs.LayoutHybridV1, "bagayfqabaaraeibxnhglkks2se5kw3kcs3jsclmrbioeaivysuwth7b7qa5vef4xiq", "bagayfqabaaraeiaqqpchxfje4vx4np6rcrrnzgzux2adpxhkp4xdrpholcvjyziyki"},
	}
	for _, v := range vectors {
		t.Run(v.backend+"/"+string(v.layout), func(t *testing.T) {
			f := newPlannerFixture(t, v.layout, plannerScheme(t, v.backend))
			if f.root.String() != v.base {
				t.Fatalf("base %s != %s", f.root, v.base)
			}
			payload := f.blocks.putRaw(t, []byte("typed migration body"))
			candidates, required, root, err := f.planner(t).Plan(t.Context(), f.root, plannerOperations(f.root, payload))
			if err != nil {
				t.Fatal(err)
			}
			if root.String() != v.root || !containsPayload(required, payload) {
				t.Fatalf("candidate %s != %s", root, v.root)
			}
			f.install(t, candidates, root)
		})
	}
}

package planner

import (
	"testing"

	"github.com/dewebprotocol/malt-client/unixfs"
)

// Pin Map layout 1 projection roots for both backends and application layouts,
// including the directory's @payload binding.
func TestPlannerPinnedProjectionRoots(t *testing.T) {
	vectors := []struct {
		backend    string
		layout     unixfs.LayoutKind
		base, root string
	}{
		{"kzg", unixfs.LayoutFlatV1, "bagcifqabaazacmemozf3wgitwkrh6kvwb4flz5iroaon255prynvnrjwafdoadxtpzmerwjvknndophihblh34wbn3xq", "bagcifqabaazacmffbyxr67q4zc24i3lcllbp5fhprbglxnemgjivb6ahf7qbdofkpvp2mp6f2shoxuq7j4v6kjpwgtuq"},
		{"kzg", unixfs.LayoutHybridV1, "bagcifqabaazacmeow55uowmb4ttczoee2q2wzdhgtiyoabe4w65akxpxsqwjjmanc3hi5oulm4dmtdau3e4kknvdvgcq", "bagcifqabaazacmfiuv5tzhcx72ml45jb6nl3bb4zeqo4bhdocpohp5cjebszimbenlbdlk5axo427hzwdns2lustnxwa"},
		{"ipa", unixfs.LayoutFlatV1, "bagcifqabaaraeiab3raop6y6dhq3exhxrn24wknygju7xajzn24mqqh2fp4dx7sp6e", "bagcifqabaaraeia5f64uaiw55lwf3aatoz7bwcy5hkz22gmzxjjybfj2o66flquqyy"},
		{"ipa", unixfs.LayoutHybridV1, "bagcifqabaaraeidtzr5uidmq7ksc2ak3ks3hohoi3weczhjkmqpsu5nogcpmkrnpoi", "bagcifqabaaraeicixfhvueklrbtciov74ckg5ve2vcgbkbeou7fjgf7eje77iuyqmi"},
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

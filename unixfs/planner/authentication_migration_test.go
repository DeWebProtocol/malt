package planner

import (
	"testing"

	"github.com/dewebprotocol/malt-client/unixfs"
)

// Core rc.4 Map layout 4 authenticates original labels, including @payload,
// with the rc.3 empty-cell scalar-zero encoding.
// Pin both commitment backends and application layouts.
func TestPlannerPinnedProjectionRoots(t *testing.T) {
	vectors := []struct {
		backend    string
		layout     unixfs.LayoutKind
		base, root string
	}{
		{"kzg", unixfs.LayoutFlatV1, "bagcirqabaazacmemozf3wgitwkrh6kvwb4flz5iroaon255prynvnrjwafdoadxtpzmerwjvknndophihblh34wbn3xq", "bagcirqabaazacmffbyxr67q4zc24i3lcllbp5fhprbglxnemgjivb6ahf7qbdofkpvp2mp6f2shoxuq7j4v6kjpwgtuq"},
		{"kzg", unixfs.LayoutHybridV1, "bagcirqabaazacmeoc3zigzbk4hzs7ygbxklqhd62vpi5bk5k5fczcjneh5ditilcwxahred6nogezupmc5epa3rpqtyq", "bagcirqabaazacmfymwvjqefdokfhzwlbxrg54mn5fne5m75ta2uabursm7p5l5vje7hckcqwtzgtsyh5wz4lykwnxi4q"},
		{"ipa", unixfs.LayoutFlatV1, "bagcirqabaaraeiab3raop6y6dhq3exhxrn24wknygju7xajzn24mqqh2fp4dx7sp6e", "bagcirqabaaraeia5f64uaiw55lwf3aatoz7bwcy5hkz22gmzxjjybfj2o66flquqyy"},
		{"ipa", unixfs.LayoutHybridV1, "bagcirqabaaraeicqrq2qyui2iy3vxbhtnm3vxp2sc2vtemhfeswtbelzqzkg53ydoy", "bagcirqabaaraeibtp5tzr7jjxhwknhgoow56snrnkfp6nefmufxueem2tkugoiqom4"},
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

package planner

import (
	"testing"

	"github.com/dewebprotocol/malt-client/unixfs"
)

// Prefix layout 4 authenticates the original labels, including @payload.
// Pin both commitment backends and application layouts.
func TestPlannerPinnedProjectionRoots(t *testing.T) {
	vectors := []struct {
		backend    string
		layout     unixfs.LayoutKind
		base, root string
	}{
		{"kzg", unixfs.LayoutFlatV1, "bagcirqabaazacmfgqx4jriyby4rfnxpgy73bm3c2w55nkcxb6fnvdfz7lwjbcckv56bsefdhs4brisrzf54xdoqhzpza", "bagcirqabaazacmfjv7rzswvecxuifmazc3mlp7oe4ij5r2g4l64crjultvzh6xhgedfj6sadvdyxl26kdjgyuc2dazxa"},
		{"kzg", unixfs.LayoutHybridV1, "bagcirqabaazacmel5k7ccqru7d2tzrolfej5cftvl7bfly4vbtum5l3egotfs4bjosjidlju7geudmc6qpnsbdpnhrra", "bagcirqabaazacmfkcxqk6eoysvogmzfa3yiwpjrvezszhfbgqr5hc6db5enrqraexmbs6nxrqgaksnmnwi7ljpom7epq"},
		{"ipa", unixfs.LayoutFlatV1, "bagcirqabaaraeic53iby27mw6ofnrlkxaceafffanigphhurusbnlhl3sacen5qd5q", "bagcirqabaaraeidmrezc2pp265vm3ysai7ndv2hi7h5cspkgzb3fixohwxs7n57v2q"},
		{"ipa", unixfs.LayoutHybridV1, "bagcirqabaaraeiap4ui5j4vobybovm6o3x6fxbtpvszwczbavwcfzs23ovyorgorqq", "bagcirqabaaraeiadccijnvrda33jfrvgtiadcokb3p5ykxg66psqi7xmah24t2zhre"},
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

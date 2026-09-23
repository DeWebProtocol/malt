package planner

import (
	"testing"

	"github.com/dewebprotocol/malt-client/unixfs"
)

// Coordinate derivation profile 4 and the ordinary @payload label deliberately
// produce new Roots. Pin both commitment backends and application layouts.
func TestPlannerPinnedProjectionRoots(t *testing.T) {
	vectors := []struct {
		backend    string
		layout     unixfs.LayoutKind
		base, root string
	}{
		{"kzg", unixfs.LayoutFlatV1, "bagcifqabaazacmfrbtk5jo7yzhgft4gquke6jtikw4d53qbkxmwvt3szqbw4nyaahvhn2sxiz2h5ee365moypjp662ua", "bagcifqabaazacmfwpxhyqwib5g2g5brlm3nzta57tmpjv4rc4wlcfr3f3vsmp6dauz3hlpw3eehduv5dpzz24z2ah7ja"},
		{"kzg", unixfs.LayoutHybridV1, "bagcifqabaazacmfkewl72lvptsnjjszisj2xovlplh5uchkkg5mregctpneqvdn2roj32j65eyan2jkgot64bffj2ooq", "bagcifqabaazacmeddjmrenpwith5fzd3wvgaacvrwo2o2awua2et47teyrmlcxc3k6mlhz6awhdtsbq7j32zkobuywpq"},
		{"ipa", unixfs.LayoutFlatV1, "bagcifqabaaraeiaekt4db5lipemlmgwqv6lzg5vphxdfx5amzat3nb77vrhefhxqke", "bagcifqabaaraeidkr6efv6fnxnuqlzjew4rnstz6rhimljts6xv4d2dc4d6ao6754u"},
		{"ipa", unixfs.LayoutHybridV1, "bagcifqabaaraeicmnjg2mgypq67mlyfdjl6ctvnwmrfilijvfj5scues2ly5xeqok4", "bagcifqabaaraeia2wsa4q2yiwi6zwz2hzsyt7kpphgu675pomwuct2xi6j4zvsyq4e"},
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

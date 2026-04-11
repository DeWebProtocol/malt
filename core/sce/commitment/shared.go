package commitment

import (
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// ExtractSortedPathsValues extracts sorted paths and values from an ArcSetView.
// This is a shared utility used by all commitment schemes (KZG, IPA, Verkle).
func ExtractSortedPathsValues(arcs arcset.View) ([]string, []cid.Cid) {
	var paths []string
	iter := arcs.Iterate()
	for {
		path, _, ok := iter.Next()
		if !ok {
			break
		}
		paths = append(paths, path)
	}
	// paths are already sorted by iterator

	values := make([]cid.Cid, len(paths))
	for i, path := range paths {
		values[i], _ = arcs.Get(path)
	}

	return paths, values
}

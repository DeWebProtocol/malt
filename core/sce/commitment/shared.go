package commitment

import (
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// ExtractSortedPathsValues extracts sorted paths and values from an arc set.
// This is only used by legacy path-oriented wrappers.
func ExtractSortedPathsValues(arcs arcset.ArcSet) ([]string, []cid.Cid) {
	var paths []string
	iter := arcs.Iterate()
	for {
		path, _, ok := iter.Next()
		if !ok {
			break
		}
		paths = append(paths, path.String())
	}
	// paths are already sorted by iterator

	values := make([]cid.Cid, len(paths))
	for i, path := range paths {
		values[i], _ = arcs.Get(arcset.CanonicalizePath(path))
	}

	return paths, values
}

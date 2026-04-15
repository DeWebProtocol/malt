package commitment

import (
	"fmt"
	"sort"

	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

// ExtractSortedPathsValues extracts sorted paths and values from a Snapshot.
// This is a shared utility used by all commitment schemes (KZG, IPA, Verkle).
func ExtractSortedPathsValues(arcs arcset.Snapshot) ([]string, []cid.Cid) {
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

// ExtractSortedPathsWithIndex extracts sorted paths, values, and a path-to-index map
// from a Snapshot. Used by SCE for session caching.
func ExtractSortedPathsWithIndex(arcs arcset.Snapshot) ([]string, []cid.Cid, map[string]int, error) {
	var paths []string
	iter := arcs.Iterate()
	for {
		path, _, ok := iter.Next()
		if !ok {
			break
		}
		paths = append(paths, path)
	}
	if iter.Err() != nil {
		return nil, nil, nil, iter.Err()
	}
	sort.Strings(paths)

	values := make([]cid.Cid, len(paths))
	pathToIndex := make(map[string]int, len(paths))

	for i, path := range paths {
		value, ok := arcs.Get(path)
		if !ok {
			return nil, nil, nil, fmt.Errorf("path %s disappeared during iteration", path)
		}
		values[i] = value
		pathToIndex[path] = i
	}

	return paths, values, pathToIndex, nil
}

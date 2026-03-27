package types

import (
	"encoding/json"
	"fmt"
	"sort"
)

// ArcSet represents a set of arcs from a single object.
// Formally: A_v = {(p, c)} where each (p, c) pair represents
// an arc from the object v to target c with path label p.
//
// Each path is unique within an ArcSet - there can be only one
// target for each path label from a given source object.
type ArcSet struct {
	// arcs maps path labels to target CIDs
	arcs map[Path]CID
}

// NewArcSet creates a new empty ArcSet.
func NewArcSet() *ArcSet {
	return &ArcSet{
		arcs: make(map[Path]CID),
	}
}

// NewArcSetFromPairs creates an ArcSet from a slice of ArcPairs.
func NewArcSetFromPairs(pairs ...ArcPair) *ArcSet {
	as := NewArcSet()
	for _, p := range pairs {
		as.arcs[p.Path] = p.Target
	}
	return as
}

// Add adds a new arc (p, c) to the set.
// If an arc with the same path already exists, it is replaced.
func (as *ArcSet) Add(p Path, c CID) {
	as.arcs[p] = c
}

// Get retrieves the target CID for a given path.
// Returns the CID and true if found, or an empty CID and false otherwise.
func (as *ArcSet) Get(p Path) (CID, bool) {
	c, ok := as.arcs[p]
	return c, ok
}

// Remove removes the arc with the given path.
// Returns true if an arc was removed, false otherwise.
func (as *ArcSet) Remove(p Path) bool {
	if _, ok := as.arcs[p]; ok {
		delete(as.arcs, p)
		return true
	}
	return false
}

// Has checks if an arc with the given path exists.
func (as *ArcSet) Has(p Path) bool {
	_, ok := as.arcs[p]
	return ok
}

// Size returns the number of arcs in the set.
func (as *ArcSet) Size() int {
	return len(as.arcs)
}

// IsEmpty checks if the arc set is empty.
func (as *ArcSet) IsEmpty() bool {
	return len(as.arcs) == 0
}

// Paths returns all path labels in the set, sorted for deterministic ordering.
func (as *ArcSet) Paths() []Path {
	paths := make([]Path, 0, len(as.arcs))
	for p := range as.arcs {
		paths = append(paths, p)
	}
	sort.Slice(paths, func(i, j int) bool {
		return paths[i] < paths[j]
	})
	return paths
}

// Pairs returns all (path, target) pairs in the set, sorted by path.
func (as *ArcSet) Pairs() []ArcPair {
	pairs := make([]ArcPair, 0, len(as.arcs))
	for p, c := range as.arcs {
		pairs = append(pairs, ArcPair{Path: p, Target: c})
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].Path < pairs[j].Path
	})
	return pairs
}

// ForEach iterates over all arcs in the set, calling fn for each.
// Iteration order is not guaranteed.
func (as *ArcSet) ForEach(fn func(p Path, c CID) bool) {
	for p, c := range as.arcs {
		if !fn(p, c) {
			break
		}
	}
}

// Clone creates a deep copy of the ArcSet.
func (as *ArcSet) Clone() *ArcSet {
	if as == nil {
		return nil
	}
	clone := NewArcSet()
	for p, c := range as.arcs {
		clone.arcs[p] = c
	}
	return clone
}

// Merge merges another ArcSet into this one.
// Arcs from the other set overwrite existing arcs with the same path.
func (as *ArcSet) Merge(other *ArcSet) {
	if other == nil {
		return
	}
	for p, c := range other.arcs {
		as.arcs[p] = c
	}
}

// ToMap returns the underlying map of paths to CIDs.
// The returned map is a copy to prevent external modification.
func (as *ArcSet) ToMap() map[Path]CID {
	result := make(map[Path]CID, len(as.arcs))
	for p, c := range as.arcs {
		result[p] = c
	}
	return result
}

// MarshalJSON implements json.Marshaler.
func (as *ArcSet) MarshalJSON() ([]byte, error) {
	pairs := as.Pairs()
	return json.Marshal(pairs)
}

// UnmarshalJSON implements json.Unmarshaler.
func (as *ArcSet) UnmarshalJSON(data []byte) error {
	var pairs []ArcPair
	if err := json.Unmarshal(data, &pairs); err != nil {
		return err
	}

	as.arcs = make(map[Path]CID, len(pairs))
	for _, p := range pairs {
		as.arcs[p.Path] = p.Target
	}
	return nil
}

// Equals checks if two ArcSets are equal.
func (as *ArcSet) Equals(other *ArcSet) bool {
	if as == nil || other == nil {
		return as == other
	}
	if len(as.arcs) != len(other.arcs) {
		return false
	}
	for p, c := range as.arcs {
		otherC, ok := other.arcs[p]
		if !ok || !c.Equals(otherC) {
			return false
		}
	}
	return true
}

// String returns a string representation of the ArcSet.
func (as *ArcSet) String() string {
	pairs := as.Pairs()
	return fmt.Sprintf("ArcSet%v", pairs)
}

// Update updates the target CID for a given path.
// Returns the old CID and true if the path existed, or empty CID and false.
func (as *ArcSet) Update(p Path, newCID CID) (CID, bool) {
	oldCID, ok := as.arcs[p]
	if ok {
		as.arcs[p] = newCID
	}
	return oldCID, ok
}

// Diff computes the difference between two ArcSets.
// Returns:
//   - added: paths that are in other but not in this
//   - removed: paths that are in this but not in other
//   - changed: paths that exist in both but have different targets
func (as *ArcSet) Diff(other *ArcSet) (added, removed, changed []Path) {
	if other == nil {
		for p := range as.arcs {
			removed = append(removed, p)
		}
		return
	}

	// Find removed and changed
	for p, c := range as.arcs {
		otherC, ok := other.arcs[p]
		if !ok {
			removed = append(removed, p)
		} else if !c.Equals(otherC) {
			changed = append(changed, p)
		}
	}

	// Find added
	for p := range other.arcs {
		if _, ok := as.arcs[p]; !ok {
			added = append(added, p)
		}
	}

	return
}
// Package indexed implements the default canonical-path ordered map semantic.
package indexed

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"slices"

	"github.com/dewebprotocol/malt/core/commitment"
	"github.com/dewebprotocol/malt/core/structure"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

var pathProofMagic = [4]byte{'M', 'P', 'T', 'H'}

const pathProofVersion byte = 1
const pathProofOverhead = 4 + 1 + sha256.Size

// Map materializes a keyed view into a canonical-path ordered index
// vector and delegates authentication to a primitive index commitment backend.
type Map struct {
	scheme commitment.IndexCommitment
}

// NewMap creates the default keyed-map semantic over an index-addressed
// commitment backend. The default placement rule orders bindings by canonical
// path and authenticates the resulting value vector.
func NewMap(scheme commitment.IndexCommitment) (*Map, error) {
	if scheme == nil {
		return nil, fmt.Errorf("scheme is nil")
	}
	return &Map{scheme: scheme}, nil
}

// Commit commits the supplied keyed view and returns a structure root.
func (s *Map) Commit(ctx context.Context, view mapping.View) (cid.Cid, error) {
	_ = ctx
	_, cells, err := extractSortedCells(view)
	if err != nil {
		return cid.Undef, err
	}
	return s.scheme.Commit(cells)
}

// Prove proves a membership binding for key under root.
func (s *Map) Prove(ctx context.Context, root cid.Cid, view mapping.View, key arcset.Path) (mapping.Binding, structure.Proof, error) {
	_ = ctx
	entries, cells, err := extractSortedCells(view)
	if err != nil {
		return mapping.Binding{}, nil, err
	}
	index, ok := findPathIndex(entries, key)
	if !ok {
		return mapping.Binding{}, nil, fmt.Errorf("path %s not found", key.String())
	}

	recomputedRoot, err := s.scheme.Commit(cells)
	if err != nil {
		return mapping.Binding{}, nil, err
	}
	if !recomputedRoot.Equals(root) {
		return mapping.Binding{}, nil, fmt.Errorf("recomputed root does not match requested root")
	}

	provedRoot, proved, proof, err := s.scheme.Prove(cells, uint64(index))
	if err != nil {
		return mapping.Binding{}, nil, err
	}
	if !provedRoot.Equals(root) {
		return mapping.Binding{}, nil, fmt.Errorf("recomputed root does not match requested root")
	}
	value, err := proved.AsCID()
	if err != nil {
		return mapping.Binding{}, nil, fmt.Errorf("proved cell at path %s is not a CID: %w", key.String(), err)
	}
	return mapping.Binding{Value: value, Present: true}, wrapPathProof(key.String(), proof), nil
}

// Verify verifies a proof for a keyed binding under root.
func (s *Map) Verify(root cid.Cid, key arcset.Path, expected mapping.Binding, proof structure.Proof) (bool, error) {
	if !expected.Present {
		return false, fmt.Errorf("non-membership verification is not implemented by the default map semantic")
	}
	primitiveProof, err := unwrapPathProof(key.String(), proof)
	if err != nil {
		return false, err
	}
	return s.scheme.VerifyProof(root, commitment.CellFromCID(expected.Value), primitiveProof)
}

// Update applies insert, replace, or delete semantics.
func (s *Map) Update(ctx context.Context, root cid.Cid, view mapping.View, key arcset.Path, oldValue, newValue cid.Cid) (cid.Cid, error) {
	_ = ctx
	entries, cells, err := extractSortedCells(view)
	if err != nil {
		return cid.Undef, err
	}
	recomputedRoot, err := s.scheme.Commit(cells)
	if err != nil {
		return cid.Undef, err
	}
	if !recomputedRoot.Equals(root) {
		return cid.Undef, fmt.Errorf("recomputed root does not match requested root")
	}

	index, exists := findPathIndex(entries, key)
	currentValue, currentOK := view.Get(key)
	if currentOK != exists {
		return cid.Undef, fmt.Errorf("view lookup mismatch for path %s", key.String())
	}

	switch {
	case !oldValue.Defined() && !newValue.Defined():
		if exists {
			return cid.Undef, fmt.Errorf("path %s exists; absent-to-absent update is invalid", key.String())
		}
		return root, nil
	case exists:
		if !oldValue.Defined() {
			return cid.Undef, fmt.Errorf("path %s already exists", key.String())
		}
		if !currentValue.Equals(oldValue) {
			return cid.Undef, fmt.Errorf("old value mismatch at path %s", key.String())
		}
		if !newValue.Defined() {
			nextView := cloneEntries(view)
			delete(nextView, key)
			return s.Commit(ctx, mapping.NewViewFromPaths(nextView))
		}
		return s.scheme.Replace(cells, uint64(index), commitment.CellFromCID(oldValue), commitment.CellFromCID(newValue))
	default:
		if oldValue.Defined() {
			return cid.Undef, fmt.Errorf("path %s is absent", key.String())
		}
		if !newValue.Defined() {
			return root, nil
		}
		nextView := cloneEntries(view)
		nextView[key] = newValue
		return s.Commit(ctx, mapping.NewViewFromPaths(nextView))
	}
}

func extractSortedCells(view mapping.View) ([]arcset.Path, []commitment.Cell, error) {
	if view == nil {
		return nil, nil, fmt.Errorf("view is nil")
	}
	paths := make([]arcset.Path, 0, view.Len())
	values := make([]commitment.Cell, 0, view.Len())
	iter := view.Iterate()
	for {
		path, value, ok := iter.Next()
		if !ok {
			break
		}
		paths = append(paths, path)
		values = append(values, commitment.CellFromCID(value))
	}
	if err := iter.Err(); err != nil {
		return nil, nil, err
	}
	if !slices.IsSorted(paths) {
		return nil, nil, fmt.Errorf("view iteration is not in canonical key order")
	}
	return paths, values, nil
}

func findPathIndex(paths []arcset.Path, path arcset.Path) (int, bool) {
	index, ok := slices.BinarySearch(paths, path)
	return index, ok
}

func wrapPathProof(path string, primitiveProof []byte) structure.Proof {
	pathHash := sha256.Sum256([]byte(path))
	out := make([]byte, 0, pathProofOverhead+len(primitiveProof))
	out = append(out, pathProofMagic[:]...)
	out = append(out, pathProofVersion)
	out = append(out, pathHash[:]...)
	out = append(out, primitiveProof...)
	return out
}

func unwrapPathProof(path string, proof structure.Proof) ([]byte, error) {
	if len(proof) < pathProofOverhead {
		return nil, fmt.Errorf("path-bound proof too short: %d", len(proof))
	}
	if !bytes.Equal(proof[:4], pathProofMagic[:]) {
		return nil, fmt.Errorf("invalid path-bound proof magic")
	}
	if proof[4] != pathProofVersion {
		return nil, fmt.Errorf("unsupported path-bound proof version %d", proof[4])
	}
	expected := sha256.Sum256([]byte(path))
	if !bytes.Equal(proof[5:5+sha256.Size], expected[:]) {
		return nil, fmt.Errorf("path-bound proof does not match requested path")
	}
	return proof[pathProofOverhead:], nil
}

func cloneEntries(view mapping.View) map[arcset.Path]cid.Cid {
	out := make(map[arcset.Path]cid.Cid, view.Len())
	iter := view.Iterate()
	for {
		path, value, ok := iter.Next()
		if !ok {
			break
		}
		out[path] = value
	}
	return out
}

var _ mapping.Semantic = (*Map)(nil)

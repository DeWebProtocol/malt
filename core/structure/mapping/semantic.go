package mapping

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"

	"github.com/dewebprotocol/malt/core/commitment"
	"github.com/dewebprotocol/malt/core/structure"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

var pathProofMagic = [4]byte{'M', 'P', 'T', 'H'}

const pathProofVersion byte = 1
const pathProofOverhead = 4 + 1 + sha256.Size

// IndexedSemantic materializes a keyed view into a stable index vector and
// delegates authentication to a primitive index commitment backend.
type IndexedSemantic struct {
	scheme commitment.IndexCommitment
}

// NewIndexedSemantic creates the default keyed-map semantic over an
// index-addressed commitment backend.
func NewIndexedSemantic(scheme commitment.IndexCommitment) (*IndexedSemantic, error) {
	if scheme == nil {
		return nil, fmt.Errorf("scheme is nil")
	}
	return &IndexedSemantic{scheme: scheme}, nil
}

// Commit commits the supplied keyed view and returns a structure root.
func (s *IndexedSemantic) Commit(ctx context.Context, view View) (cid.Cid, error) {
	_ = ctx
	_, cells, err := extractSortedCells(view)
	if err != nil {
		return cid.Undef, err
	}
	return s.scheme.CommitValues(cells)
}

// Prove proves a membership binding for key under root.
func (s *IndexedSemantic) Prove(ctx context.Context, root cid.Cid, view View, key arcset.Path) (Binding, structure.Proof, error) {
	_ = ctx
	paths, cells, err := extractSortedCells(view)
	if err != nil {
		return Binding{}, nil, err
	}
	index, ok := findPathIndex(paths, key.String())
	if !ok {
		return Binding{Present: false}, nil, fmt.Errorf("path %s not found", key.String())
	}

	proved, proof, err := s.scheme.ProveIndex(root, cells, uint64(index))
	if err != nil {
		return Binding{}, nil, err
	}
	value, err := proved.AsCID()
	if err != nil {
		return Binding{}, nil, fmt.Errorf("proved cell at path %s is not a CID: %w", key.String(), err)
	}
	return Binding{Value: value, Present: true}, wrapPathProof(key.String(), proof), nil
}

// Verify verifies a proof for a keyed binding under root.
func (s *IndexedSemantic) Verify(root cid.Cid, key arcset.Path, expected Binding, proof structure.Proof) (bool, error) {
	if !expected.Present {
		return false, fmt.Errorf("non-membership verification is not implemented")
	}
	primitiveProof, err := unwrapPathProof(key.String(), proof)
	if err != nil {
		return false, err
	}
	return s.scheme.VerifyProof(root, commitment.CellFromCID(expected.Value), primitiveProof)
}

// Update applies insert, replace, or delete semantics.
func (s *IndexedSemantic) Update(ctx context.Context, root cid.Cid, view View, key arcset.Path, oldValue, newValue cid.Cid) (cid.Cid, error) {
	_ = ctx
	paths, cells, err := extractSortedCells(view)
	if err != nil {
		return cid.Undef, err
	}
	index, ok := findPathIndex(paths, key.String())
	if !ok {
		return cid.Undef, fmt.Errorf("path %s not found", key.String())
	}
	return s.scheme.ReplaceIndex(root, cells, uint64(index), commitment.CellFromCID(oldValue), commitment.CellFromCID(newValue))
}

func extractSortedCells(view View) ([]string, []commitment.Cell, error) {
	if view == nil {
		return nil, nil, fmt.Errorf("view is nil")
	}
	paths := make([]string, 0, view.Len())
	cells := make([]commitment.Cell, 0, view.Len())
	iter := view.Iterate()
	for {
		path, value, ok := iter.Next()
		if !ok {
			break
		}
		paths = append(paths, path.String())
		cells = append(cells, commitment.CellFromCID(value))
	}
	if err := iter.Err(); err != nil {
		return nil, nil, err
	}
	return paths, cells, nil
}

func findPathIndex(paths []string, path string) (int, bool) {
	low, high := 0, len(paths)-1
	for low <= high {
		mid := (low + high) / 2
		if paths[mid] == path {
			return mid, true
		}
		if paths[mid] < path {
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	return -1, false
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

var _ Semantic = (*IndexedSemantic)(nil)

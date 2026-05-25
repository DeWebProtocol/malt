// Package indexedmap implements the canonical-path ordered baseline map
// semantic for eval comparison. Core runtime graph wiring uses mapping/radix.
package indexedmap

import (
	"context"
	"encoding/binary"
	"fmt"
	"slices"
	"strconv"

	"github.com/dewebprotocol/malt/core/arctable"
	"github.com/dewebprotocol/malt/core/commitment"
	"github.com/dewebprotocol/malt/core/structure"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

const (
	countPrefix = "malt:map:count:v1:"
	pathPrefix  = "malt:map:path:v1:"
)

// Map materializes a keyed runtime view into a canonical-path ordered binding
// vector and delegates authentication to a primitive index commitment backend.
type Map struct {
	scheme   commitment.IndexCommitment
	arctable arctable.ArcTable
}

type entry struct {
	path  arcset.Path
	value cid.Cid
}

// NewMap creates the baseline keyed-map semantic over an index-addressed
// commitment backend. Its placement rule orders bindings by canonical path and
// authenticates binding cells rather than bare values.
func NewMap(scheme commitment.IndexCommitment, e arctable.ArcTable) (*Map, error) {
	if scheme == nil {
		return nil, fmt.Errorf("scheme is nil")
	}
	if e == nil {
		return nil, fmt.Errorf("arctable is nil")
	}
	return &Map{scheme: scheme, arctable: e}, nil
}

// Commit commits the supplied keyed view and materializes the runtime state in ArcTable.
func (s *Map) Commit(ctx context.Context, namespace string, view mapping.View) (cid.Cid, error) {
	entries, cells, err := extractSortedEntries(view)
	if err != nil {
		return cid.Undef, err
	}
	root, err := s.scheme.Commit(cells)
	if err != nil {
		return cid.Undef, err
	}
	if err := s.storeEntries(ctx, namespace, root, entries); err != nil {
		return cid.Undef, err
	}
	return root, nil
}

// Prove proves a membership binding for key under root.
func (s *Map) Prove(ctx context.Context, namespace string, root cid.Cid, key arcset.Path) (mapping.Binding, structure.Proof, error) {
	entries, cells, err := s.loadEntries(ctx, namespace, root)
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
		return mapping.Binding{}, nil, fmt.Errorf("proved root does not match requested root")
	}

	provedValue, err := decodeBindingCell(key, proved)
	if err != nil {
		return mapping.Binding{}, nil, err
	}
	return mapping.Binding{Value: provedValue, Present: true}, structure.Proof(proof), nil
}

// Verify verifies a proof for a keyed binding under root.
func (s *Map) Verify(root cid.Cid, key arcset.Path, expected mapping.Binding, proof structure.Proof) (bool, error) {
	if !expected.Present {
		return false, fmt.Errorf("non-membership verification is not implemented by the default map semantic")
	}
	cell, err := encodeBindingCell(key, expected.Value)
	if err != nil {
		return false, err
	}
	return s.scheme.VerifyProof(root, cell, proof)
}

// Update applies insert, replace, or delete semantics over the committed runtime state.
func (s *Map) Update(ctx context.Context, namespace string, root cid.Cid, key arcset.Path, oldValue, newValue cid.Cid) (cid.Cid, error) {
	entries, cells, err := s.loadEntries(ctx, namespace, root)
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
	switch {
	case !oldValue.Defined() && !newValue.Defined():
		if exists {
			return cid.Undef, fmt.Errorf("path %s exists; absent-to-absent update is invalid", key.String())
		}
		return root, nil
	case exists:
		currentValue := entries[index].value
		if !oldValue.Defined() {
			return cid.Undef, fmt.Errorf("path %s already exists", key.String())
		}
		if !currentValue.Equals(oldValue) {
			return cid.Undef, fmt.Errorf("old value mismatch at path %s", key.String())
		}
		if !newValue.Defined() {
			nextEntries := append([]entry(nil), entries[:index]...)
			nextEntries = append(nextEntries, entries[index+1:]...)
			return s.commitEntries(ctx, namespace, nextEntries)
		}

		oldCell, err := encodeBindingCell(key, oldValue)
		if err != nil {
			return cid.Undef, err
		}
		newCell, err := encodeBindingCell(key, newValue)
		if err != nil {
			return cid.Undef, err
		}
		newRoot, err := s.scheme.Replace(cells, uint64(index), oldCell, newCell)
		if err != nil {
			return cid.Undef, err
		}
		nextEntries := append([]entry(nil), entries...)
		nextEntries[index].value = newValue
		if err := s.storeEntries(ctx, namespace, newRoot, nextEntries); err != nil {
			return cid.Undef, err
		}
		return newRoot, nil
	default:
		if oldValue.Defined() {
			return cid.Undef, fmt.Errorf("path %s is absent", key.String())
		}
		if !newValue.Defined() {
			return root, nil
		}
		nextEntries := append([]entry(nil), entries...)
		nextEntries = append(nextEntries, entry{})
		copy(nextEntries[index+1:], nextEntries[index:])
		nextEntries[index] = entry{path: key, value: newValue}
		return s.commitEntries(ctx, namespace, nextEntries)
	}
}

func (s *Map) commitEntries(ctx context.Context, namespace string, entries []entry) (cid.Cid, error) {
	cells, err := entriesToCells(entries)
	if err != nil {
		return cid.Undef, err
	}
	root, err := s.scheme.Commit(cells)
	if err != nil {
		return cid.Undef, err
	}
	if err := s.storeEntries(ctx, namespace, root, entries); err != nil {
		return cid.Undef, err
	}
	return root, nil
}

func extractSortedEntries(view mapping.View) ([]entry, []commitment.Cell, error) {
	if view == nil {
		return nil, nil, fmt.Errorf("view is nil")
	}

	entries := make([]entry, 0, view.Len())
	iter := view.Iterate()
	for {
		path, value, ok := iter.Next()
		if !ok {
			break
		}
		entries = append(entries, entry{path: path, value: value})
	}
	if err := iter.Err(); err != nil {
		return nil, nil, err
	}
	if !slices.IsSortedFunc(entries, func(a, b entry) int {
		switch {
		case a.path < b.path:
			return -1
		case a.path > b.path:
			return 1
		default:
			return 0
		}
	}) {
		return nil, nil, fmt.Errorf("view iteration is not in canonical key order")
	}

	cells, err := entriesToCells(entries)
	if err != nil {
		return nil, nil, err
	}
	return entries, cells, nil
}

func entriesToCells(entries []entry) ([]commitment.Cell, error) {
	cells := make([]commitment.Cell, len(entries))
	for i, ent := range entries {
		cell, err := encodeBindingCell(ent.path, ent.value)
		if err != nil {
			return nil, err
		}
		cells[i] = cell
	}
	return cells, nil
}

func findPathIndex(entries []entry, path arcset.Path) (int, bool) {
	index, ok := slices.BinarySearchFunc(entries, path, func(ent entry, key arcset.Path) int {
		switch {
		case ent.path < key:
			return -1
		case ent.path > key:
			return 1
		default:
			return 0
		}
	})
	return index, ok
}

func encodeBindingCell(path arcset.Path, value cid.Cid) (commitment.Cell, error) {
	if path.IsEmpty() {
		return nil, fmt.Errorf("path is empty")
	}
	if !value.Defined() {
		return nil, fmt.Errorf("value is undefined")
	}

	pathBytes := []byte(path.String())
	valueBytes := value.Bytes()
	if len(pathBytes) > 0xffff {
		return nil, fmt.Errorf("path %q is too long", path.String())
	}

	out := make([]byte, 0, 1+2+len(pathBytes)+len(valueBytes))
	out = append(out, 1)
	out = binary.BigEndian.AppendUint16(out, uint16(len(pathBytes)))
	out = append(out, pathBytes...)
	out = append(out, valueBytes...)
	return commitment.NewCell(out), nil
}

func decodeBindingCell(path arcset.Path, cell commitment.Cell) (cid.Cid, error) {
	if len(cell) < 3 {
		return cid.Undef, fmt.Errorf("binding cell too short")
	}
	if cell[0] != 1 {
		return cid.Undef, fmt.Errorf("unsupported binding cell version %d", cell[0])
	}

	pathLen := int(binary.BigEndian.Uint16(cell[1:3]))
	if len(cell) < 3+pathLen {
		return cid.Undef, fmt.Errorf("binding cell truncated")
	}
	cellPath := arcset.CanonicalizePath(string(cell[3 : 3+pathLen]))
	if cellPath != path {
		return cid.Undef, fmt.Errorf("binding cell path mismatch: got %s want %s", cellPath.String(), path.String())
	}
	return cid.Cast(cell[3+pathLen:])
}

func (s *Map) loadEntries(ctx context.Context, namespace string, root cid.Cid) ([]entry, []commitment.Cell, error) {
	countCID, err := s.arctable.Get(ctx, namespace, cid.Undef, countPath(root))
	if err != nil {
		return nil, nil, err
	}
	count, err := decodeCountMarker(countCID)
	if err != nil {
		return nil, nil, err
	}
	if count == 0 {
		return nil, nil, nil
	}

	paths := make([]arcset.Path, 0, count*2)
	for i := uint64(0); i < count; i++ {
		paths = append(paths, entryKeyPath(root, i), entryValuePath(root, i))
	}
	found, err := s.arctable.BatchGet(ctx, namespace, cid.Undef, paths)
	if err != nil {
		return nil, nil, err
	}

	entries := make([]entry, count)
	for i := uint64(0); i < count; i++ {
		keyCID, ok := found[entryKeyPath(root, i)]
		if !ok {
			return nil, nil, fmt.Errorf("missing key marker at index %d", i)
		}
		valueCID, ok := found[entryValuePath(root, i)]
		if !ok {
			return nil, nil, fmt.Errorf("missing value CID at index %d", i)
		}
		path, err := decodePathMarker(keyCID)
		if err != nil {
			return nil, nil, err
		}
		entries[i] = entry{path: path, value: valueCID}
	}

	cells, err := entriesToCells(entries)
	if err != nil {
		return nil, nil, err
	}
	return entries, cells, nil
}

func (s *Map) storeEntries(ctx context.Context, namespace string, root cid.Cid, entries []entry) error {
	arcs := make(map[arcset.Path]cid.Cid, 1+len(entries)*2)

	countCID, err := encodeCountMarker(uint64(len(entries)))
	if err != nil {
		return err
	}
	arcs[countPath(root)] = countCID

	for i, ent := range entries {
		keyCID, err := encodePathMarker(ent.path)
		if err != nil {
			return err
		}
		arcs[entryKeyPath(root, uint64(i))] = keyCID
		arcs[entryValuePath(root, uint64(i))] = ent.value
	}

	snapshot, err := arcset.NewArcSetFromPaths(arcs)
	if err != nil {
		return err
	}
	return s.arctable.Update(ctx, namespace, cid.Undef, cid.Undef, snapshot)
}

func countPath(root cid.Cid) arcset.Path {
	return arcset.CanonicalizePath(fmt.Sprintf("runtime/map/%s/count", root.String()))
}

func entryKeyPath(root cid.Cid, index uint64) arcset.Path {
	return arcset.CanonicalizePath(fmt.Sprintf("runtime/map/%s/entries/%d/key", root.String(), index))
}

func entryValuePath(root cid.Cid, index uint64) arcset.Path {
	return arcset.CanonicalizePath(fmt.Sprintf("runtime/map/%s/entries/%d/value", root.String(), index))
}

func encodeCountMarker(count uint64) (cid.Cid, error) {
	payload := []byte(countPrefix + strconv.FormatUint(count, 10))
	sum, err := mh.Sum(payload, mh.IDENTITY, len(payload))
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, sum), nil
}

func decodeCountMarker(marker cid.Cid) (uint64, error) {
	payload, err := decodeIdentityPayload(marker)
	if err != nil {
		return 0, err
	}
	if len(payload) < len(countPrefix) || string(payload[:len(countPrefix)]) != countPrefix {
		return 0, fmt.Errorf("count marker prefix mismatch")
	}
	return strconv.ParseUint(string(payload[len(countPrefix):]), 10, 64)
}

func encodePathMarker(path arcset.Path) (cid.Cid, error) {
	if path.IsEmpty() {
		return cid.Undef, fmt.Errorf("path is empty")
	}
	payload := []byte(pathPrefix + path.String())
	sum, err := mh.Sum(payload, mh.IDENTITY, len(payload))
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, sum), nil
}

func decodePathMarker(marker cid.Cid) (arcset.Path, error) {
	payload, err := decodeIdentityPayload(marker)
	if err != nil {
		return "", err
	}
	if len(payload) < len(pathPrefix) || string(payload[:len(pathPrefix)]) != pathPrefix {
		return "", fmt.Errorf("path marker prefix mismatch")
	}
	return arcset.CanonicalizePath(string(payload[len(pathPrefix):])), nil
}

func decodeIdentityPayload(value cid.Cid) ([]byte, error) {
	decoded, err := mh.Decode(value.Hash())
	if err != nil {
		return nil, err
	}
	if decoded.Code != mh.IDENTITY {
		return nil, fmt.Errorf("marker is not identity-encoded")
	}
	return decoded.Digest, nil
}

var _ mapping.Semantics = (*Map)(nil)

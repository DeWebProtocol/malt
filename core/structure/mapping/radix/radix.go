// Package radix implements a digest-keyed radix-map semantic above the
// primitive index commitment backends.
package radix

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/dewebprotocol/malt/core/arctable"
	"github.com/dewebprotocol/malt/core/commitment"
	"github.com/dewebprotocol/malt/core/structure"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

const (
	fanout = 256

	leafPrefix        = "malt:map:radix:leaf:v1:"
	bucketRefPrefix   = "malt:map:radix:bucket:v1:"
	bucketCountPrefix = "malt:map:radix:bucket-count:v1:"
)

type Map struct {
	scheme   commitment.IndexCommitment
	arctable arctable.ArcTable
}

type leafBinding struct {
	path   arcset.Path
	value  cid.Cid
	digest [sha256.Size]byte
}

type proofEnvelope struct {
	Steps  []proofStep    `json:"steps"`
	Bucket *bucketWitness `json:"bucket,omitempty"`
}

type proofStep struct {
	Slot  []byte `json:"slot,omitempty"`
	Proof []byte `json:"proof"`
}

type bucketWitness struct {
	Proof []byte `json:"proof"`
}

func NewMap(scheme commitment.IndexCommitment, e arctable.ArcTable) (*Map, error) {
	if scheme == nil {
		return nil, fmt.Errorf("scheme is nil")
	}
	if e == nil {
		return nil, fmt.Errorf("arctable is nil")
	}
	if scheme.MaxValues() < fanout {
		return nil, fmt.Errorf("index commitment capacity %d is smaller than radix fanout %d", scheme.MaxValues(), fanout)
	}
	return &Map{scheme: scheme, arctable: e}, nil
}

func (s *Map) Commit(ctx context.Context, bucketID string, view mapping.View) (cid.Cid, error) {
	bindings, err := extractBindings(view)
	if err != nil {
		return cid.Undef, err
	}
	return s.commitRoot(ctx, bucketID, bindings)
}

func (s *Map) Prove(ctx context.Context, bucketID string, root cid.Cid, key arcset.Path) (mapping.Binding, structure.Proof, error) {
	if !root.Defined() {
		return mapping.Binding{}, nil, fmt.Errorf("root is undefined")
	}
	if key.IsEmpty() {
		return mapping.Binding{}, nil, fmt.Errorf("key is empty")
	}

	digest := hashPath(key)
	currentRoot := root
	envelope := proofEnvelope{}

	for depth := 0; depth < len(digest); depth++ {
		slots, err := s.loadValidatedNode(ctx, bucketID, currentRoot)
		if err != nil {
			return mapping.Binding{}, nil, err
		}

		slotIndex := digest[depth]
		provedRoot, value, proof, err := s.scheme.Prove(cellsFromCIDs(slots), uint64(slotIndex))
		if err != nil {
			return mapping.Binding{}, nil, err
		}
		if !provedRoot.Equals(currentRoot) {
			return mapping.Binding{}, nil, fmt.Errorf("proved root does not match requested node root")
		}

		slotCID, err := value.AsCID()
		if err != nil {
			return mapping.Binding{}, nil, err
		}
		envelope.Steps = append(envelope.Steps, proofStep{
			Slot:  cidBytes(slotCID),
			Proof: proof,
		})

		if !slotCID.Defined() {
			return mapping.Binding{}, nil, fmt.Errorf("path %s not found", key.String())
		}

		if leafPath, leafValue, ok, err := tryDecodeLeafMarker(slotCID); err != nil {
			return mapping.Binding{}, nil, err
		} else if ok {
			if leafPath != key {
				return mapping.Binding{}, nil, fmt.Errorf("path %s not found", key.String())
			}
			proofBytes, err := json.Marshal(envelope)
			if err != nil {
				return mapping.Binding{}, nil, err
			}
			return mapping.Binding{Value: leafValue, Present: true}, structure.Proof(proofBytes), nil
		}

		if bucketRoot, ok, err := tryDecodeBucketRef(slotCID); err != nil {
			return mapping.Binding{}, nil, err
		} else if ok {
			markers, err := s.loadBucketEntries(ctx, bucketID, bucketRoot)
			if err != nil {
				return mapping.Binding{}, nil, err
			}
			index := -1
			for i, marker := range markers {
				leafPath, _, err := decodeLeafMarker(marker)
				if err != nil {
					return mapping.Binding{}, nil, err
				}
				if leafPath == key {
					index = i
					break
				}
			}
			if index < 0 {
				return mapping.Binding{}, nil, fmt.Errorf("path %s not found", key.String())
			}

			provedRoot, value, proof, err := s.scheme.Prove(cellsFromCIDs(markers), uint64(index))
			if err != nil {
				return mapping.Binding{}, nil, err
			}
			if !provedRoot.Equals(bucketRoot) {
				return mapping.Binding{}, nil, fmt.Errorf("bucket proof root mismatch")
			}
			_, leafValue, err := decodeLeafMarkerCID(value)
			if err != nil {
				return mapping.Binding{}, nil, err
			}
			envelope.Bucket = &bucketWitness{Proof: proof}
			proofBytes, err := json.Marshal(envelope)
			if err != nil {
				return mapping.Binding{}, nil, err
			}
			return mapping.Binding{Value: leafValue, Present: true}, structure.Proof(proofBytes), nil
		}

		currentRoot = slotCID
	}

	return mapping.Binding{}, nil, fmt.Errorf("path %s not found", key.String())
}

func (s *Map) Verify(root cid.Cid, key arcset.Path, expected mapping.Binding, proof structure.Proof) (bool, error) {
	if !expected.Present {
		return false, fmt.Errorf("non-membership verification is not implemented")
	}
	if !root.Defined() {
		return false, fmt.Errorf("root is undefined")
	}
	if key.IsEmpty() {
		return false, fmt.Errorf("key is empty")
	}

	var envelope proofEnvelope
	if err := json.Unmarshal(proof, &envelope); err != nil {
		return false, err
	}
	if len(envelope.Steps) == 0 {
		return false, fmt.Errorf("missing proof steps")
	}

	digest := hashPath(key)
	if len(envelope.Steps) > len(digest) {
		return false, fmt.Errorf("proof has too many radix steps")
	}
	currentRoot := root
	expectedLeaf, err := encodeLeafMarker(key, expected.Value)
	if err != nil {
		return false, err
	}

	for depth, step := range envelope.Steps {
		var slotCID cid.Cid
		if len(step.Slot) > 0 {
			slotCID, err = cid.Cast(step.Slot)
			if err != nil {
				return false, err
			}
		}

		ok, err := s.scheme.VerifyIndex(currentRoot, uint64(digest[depth]), commitment.CellFromCID(slotCID), step.Proof)
		if err != nil || !ok {
			return ok, err
		}
		if !slotCID.Defined() {
			return false, nil
		}

		if leafPath, leafValue, ok, err := tryDecodeLeafMarker(slotCID); err != nil {
			return false, err
		} else if ok {
			if depth != len(envelope.Steps)-1 || envelope.Bucket != nil {
				return false, nil
			}
			return leafPath == key && leafValue.Equals(expected.Value), nil
		}

		if bucketRoot, ok, err := tryDecodeBucketRef(slotCID); err != nil {
			return false, err
		} else if ok {
			if depth != len(envelope.Steps)-1 || envelope.Bucket == nil {
				return false, nil
			}
			return s.scheme.VerifyProof(bucketRoot, commitment.CellFromCID(expectedLeaf), envelope.Bucket.Proof)
		}

		if depth == len(envelope.Steps)-1 {
			return false, nil
		}
		currentRoot = slotCID
	}

	return false, nil
}

func (s *Map) Update(ctx context.Context, bucketID string, root cid.Cid, key arcset.Path, oldValue, newValue cid.Cid) (cid.Cid, error) {
	if !root.Defined() {
		return cid.Undef, fmt.Errorf("root is undefined")
	}
	if key.IsEmpty() {
		return cid.Undef, fmt.Errorf("key is empty")
	}

	rootSlots, err := s.loadValidatedNode(ctx, bucketID, root)
	if err != nil {
		return cid.Undef, err
	}

	digest := hashPath(key)
	slotIndex := digest[0]
	nextSlot, err := s.updateSubtree(ctx, bucketID, rootSlots[slotIndex], digest, 1, key, oldValue, newValue)
	if err != nil {
		return cid.Undef, err
	}
	if cidEqual(nextSlot, rootSlots[slotIndex]) {
		return root, nil
	}

	nextSlots := cloneCIDs(rootSlots)
	nextSlots[slotIndex] = nextSlot
	return s.commitNode(ctx, bucketID, nextSlots)
}

func (s *Map) updateSubtree(
	ctx context.Context,
	bucketID string,
	current cid.Cid,
	digest [sha256.Size]byte,
	depth int,
	key arcset.Path,
	oldValue, newValue cid.Cid,
) (cid.Cid, error) {
	if !current.Defined() {
		if oldValue.Defined() {
			return cid.Undef, fmt.Errorf("path %s is absent", key.String())
		}
		if !newValue.Defined() {
			return cid.Undef, nil
		}
		return encodeLeafMarker(key, newValue)
	}

	if leafPath, leafValue, ok, err := tryDecodeLeafMarker(current); err != nil {
		return cid.Undef, err
	} else if ok {
		switch {
		case leafPath == key:
			if !oldValue.Defined() {
				return cid.Undef, fmt.Errorf("path %s already exists", key.String())
			}
			if !leafValue.Equals(oldValue) {
				return cid.Undef, fmt.Errorf("old value mismatch at path %s", key.String())
			}
			if !newValue.Defined() {
				return cid.Undef, nil
			}
			return encodeLeafMarker(key, newValue)
		default:
			if oldValue.Defined() {
				return cid.Undef, fmt.Errorf("path %s is absent", key.String())
			}
			if !newValue.Defined() {
				return current, nil
			}
			existing := newLeafBinding(leafPath, leafValue)
			inserted := leafBinding{path: key, value: newValue, digest: digest}
			return s.buildSubtree(ctx, bucketID, []leafBinding{existing, inserted}, depth)
		}
	}

	if bucketRoot, ok, err := tryDecodeBucketRef(current); err != nil {
		return cid.Undef, err
	} else if ok {
		return s.updateBucket(ctx, bucketID, bucketRoot, key, oldValue, newValue)
	}

	if depth >= len(digest) {
		return cid.Undef, fmt.Errorf("unexpected radix depth overflow")
	}

	slots, err := s.loadValidatedNode(ctx, bucketID, current)
	if err != nil {
		return cid.Undef, err
	}

	slotIndex := digest[depth]
	nextSlot, err := s.updateSubtree(ctx, bucketID, slots[slotIndex], digest, depth+1, key, oldValue, newValue)
	if err != nil {
		return cid.Undef, err
	}
	if cidEqual(nextSlot, slots[slotIndex]) {
		return current, nil
	}

	nextSlots := cloneCIDs(slots)
	nextSlots[slotIndex] = nextSlot
	return s.commitOrCollapseNode(ctx, bucketID, nextSlots)
}

func (s *Map) updateBucket(ctx context.Context, bucketID string, bucketRoot cid.Cid, key arcset.Path, oldValue, newValue cid.Cid) (cid.Cid, error) {
	markers, err := s.loadBucketEntries(ctx, bucketID, bucketRoot)
	if err != nil {
		return cid.Undef, err
	}

	index := -1
	var currentValue cid.Cid
	for i, marker := range markers {
		leafPath, leafValue, err := decodeLeafMarker(marker)
		if err != nil {
			return cid.Undef, err
		}
		if leafPath == key {
			index = i
			currentValue = leafValue
			break
		}
	}

	switch {
	case index >= 0:
		if !oldValue.Defined() {
			return cid.Undef, fmt.Errorf("path %s already exists", key.String())
		}
		if !currentValue.Equals(oldValue) {
			return cid.Undef, fmt.Errorf("old value mismatch at path %s", key.String())
		}
		if !newValue.Defined() {
			next := append([]cid.Cid(nil), markers[:index]...)
			next = append(next, markers[index+1:]...)
			return s.commitBucketMarkers(ctx, bucketID, next)
		}

		nextMarker, err := encodeLeafMarker(key, newValue)
		if err != nil {
			return cid.Undef, err
		}
		if len(markers) == 1 {
			return nextMarker, nil
		}
		root, err := s.scheme.Replace(cellsFromCIDs(markers), uint64(index), commitment.CellFromCID(markers[index]), commitment.CellFromCID(nextMarker))
		if err != nil {
			return cid.Undef, err
		}
		next := cloneCIDs(markers)
		next[index] = nextMarker
		if err := s.storeBucketEntries(ctx, bucketID, root, next); err != nil {
			return cid.Undef, err
		}
		return encodeBucketRef(root)

	default:
		if oldValue.Defined() {
			return cid.Undef, fmt.Errorf("path %s is absent", key.String())
		}
		if !newValue.Defined() {
			return encodeBucketRef(bucketRoot)
		}
		nextMarker, err := encodeLeafMarker(key, newValue)
		if err != nil {
			return cid.Undef, err
		}
		next := append([]cid.Cid(nil), markers...)
		next = append(next, nextMarker)
		slices.SortFunc(next, func(a, b cid.Cid) int {
			ap, _, err := decodeLeafMarker(a)
			if err != nil {
				return 0
			}
			bp, _, err := decodeLeafMarker(b)
			if err != nil {
				return 0
			}
			switch {
			case ap < bp:
				return -1
			case ap > bp:
				return 1
			default:
				return 0
			}
		})
		return s.commitBucketMarkers(ctx, bucketID, next)
	}
}

func (s *Map) commitRoot(ctx context.Context, bucketID string, bindings []leafBinding) (cid.Cid, error) {
	slots := make([]cid.Cid, fanout)
	for slotIndex, group := range groupBindings(bindings, 0) {
		child, err := s.buildSubtree(ctx, bucketID, group, 1)
		if err != nil {
			return cid.Undef, err
		}
		slots[slotIndex] = child
	}
	return s.commitNode(ctx, bucketID, slots)
}

func (s *Map) buildSubtree(ctx context.Context, bucketID string, bindings []leafBinding, depth int) (cid.Cid, error) {
	switch len(bindings) {
	case 0:
		return cid.Undef, nil
	case 1:
		return encodeLeafMarker(bindings[0].path, bindings[0].value)
	}

	if depth >= sha256.Size || allSameDigest(bindings) {
		markers := make([]cid.Cid, len(bindings))
		for i, binding := range bindings {
			marker, err := encodeLeafMarker(binding.path, binding.value)
			if err != nil {
				return cid.Undef, err
			}
			markers[i] = marker
		}
		return s.commitBucketMarkers(ctx, bucketID, markers)
	}

	slots := make([]cid.Cid, fanout)
	for slotIndex, group := range groupBindings(bindings, depth) {
		child, err := s.buildSubtree(ctx, bucketID, group, depth+1)
		if err != nil {
			return cid.Undef, err
		}
		slots[slotIndex] = child
	}
	return s.commitNode(ctx, bucketID, slots)
}

func (s *Map) commitNode(ctx context.Context, bucketID string, slots []cid.Cid) (cid.Cid, error) {
	root, err := s.scheme.Commit(cellsFromCIDs(slots))
	if err != nil {
		return cid.Undef, err
	}
	if err := s.storeNodeSlots(ctx, bucketID, root, slots); err != nil {
		return cid.Undef, err
	}
	return root, nil
}

func (s *Map) commitOrCollapseNode(ctx context.Context, bucketID string, slots []cid.Cid) (cid.Cid, error) {
	var only cid.Cid
	count := 0
	for _, slot := range slots {
		if !slot.Defined() {
			continue
		}
		count++
		only = slot
		if count > 1 {
			break
		}
	}
	if count == 0 {
		return cid.Undef, nil
	}
	if count == 1 {
		if _, _, ok, err := tryDecodeLeafMarker(only); err != nil {
			return cid.Undef, err
		} else if ok {
			return only, nil
		}
		if _, ok, err := tryDecodeBucketRef(only); err != nil {
			return cid.Undef, err
		} else if ok {
			return only, nil
		}
	}
	return s.commitNode(ctx, bucketID, slots)
}

func (s *Map) commitBucketMarkers(ctx context.Context, bucketID string, markers []cid.Cid) (cid.Cid, error) {
	switch len(markers) {
	case 0:
		return cid.Undef, nil
	case 1:
		return markers[0], nil
	}
	if len(markers) > s.scheme.MaxValues() {
		return cid.Undef, fmt.Errorf("bucket size %d exceeds commitment capacity %d", len(markers), s.scheme.MaxValues())
	}

	root, err := s.scheme.Commit(cellsFromCIDs(markers))
	if err != nil {
		return cid.Undef, err
	}
	if err := s.storeBucketEntries(ctx, bucketID, root, markers); err != nil {
		return cid.Undef, err
	}
	return encodeBucketRef(root)
}

func extractBindings(view mapping.View) ([]leafBinding, error) {
	if view == nil {
		return nil, fmt.Errorf("view is nil")
	}

	bindings := make([]leafBinding, 0, view.Len())
	iter := view.Iterate()
	for {
		path, value, ok := iter.Next()
		if !ok {
			break
		}
		bindings = append(bindings, newLeafBinding(path, value))
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}
	if !slices.IsSortedFunc(bindings, func(a, b leafBinding) int {
		switch {
		case a.path < b.path:
			return -1
		case a.path > b.path:
			return 1
		default:
			return 0
		}
	}) {
		return nil, fmt.Errorf("view iteration is not in canonical key order")
	}
	return bindings, nil
}

func groupBindings(bindings []leafBinding, depth int) map[byte][]leafBinding {
	grouped := make(map[byte][]leafBinding)
	for _, binding := range bindings {
		grouped[binding.digest[depth]] = append(grouped[binding.digest[depth]], binding)
	}
	return grouped
}

func allSameDigest(bindings []leafBinding) bool {
	for i := 1; i < len(bindings); i++ {
		if bindings[i].digest != bindings[0].digest {
			return false
		}
	}
	return true
}

func newLeafBinding(path arcset.Path, value cid.Cid) leafBinding {
	return leafBinding{
		path:   path,
		value:  value,
		digest: hashPath(path),
	}
}

func hashPath(path arcset.Path) [sha256.Size]byte {
	return sha256.Sum256([]byte(path.String()))
}

func (s *Map) loadValidatedNode(ctx context.Context, bucketID string, root cid.Cid) ([]cid.Cid, error) {
	slots, err := s.loadNodeSlots(ctx, bucketID, root)
	if err != nil {
		return nil, err
	}
	recomputed, err := s.scheme.Commit(cellsFromCIDs(slots))
	if err != nil {
		return nil, err
	}
	if !recomputed.Equals(root) {
		return nil, fmt.Errorf("materialized node state does not match root %s", root.String())
	}
	return slots, nil
}

func (s *Map) loadNodeSlots(ctx context.Context, bucketID string, root cid.Cid) ([]cid.Cid, error) {
	paths := make([]arcset.Path, fanout)
	for i := 0; i < fanout; i++ {
		paths[i] = nodeSlotPath(root, byte(i))
	}
	found, err := s.arctable.BatchGet(ctx, bucketID, cid.Undef, paths)
	if err != nil {
		return nil, err
	}

	slots := make([]cid.Cid, fanout)
	for i, path := range paths {
		if target, ok := found[path]; ok {
			slots[i] = target
		}
	}
	return slots, nil
}

func (s *Map) storeNodeSlots(ctx context.Context, bucketID string, root cid.Cid, slots []cid.Cid) error {
	arcs := make(map[arcset.Path]cid.Cid)
	for i, slot := range slots {
		if !slot.Defined() {
			continue
		}
		arcs[nodeSlotPath(root, byte(i))] = slot
	}
	if len(arcs) == 0 {
		return nil
	}
	snapshot, err := arcset.NewArcSetFromPaths(arcs)
	if err != nil {
		return err
	}
	return s.arctable.Update(ctx, bucketID, cid.Undef, cid.Undef, snapshot)
}

func (s *Map) loadBucketEntries(ctx context.Context, bucketID string, root cid.Cid) ([]cid.Cid, error) {
	countCID, err := s.arctable.Get(ctx, bucketID, cid.Undef, bucketCountPath(root))
	if err != nil {
		return nil, err
	}
	count, err := decodeBucketCountMarker(countCID)
	if err != nil {
		return nil, err
	}

	paths := make([]arcset.Path, count)
	for i := uint64(0); i < count; i++ {
		paths[i] = bucketEntryPath(root, i)
	}
	found, err := s.arctable.BatchGet(ctx, bucketID, cid.Undef, paths)
	if err != nil {
		return nil, err
	}

	markers := make([]cid.Cid, count)
	for i, path := range paths {
		marker, ok := found[path]
		if !ok {
			return nil, fmt.Errorf("missing bucket entry %d", i)
		}
		markers[i] = marker
	}
	return markers, nil
}

func (s *Map) storeBucketEntries(ctx context.Context, bucketID string, root cid.Cid, markers []cid.Cid) error {
	arcs := make(map[arcset.Path]cid.Cid, len(markers)+1)
	countMarker, err := encodeBucketCountMarker(uint64(len(markers)))
	if err != nil {
		return err
	}
	arcs[bucketCountPath(root)] = countMarker
	for i, marker := range markers {
		arcs[bucketEntryPath(root, uint64(i))] = marker
	}
	snapshot, err := arcset.NewArcSetFromPaths(arcs)
	if err != nil {
		return err
	}
	return s.arctable.Update(ctx, bucketID, cid.Undef, cid.Undef, snapshot)
}

func cellsFromCIDs(values []cid.Cid) []commitment.Cell {
	cells := make([]commitment.Cell, len(values))
	for i, value := range values {
		cells[i] = commitment.CellFromCID(value)
	}
	return cells
}

func cloneCIDs(values []cid.Cid) []cid.Cid {
	return append([]cid.Cid(nil), values...)
}

func cidEqual(a, b cid.Cid) bool {
	if !a.Defined() && !b.Defined() {
		return true
	}
	return a.Equals(b)
}

func cidBytes(value cid.Cid) []byte {
	if !value.Defined() {
		return nil
	}
	return value.Bytes()
}

func nodeSlotPath(root cid.Cid, slot byte) arcset.Path {
	return arcset.CanonicalizePath(fmt.Sprintf("runtime/map/radix/nodes/%s/slots/%d", root.String(), slot))
}

func bucketCountPath(root cid.Cid) arcset.Path {
	return arcset.CanonicalizePath(fmt.Sprintf("runtime/map/radix/buckets/%s/count", root.String()))
}

func bucketEntryPath(root cid.Cid, index uint64) arcset.Path {
	return arcset.CanonicalizePath(fmt.Sprintf("runtime/map/radix/buckets/%s/entries/%d", root.String(), index))
}

func encodeLeafMarker(path arcset.Path, value cid.Cid) (cid.Cid, error) {
	if path.IsEmpty() {
		return cid.Undef, fmt.Errorf("path is empty")
	}
	if !value.Defined() {
		return cid.Undef, fmt.Errorf("value is undefined")
	}

	pathBytes := []byte(path.String())
	if len(pathBytes) > 0xffff {
		return cid.Undef, fmt.Errorf("path %q is too long", path.String())
	}

	payload := make([]byte, 0, len(leafPrefix)+2+len(pathBytes)+len(value.Bytes()))
	payload = append(payload, []byte(leafPrefix)...)
	payload = binary.BigEndian.AppendUint16(payload, uint16(len(pathBytes)))
	payload = append(payload, pathBytes...)
	payload = append(payload, value.Bytes()...)

	sum, err := mh.Sum(payload, mh.IDENTITY, len(payload))
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, sum), nil
}

func decodeLeafMarker(marker cid.Cid) (arcset.Path, cid.Cid, error) {
	payload, err := decodeIdentityPayload(marker)
	if err != nil {
		return "", cid.Undef, err
	}
	if len(payload) < len(leafPrefix)+2 || string(payload[:len(leafPrefix)]) != leafPrefix {
		return "", cid.Undef, fmt.Errorf("leaf marker prefix mismatch")
	}
	pathLen := int(binary.BigEndian.Uint16(payload[len(leafPrefix) : len(leafPrefix)+2]))
	offset := len(leafPrefix) + 2
	if len(payload) < offset+pathLen {
		return "", cid.Undef, fmt.Errorf("leaf marker truncated")
	}
	path := arcset.CanonicalizePath(string(payload[offset : offset+pathLen]))
	value, err := cid.Cast(payload[offset+pathLen:])
	if err != nil {
		return "", cid.Undef, err
	}
	return path, value, nil
}

func tryDecodeLeafMarker(marker cid.Cid) (arcset.Path, cid.Cid, bool, error) {
	payload, err := decodeIdentityPayload(marker)
	if err != nil {
		if !marker.Defined() {
			return "", cid.Undef, false, nil
		}
		return "", cid.Undef, false, nil
	}
	if len(payload) < len(leafPrefix) || string(payload[:len(leafPrefix)]) != leafPrefix {
		return "", cid.Undef, false, nil
	}
	path, value, err := decodeLeafMarker(marker)
	return path, value, err == nil, err
}

func decodeLeafMarkerCID(cell commitment.Cell) (arcset.Path, cid.Cid, error) {
	slotCID, err := cell.AsCID()
	if err != nil {
		return "", cid.Undef, err
	}
	return decodeLeafMarker(slotCID)
}

func encodeBucketRef(root cid.Cid) (cid.Cid, error) {
	if !root.Defined() {
		return cid.Undef, fmt.Errorf("bucket root is undefined")
	}
	payload := make([]byte, 0, len(bucketRefPrefix)+len(root.Bytes()))
	payload = append(payload, []byte(bucketRefPrefix)...)
	payload = append(payload, root.Bytes()...)
	sum, err := mh.Sum(payload, mh.IDENTITY, len(payload))
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, sum), nil
}

func tryDecodeBucketRef(marker cid.Cid) (cid.Cid, bool, error) {
	payload, err := decodeIdentityPayload(marker)
	if err != nil {
		if !marker.Defined() {
			return cid.Undef, false, nil
		}
		return cid.Undef, false, nil
	}
	if len(payload) < len(bucketRefPrefix) || string(payload[:len(bucketRefPrefix)]) != bucketRefPrefix {
		return cid.Undef, false, nil
	}
	root, err := cid.Cast(payload[len(bucketRefPrefix):])
	return root, err == nil, err
}

func encodeBucketCountMarker(count uint64) (cid.Cid, error) {
	payload := make([]byte, 0, len(bucketCountPrefix)+8)
	payload = append(payload, []byte(bucketCountPrefix)...)
	payload = binary.BigEndian.AppendUint64(payload, count)
	sum, err := mh.Sum(payload, mh.IDENTITY, len(payload))
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, sum), nil
}

func decodeBucketCountMarker(marker cid.Cid) (uint64, error) {
	payload, err := decodeIdentityPayload(marker)
	if err != nil {
		return 0, err
	}
	if len(payload) != len(bucketCountPrefix)+8 || string(payload[:len(bucketCountPrefix)]) != bucketCountPrefix {
		return 0, fmt.Errorf("bucket count marker prefix mismatch")
	}
	return binary.BigEndian.Uint64(payload[len(bucketCountPrefix):]), nil
}

func decodeIdentityPayload(value cid.Cid) ([]byte, error) {
	if !value.Defined() {
		return nil, fmt.Errorf("marker is undefined")
	}
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

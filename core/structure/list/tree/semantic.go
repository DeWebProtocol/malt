// Package tree implements the stable-indexed list semantic using a tree-shaped
// fixed-slot layout. Runtime operations execute against node materialization
// stored in EAT, so proofs and updates do not require a caller-supplied view.
package tree

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/sce/commitment"
	"github.com/dewebprotocol/malt/core/structure"
	"github.com/dewebprotocol/malt/core/structure/list"
	listruntime "github.com/dewebprotocol/malt/core/structure/list/internal/runtime"
	cid "github.com/ipfs/go-cid"
)

type Semantic struct {
	scheme   commitment.Scheme
	eat      eat.EAT
	bucketID string
}

type proofEnvelope struct {
	LengthProof []byte      `json:"length_proof"`
	Steps       []proofStep `json:"steps,omitempty"`
}

type proofStep struct {
	Target []byte `json:"target"`
	Proof  []byte `json:"proof"`
}

func New(scheme commitment.Scheme, eat eat.EAT, bucketID string) (*Semantic, error) {
	if err := listruntime.ValidateScheme(scheme); err != nil {
		return nil, err
	}
	if eat == nil {
		return nil, fmt.Errorf("eat is nil")
	}
	if bucketID == "" {
		return nil, fmt.Errorf("bucket id is empty")
	}
	return &Semantic{
		scheme:   scheme,
		eat:      eat,
		bucketID: bucketID,
	}, nil
}

func (s *Semantic) Commit(ctx context.Context, view list.View) (cid.Cid, error) {
	values, err := valuesFromView(view)
	if err != nil {
		return cid.Undef, err
	}
	return s.buildFromValues(ctx, values, listruntime.RequiredHeight(uint64(len(values))), true)
}

func (s *Semantic) Prove(ctx context.Context, root cid.Cid, index uint64) (list.Query, structure.Proof, error) {
	rootSlots, length, err := s.loadRoot(ctx, root)
	if err != nil {
		return list.Query{}, nil, err
	}

	query := list.Query{Length: length}
	_, lengthProof, err := listruntime.ProveSlot(s.scheme, root, rootSlots, 0)
	if err != nil {
		return list.Query{}, nil, err
	}
	envelope := proofEnvelope{LengthProof: lengthProof}

	if index >= length {
		query.Key = cid.Undef
		return encodeProof(query, envelope)
	}

	height := listruntime.RequiredHeight(length)
	digits, err := listruntime.IndexDigits(index, height)
	if err != nil {
		return list.Query{}, nil, err
	}

	currentRoot := root
	currentSlots := rootSlots
	for level, digit := range digits {
		slot := uint64(digit)
		if level == 0 {
			slot++
		}

		target, proof, err := listruntime.ProveSlot(s.scheme, currentRoot, currentSlots, slot)
		if err != nil {
			return list.Query{}, nil, err
		}
		envelope.Steps = append(envelope.Steps, newProofStep(target, proof))

		if level == len(digits)-1 {
			query.Key = target
			return encodeProof(query, envelope)
		}
		if !target.Defined() {
			return list.Query{}, nil, fmt.Errorf("missing child at level %d digit %d", level, digit)
		}

		currentRoot = target
		currentSlots, err = s.loadNode(ctx, currentRoot, false)
		if err != nil {
			return list.Query{}, nil, err
		}
	}

	return list.Query{}, nil, fmt.Errorf("unreachable proof state")
}

func (s *Semantic) Verify(root cid.Cid, index uint64, expected list.Query, proof structure.Proof) (bool, error) {
	var envelope proofEnvelope
	if err := json.Unmarshal(proof, &envelope); err != nil {
		return false, err
	}
	if len(envelope.LengthProof) == 0 {
		return false, fmt.Errorf("missing length proof")
	}

	lengthMarker, err := listruntime.EncodeLengthMarker(expected.Length)
	if err != nil {
		return false, err
	}
	ok, err := listruntime.VerifySlot(s.scheme, root, 0, lengthMarker, envelope.LengthProof)
	if err != nil || !ok {
		return ok, err
	}

	if index >= expected.Length {
		if expected.Key.Defined() {
			return false, nil
		}
		return len(envelope.Steps) == 0, nil
	}
	if !expected.Key.Defined() {
		return false, nil
	}

	height := listruntime.RequiredHeight(expected.Length)
	if len(envelope.Steps) != height+1 {
		return false, nil
	}

	digits, err := listruntime.IndexDigits(index, height)
	if err != nil {
		return false, err
	}

	currentRoot := root
	for level, digit := range digits {
		step := envelope.Steps[level]
		target, err := parseStepTarget(step)
		if err != nil {
			return false, err
		}

		slot := uint64(digit)
		if level == 0 {
			slot++
		}
		ok, err := listruntime.VerifySlot(s.scheme, currentRoot, slot, target, step.Proof)
		if err != nil || !ok {
			return ok, err
		}

		if level == len(digits)-1 {
			return target.Equals(expected.Key), nil
		}
		currentRoot = target
	}

	return false, nil
}

func (s *Semantic) Replace(ctx context.Context, root cid.Cid, index uint64, oldKey, newKey cid.Cid) (cid.Cid, error) {
	_, length, err := s.loadRoot(ctx, root)
	if err != nil {
		return cid.Undef, err
	}
	if index >= length {
		return cid.Undef, fmt.Errorf("index %d out of range", index)
	}
	return s.replaceAt(ctx, root, true, listruntime.RequiredHeight(length), index, oldKey, newKey)
}

func (s *Semantic) Append(ctx context.Context, root cid.Cid, key cid.Cid) (cid.Cid, uint64, error) {
	rootSlots, length, err := s.loadRoot(ctx, root)
	if err != nil {
		return cid.Undef, 0, err
	}

	newIndex := length
	newLength := length + 1
	oldHeight := listruntime.RequiredHeight(length)
	newHeight := listruntime.RequiredHeight(newLength)

	if newHeight > oldHeight {
		grownRoot, err := s.growRoot(ctx, root, oldHeight, length)
		if err != nil {
			return cid.Undef, 0, err
		}

		nextRootSlots := listruntime.EmptyRootSlots()
		lengthMarker, err := listruntime.EncodeLengthMarker(newLength)
		if err != nil {
			return cid.Undef, 0, err
		}
		nextRootSlots[0] = lengthMarker
		content := listruntime.ContentSlots(nextRootSlots, true)
		content[0] = grownRoot

		childSpan, err := listruntime.SubtreeCapacity(newHeight - 1)
		if err != nil {
			return cid.Undef, 0, err
		}
		rootDigit := int(newIndex / childSpan)
		localIndex := newIndex % childSpan
		childRoot, err := s.buildSparseSubtree(ctx, newHeight-1, localIndex, key)
		if err != nil {
			return cid.Undef, 0, err
		}
		content[rootDigit] = childRoot

		newRoot, err := s.commitSlots(ctx, nextRootSlots)
		return newRoot, newIndex, err
	}

	nextRootSlots := cloneSlots(rootSlots)
	lengthMarker, err := listruntime.EncodeLengthMarker(newLength)
	if err != nil {
		return cid.Undef, 0, err
	}
	nextRootSlots[0] = lengthMarker
	content := listruntime.ContentSlots(nextRootSlots, true)

	if oldHeight == 0 {
		if content[newIndex].Defined() {
			return cid.Undef, 0, fmt.Errorf("append slot %d is already occupied", newIndex)
		}
		content[newIndex] = key
		newRoot, err := s.commitSlots(ctx, nextRootSlots)
		return newRoot, newIndex, err
	}

	childSpan, err := listruntime.SubtreeCapacity(oldHeight - 1)
	if err != nil {
		return cid.Undef, 0, err
	}
	digit := int(newIndex / childSpan)
	localIndex := newIndex % childSpan

	if content[digit].Defined() {
		content[digit], err = s.appendInto(ctx, content[digit], oldHeight-1, localIndex, key)
	} else {
		content[digit], err = s.buildSparseSubtree(ctx, oldHeight-1, localIndex, key)
	}
	if err != nil {
		return cid.Undef, 0, err
	}

	newRoot, err := s.commitSlots(ctx, nextRootSlots)
	return newRoot, newIndex, err
}

func (s *Semantic) Truncate(ctx context.Context, root cid.Cid, newLen uint64) (cid.Cid, error) {
	_, oldLen, err := s.loadRoot(ctx, root)
	if err != nil {
		return cid.Undef, err
	}
	if newLen > oldLen {
		return cid.Undef, fmt.Errorf("new length %d exceeds current length %d", newLen, oldLen)
	}
	if newLen == oldLen {
		return root, nil
	}
	if newLen == 0 {
		return s.commitEmptyRoot(ctx)
	}

	oldHeight := listruntime.RequiredHeight(oldLen)
	newHeight := listruntime.RequiredHeight(newLen)
	return s.rebuildPrefix(ctx, root, true, oldHeight, true, newHeight, newLen)
}

func (s *Semantic) buildFromValues(ctx context.Context, values []cid.Cid, height int, isRoot bool) (cid.Cid, error) {
	var slots []cid.Cid
	if isRoot {
		slots = listruntime.EmptyRootSlots()
		lengthMarker, err := listruntime.EncodeLengthMarker(uint64(len(values)))
		if err != nil {
			return cid.Undef, err
		}
		slots[0] = lengthMarker
	} else {
		slots = listruntime.EmptyNodeSlots()
	}

	content := listruntime.ContentSlots(slots, isRoot)
	if height == 0 {
		copy(content, values)
		return s.commitSlots(ctx, slots)
	}

	childSpan, err := listruntime.SubtreeCapacity(height - 1)
	if err != nil {
		return cid.Undef, err
	}
	for childIdx, start := 0, 0; start < len(values); childIdx++ {
		end := start + int(childSpan)
		if end > len(values) {
			end = len(values)
		}
		childRoot, err := s.buildFromValues(ctx, values[start:end], height-1, false)
		if err != nil {
			return cid.Undef, err
		}
		content[childIdx] = childRoot
		start = end
	}

	return s.commitSlots(ctx, slots)
}

func (s *Semantic) growRoot(ctx context.Context, root cid.Cid, oldHeight int, oldLen uint64) (cid.Cid, error) {
	return s.rebuildPrefix(ctx, root, true, oldHeight, false, oldHeight, oldLen)
}

func (s *Semantic) rebuildPrefix(
	ctx context.Context,
	root cid.Cid,
	sourceRoot bool,
	sourceHeight int,
	targetRoot bool,
	targetHeight int,
	keepLen uint64,
) (cid.Cid, error) {
	if targetHeight > sourceHeight {
		return cid.Undef, fmt.Errorf("target height %d exceeds source height %d", targetHeight, sourceHeight)
	}
	if keepLen == 0 {
		if targetRoot {
			return s.commitEmptyRoot(ctx)
		}
		return cid.Undef, nil
	}

	slots, err := s.loadNode(ctx, root, sourceRoot)
	if err != nil {
		return cid.Undef, err
	}
	content := listruntime.ContentSlots(slots, sourceRoot)

	if targetHeight < sourceHeight {
		if !content[0].Defined() {
			return cid.Undef, fmt.Errorf("cannot descend into empty leftmost subtree")
		}
		return s.rebuildPrefix(ctx, content[0], false, sourceHeight-1, targetRoot, targetHeight, keepLen)
	}

	var nextSlots []cid.Cid
	if targetRoot {
		nextSlots = listruntime.EmptyRootSlots()
		lengthMarker, err := listruntime.EncodeLengthMarker(keepLen)
		if err != nil {
			return cid.Undef, err
		}
		nextSlots[0] = lengthMarker
	} else {
		nextSlots = listruntime.EmptyNodeSlots()
	}
	nextContent := listruntime.ContentSlots(nextSlots, targetRoot)

	if targetHeight == 0 {
		if keepLen > uint64(len(content)) {
			return cid.Undef, fmt.Errorf("keep length %d exceeds leaf width %d", keepLen, len(content))
		}
		copy(nextContent, content[:int(keepLen)])
		return s.commitSlots(ctx, nextSlots)
	}

	childSpan, err := listruntime.SubtreeCapacity(targetHeight - 1)
	if err != nil {
		return cid.Undef, err
	}
	fullChildren := keepLen / childSpan
	partial := keepLen % childSpan

	for i := uint64(0); i < fullChildren; i++ {
		if !content[i].Defined() {
			return cid.Undef, fmt.Errorf("missing child %d while rebuilding prefix", i)
		}
		nextContent[i] = content[i]
	}
	if partial > 0 {
		if !content[fullChildren].Defined() {
			return cid.Undef, fmt.Errorf("missing boundary child %d while rebuilding prefix", fullChildren)
		}
		nextContent[fullChildren], err = s.rebuildPrefix(
			ctx,
			content[fullChildren],
			false,
			targetHeight-1,
			false,
			targetHeight-1,
			partial,
		)
		if err != nil {
			return cid.Undef, err
		}
	}

	return s.commitSlots(ctx, nextSlots)
}

func (s *Semantic) replaceAt(
	ctx context.Context,
	root cid.Cid,
	isRoot bool,
	height int,
	index uint64,
	oldKey cid.Cid,
	newKey cid.Cid,
) (cid.Cid, error) {
	slots, err := s.loadNode(ctx, root, isRoot)
	if err != nil {
		return cid.Undef, err
	}
	content := listruntime.ContentSlots(slots, isRoot)

	if height == 0 {
		if index >= uint64(len(content)) {
			return cid.Undef, fmt.Errorf("index %d out of leaf range", index)
		}
		if !content[index].Equals(oldKey) {
			return cid.Undef, fmt.Errorf("old key mismatch at index %d", index)
		}
		nextSlots := cloneSlots(slots)
		listruntime.ContentSlots(nextSlots, isRoot)[index] = newKey
		return s.commitSlots(ctx, nextSlots)
	}

	childSpan, err := listruntime.SubtreeCapacity(height - 1)
	if err != nil {
		return cid.Undef, err
	}
	digit := int(index / childSpan)
	localIndex := index % childSpan

	if !content[digit].Defined() {
		return cid.Undef, fmt.Errorf("missing child at digit %d", digit)
	}

	newChild, err := s.replaceAt(ctx, content[digit], false, height-1, localIndex, oldKey, newKey)
	if err != nil {
		return cid.Undef, err
	}

	nextSlots := cloneSlots(slots)
	listruntime.ContentSlots(nextSlots, isRoot)[digit] = newChild
	return s.commitSlots(ctx, nextSlots)
}

func (s *Semantic) appendInto(ctx context.Context, root cid.Cid, height int, index uint64, key cid.Cid) (cid.Cid, error) {
	slots, err := s.loadNode(ctx, root, false)
	if err != nil {
		return cid.Undef, err
	}

	if height == 0 {
		if index >= uint64(len(slots)) {
			return cid.Undef, fmt.Errorf("index %d out of leaf range", index)
		}
		if slots[index].Defined() {
			return cid.Undef, fmt.Errorf("append slot %d is already occupied", index)
		}
		nextSlots := cloneSlots(slots)
		nextSlots[index] = key
		return s.commitSlots(ctx, nextSlots)
	}

	childSpan, err := listruntime.SubtreeCapacity(height - 1)
	if err != nil {
		return cid.Undef, err
	}
	digit := int(index / childSpan)
	localIndex := index % childSpan

	nextSlots := cloneSlots(slots)
	if nextSlots[digit].Defined() {
		nextSlots[digit], err = s.appendInto(ctx, nextSlots[digit], height-1, localIndex, key)
	} else {
		nextSlots[digit], err = s.buildSparseSubtree(ctx, height-1, localIndex, key)
	}
	if err != nil {
		return cid.Undef, err
	}
	return s.commitSlots(ctx, nextSlots)
}

func (s *Semantic) buildSparseSubtree(ctx context.Context, height int, index uint64, key cid.Cid) (cid.Cid, error) {
	if height == 0 {
		slots := listruntime.EmptyNodeSlots()
		if index >= uint64(len(slots)) {
			return cid.Undef, fmt.Errorf("index %d out of leaf range", index)
		}
		slots[index] = key
		return s.commitSlots(ctx, slots)
	}

	childSpan, err := listruntime.SubtreeCapacity(height - 1)
	if err != nil {
		return cid.Undef, err
	}
	digit := int(index / childSpan)
	localIndex := index % childSpan

	slots := listruntime.EmptyNodeSlots()
	slots[digit], err = s.buildSparseSubtree(ctx, height-1, localIndex, key)
	if err != nil {
		return cid.Undef, err
	}
	return s.commitSlots(ctx, slots)
}

func (s *Semantic) commitEmptyRoot(ctx context.Context) (cid.Cid, error) {
	slots := listruntime.EmptyRootSlots()
	lengthMarker, err := listruntime.EncodeLengthMarker(0)
	if err != nil {
		return cid.Undef, err
	}
	slots[0] = lengthMarker
	return s.commitSlots(ctx, slots)
}

func (s *Semantic) loadRoot(ctx context.Context, root cid.Cid) ([]cid.Cid, uint64, error) {
	slots, err := s.loadNode(ctx, root, true)
	if err != nil {
		return nil, 0, err
	}
	length, err := listruntime.DecodeLengthMarker(slots[0])
	if err != nil {
		return nil, 0, err
	}
	return slots, length, nil
}

func (s *Semantic) loadNode(ctx context.Context, root cid.Cid, isRoot bool) ([]cid.Cid, error) {
	width := listruntime.Fanout
	if isRoot {
		width = listruntime.RootWidth
	}
	return listruntime.LoadSlots(ctx, s.eat, s.bucketID, root, width)
}

func (s *Semantic) commitSlots(ctx context.Context, slots []cid.Cid) (cid.Cid, error) {
	root, err := listruntime.CommitSlots(s.scheme, slots)
	if err != nil {
		return cid.Undef, err
	}
	if err := listruntime.StoreSlots(ctx, s.eat, s.bucketID, root, slots); err != nil {
		return cid.Undef, err
	}
	return root, nil
}

func encodeProof(query list.Query, envelope proofEnvelope) (list.Query, structure.Proof, error) {
	proofBytes, err := json.Marshal(envelope)
	if err != nil {
		return list.Query{}, nil, err
	}
	return query, structure.Proof(proofBytes), nil
}

func valuesFromView(view list.View) ([]cid.Cid, error) {
	if view == nil {
		return nil, fmt.Errorf("list view is nil")
	}

	values := make([]cid.Cid, view.Len())
	for i := uint64(0); i < view.Len(); i++ {
		value, ok := view.Get(i)
		if !ok {
			return nil, fmt.Errorf("missing value at index %d", i)
		}
		values[i] = value
	}
	return values, nil
}

func newProofStep(target cid.Cid, proof []byte) proofStep {
	if !target.Defined() {
		return proofStep{Proof: proof}
	}
	return proofStep{
		Target: target.Bytes(),
		Proof:  proof,
	}
}

func parseStepTarget(step proofStep) (cid.Cid, error) {
	if len(step.Target) == 0 {
		return cid.Undef, nil
	}
	return cid.Cast(step.Target)
}

func cloneSlots(slots []cid.Cid) []cid.Cid {
	return append([]cid.Cid(nil), slots...)
}

var _ list.Semantic = (*Semantic)(nil)

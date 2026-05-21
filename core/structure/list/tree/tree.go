// Package tree implements the stable-indexed list semantic using a tree-shaped
// fixed-slot layout. Runtime operations execute against node materialization
// stored in ArcTable, so proofs and updates do not require a caller-supplied view.
package tree

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dewebprotocol/malt/core/arctable"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/commitment"
	"github.com/dewebprotocol/malt/core/structure"
	"github.com/dewebprotocol/malt/core/structure/list"
	"github.com/dewebprotocol/malt/core/structure/list/tree/internal"
	cid "github.com/ipfs/go-cid"
)

type TreeList struct {
	scheme   commitment.IndexCommitment
	arctable arctable.ArcTable
}

type proofEnvelope struct {
	LengthProof  []byte      `json:"length_proof"`
	LengthTarget []byte      `json:"length_target,omitempty"`
	Steps        []proofStep `json:"steps,omitempty"`
}

type proofStep struct {
	Target []byte  `json:"target"`
	Proof  []byte  `json:"proof"`
	Slot   *uint64 `json:"slot,omitempty"`
}

type rangeProofEnvelope struct {
	MetadataProof []byte            `json:"metadata_proof"`
	IndexProofs   []rangeIndexProof `json:"index_proofs,omitempty"`
}

type rangeIndexProof struct {
	Index uint64 `json:"index"`
	Proof []byte `json:"proof"`
}

func NewList(scheme commitment.IndexCommitment, arctable arctable.ArcTable) (*TreeList, error) {
	if err := layout.ValidateCommitment(scheme); err != nil {
		return nil, err
	}
	if arctable == nil {
		return nil, fmt.Errorf("arctable is nil")
	}
	return &TreeList{
		scheme:   scheme,
		arctable: arctable,
	}, nil
}

func (s *TreeList) Commit(ctx context.Context, namespace string, view list.View) (cid.Cid, error) {
	values, err := valuesFromView(view)
	if err != nil {
		return cid.Undef, err
	}
	return s.buildPlainFromValues(ctx, namespace, values, layout.RequiredHeight(uint64(len(values))))
}

// CommitFixed commits fixed-width measured list chunks. The resulting root
// remains compatible with base index proofs while also supporting byte-range
// proofs over authenticated fixed chunk metadata.
func (s *TreeList) CommitFixed(ctx context.Context, namespace string, chunks []cid.Cid, chunkSize, totalSize uint64) (cid.Cid, error) {
	if chunkSize == 0 {
		return cid.Undef, fmt.Errorf("chunk size must be positive")
	}
	if uint64(len(chunks)) != chunkCount(totalSize, chunkSize) {
		return cid.Undef, fmt.Errorf("chunk count %d does not match total size %d and chunk size %d", len(chunks), totalSize, chunkSize)
	}
	for i, chunk := range chunks {
		if !chunk.Defined() {
			return cid.Undef, fmt.Errorf("chunk at index %d is undefined", i)
		}
	}
	values := append([]cid.Cid(nil), chunks...)
	return s.buildMeasuredFromValues(ctx, namespace, values, layout.RequiredHeight(uint64(len(values))), 0, chunkSize, totalSize)
}

func (s *TreeList) Prove(ctx context.Context, namespace string, root cid.Cid, index uint64) (list.Query, structure.Proof, error) {
	rootSlots, length, err := s.loadRoot(ctx, namespace, root)
	if err != nil {
		return list.Query{}, nil, err
	}

	query := list.Query{Length: length}
	lengthTarget, lengthProof, err := layout.ProveSlot(s.scheme, root, rootSlots, 0)
	if err != nil {
		return list.Query{}, nil, err
	}
	envelope := proofEnvelope{LengthProof: lengthProof}
	if target, err := lengthTarget.AsCID(); err != nil {
		return list.Query{}, nil, err
	} else if needsExplicitLengthTarget(target, length) {
		envelope.LengthTarget = lengthTarget.Bytes()
	}

	if index >= length {
		query.Key = cid.Undef
		return encodeProof(query, envelope)
	}

	height := layout.RequiredHeight(length)
	digits, err := layout.IndexDigits(index, height)
	if err != nil {
		return list.Query{}, nil, err
	}

	currentRoot := root
	currentSlots := rootSlots
	for level, digit := range digits {
		slot, err := layout.ContentSlotIndex(currentSlots, level == 0, digit)
		if err != nil {
			return list.Query{}, nil, err
		}

		target, proof, err := layout.ProveSlot(s.scheme, currentRoot, currentSlots, slot)
		if err != nil {
			return list.Query{}, nil, err
		}
		envelope.Steps = append(envelope.Steps, newProofStep(target, proof, slot))

		if level == len(digits)-1 {
			query.Key, err = target.AsCID()
			if err != nil {
				return list.Query{}, nil, err
			}
			return encodeProof(query, envelope)
		}
		if !target.Defined() {
			return list.Query{}, nil, fmt.Errorf("missing child at level %d digit %d", level, digit)
		}

		currentRoot, err = target.AsCID()
		if err != nil {
			return list.Query{}, nil, err
		}
		currentSlots, err = s.loadNode(ctx, namespace, currentRoot, false)
		if err != nil {
			return list.Query{}, nil, err
		}
	}

	return list.Query{}, nil, fmt.Errorf("unreachable proof state")
}

func (s *TreeList) Verify(root cid.Cid, index uint64, expected list.Query, proof structure.Proof) (bool, error) {
	var envelope proofEnvelope
	if err := json.Unmarshal(proof, &envelope); err != nil {
		return false, err
	}
	if len(envelope.LengthProof) == 0 {
		return false, fmt.Errorf("missing length proof")
	}

	lengthTarget, err := lengthTargetForVerify(expected.Length, envelope.LengthTarget)
	if err != nil {
		return false, err
	}
	ok, err := layout.VerifySlot(s.scheme, root, 0, lengthTarget, envelope.LengthProof)
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

	height := layout.RequiredHeight(expected.Length)
	if len(envelope.Steps) != height+1 {
		return false, nil
	}

	digits, err := layout.IndexDigits(index, height)
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

		slot, err := contentSlotForVerify(step, level == 0, digit)
		if err != nil {
			return false, err
		}
		ok, err := layout.VerifySlot(s.scheme, currentRoot, slot, target, step.Proof)
		if err != nil || !ok {
			return ok, err
		}

		if level == len(digits)-1 {
			return target.Equal(commitment.CellFromCID(expected.Key)), nil
		}
		currentRoot, err = target.AsCID()
		if err != nil {
			return false, err
		}
	}

	return false, nil
}

func (s *TreeList) ProveRange(ctx context.Context, namespace string, root cid.Cid, start uint64, end *uint64) (list.RangeResult, structure.Proof, error) {
	rootSlots, _, err := s.loadRoot(ctx, namespace, root)
	if err != nil {
		return list.RangeResult{}, nil, err
	}
	meta, err := fixedRangeMetadata(rootSlots[0])
	if err != nil {
		return list.RangeResult{}, nil, err
	}
	if err := validateFixedMetadata(meta); err != nil {
		return list.RangeResult{}, nil, err
	}

	_, metadataProof, err := layout.ProveSlot(s.scheme, root, rootSlots, 0)
	if err != nil {
		return list.RangeResult{}, nil, err
	}
	result := list.RangeResult{
		Metadata: list.RangeMetadata{
			ChildCount: meta.ChildCount,
			TotalSize:  meta.TotalSize,
			ChunkSize:  meta.ChunkSize,
		},
	}
	envelope := rangeProofEnvelope{MetadataProof: metadataProof}

	endExclusive, empty, err := normalizeRange(start, end, meta.TotalSize)
	if err != nil {
		return list.RangeResult{}, nil, err
	}
	if empty {
		return encodeRangeProof(result, envelope)
	}

	first := start / meta.ChunkSize
	last := (endExclusive - 1) / meta.ChunkSize
	result.Segments = make([]cid.Cid, 0, last-first+1)
	envelope.IndexProofs = make([]rangeIndexProof, 0, last-first+1)
	for index := first; index <= last; index++ {
		query, proof, err := s.Prove(ctx, namespace, root, index)
		if err != nil {
			return list.RangeResult{}, nil, err
		}
		if !query.Key.Defined() {
			return list.RangeResult{}, nil, fmt.Errorf("missing segment at index %d", index)
		}
		result.Segments = append(result.Segments, query.Key)
		envelope.IndexProofs = append(envelope.IndexProofs, rangeIndexProof{
			Index: index,
			Proof: cloneBytes(proof),
		})
	}
	return encodeRangeProof(result, envelope)
}

func (s *TreeList) VerifyRange(root cid.Cid, start uint64, end *uint64, expected list.RangeResult, proof structure.Proof) (bool, error) {
	var envelope rangeProofEnvelope
	if err := json.Unmarshal(proof, &envelope); err != nil {
		return false, err
	}
	if len(envelope.MetadataProof) == 0 {
		return false, fmt.Errorf("missing metadata proof")
	}
	meta := layout.FixedMetadata{
		ChildCount: expected.Metadata.ChildCount,
		TotalSize:  expected.Metadata.TotalSize,
		ChunkSize:  expected.Metadata.ChunkSize,
	}
	if err := validateFixedMetadata(meta); err != nil {
		return false, err
	}
	ok, err := s.verifyFixedMetadataSlot(root, meta, envelope.MetadataProof)
	if err != nil || !ok {
		return ok, err
	}

	endExclusive, empty, err := normalizeRange(start, end, meta.TotalSize)
	if err != nil {
		return false, err
	}
	if empty {
		return len(expected.Segments) == 0 && len(envelope.IndexProofs) == 0, nil
	}

	first := start / meta.ChunkSize
	last := (endExclusive - 1) / meta.ChunkSize
	wantCount := int(last - first + 1)
	if len(expected.Segments) != wantCount || len(envelope.IndexProofs) != wantCount {
		return false, nil
	}
	for i := 0; i < wantCount; i++ {
		index := first + uint64(i)
		indexProof := envelope.IndexProofs[i]
		if indexProof.Index != index {
			return false, nil
		}
		ok, err := s.Verify(root, index, list.Query{
			Key:    expected.Segments[i],
			Length: meta.ChildCount,
		}, structure.Proof(indexProof.Proof))
		if err != nil || !ok {
			return ok, err
		}
	}
	return true, nil
}

func (s *TreeList) Replace(ctx context.Context, namespace string, root cid.Cid, index uint64, oldKey, newKey cid.Cid) (cid.Cid, error) {
	if !oldKey.Defined() {
		return cid.Undef, fmt.Errorf("old key is undefined")
	}
	if !newKey.Defined() {
		return cid.Undef, fmt.Errorf("new key is undefined")
	}
	_, length, err := s.loadRoot(ctx, namespace, root)
	if err != nil {
		return cid.Undef, err
	}
	if index >= length {
		return cid.Undef, fmt.Errorf("index %d out of range", index)
	}
	return s.replaceAt(ctx, namespace, root, true, layout.RequiredHeight(length), index, oldKey, newKey)
}

func (s *TreeList) Append(ctx context.Context, namespace string, root cid.Cid, key cid.Cid) (cid.Cid, uint64, error) {
	if !key.Defined() {
		return cid.Undef, 0, fmt.Errorf("key is undefined")
	}
	rootSlots, length, err := s.loadRoot(ctx, namespace, root)
	if err != nil {
		return cid.Undef, 0, err
	}
	if _, err := fixedRangeMetadata(rootSlots[0]); err == nil {
		return cid.Undef, 0, fmt.Errorf("append is not supported for fixed measured list roots")
	}

	newIndex := length
	newLength := length + 1
	oldHeight := layout.RequiredHeight(length)
	newHeight := layout.RequiredHeight(newLength)

	if newHeight > oldHeight {
		grownRoot, err := s.growRoot(ctx, namespace, root, oldHeight, length)
		if err != nil {
			return cid.Undef, 0, err
		}

		nextRootSlots := layout.EmptyRootSlots()
		rootMarker, err := plainNodeMetadata(newHeight, newLength)
		if err != nil {
			return cid.Undef, 0, err
		}
		nextRootSlots[0] = rootMarker
		content := layout.ContentSlots(nextRootSlots, true)
		content[0] = grownRoot

		childSpan, err := layout.SubtreeCapacity(newHeight - 1)
		if err != nil {
			return cid.Undef, 0, err
		}
		rootDigit := int(newIndex / childSpan)
		localIndex := newIndex % childSpan
		childRoot, err := s.buildSparseSubtree(ctx, namespace, newHeight-1, localIndex, key)
		if err != nil {
			return cid.Undef, 0, err
		}
		content[rootDigit] = childRoot

		newRoot, err := s.commitSlots(ctx, namespace, nextRootSlots)
		return newRoot, newIndex, err
	}

	nextRootSlots := cloneSlots(rootSlots)
	rootMarker, err := plainNodeMetadata(newHeight, newLength)
	if err != nil {
		return cid.Undef, 0, err
	}
	nextRootSlots[0] = rootMarker
	content := layout.ContentSlots(nextRootSlots, true)

	if oldHeight == 0 {
		if content[newIndex].Defined() {
			return cid.Undef, 0, fmt.Errorf("append slot %d is already occupied", newIndex)
		}
		content[newIndex] = key
		newRoot, err := s.commitSlots(ctx, namespace, nextRootSlots)
		return newRoot, newIndex, err
	}

	childSpan, err := layout.SubtreeCapacity(oldHeight - 1)
	if err != nil {
		return cid.Undef, 0, err
	}
	digit := int(newIndex / childSpan)
	localIndex := newIndex % childSpan

	if content[digit].Defined() {
		content[digit], err = s.appendInto(ctx, namespace, content[digit], oldHeight-1, localIndex, key)
	} else {
		content[digit], err = s.buildSparseSubtree(ctx, namespace, oldHeight-1, localIndex, key)
	}
	if err != nil {
		return cid.Undef, 0, err
	}

	newRoot, err := s.commitSlots(ctx, namespace, nextRootSlots)
	return newRoot, newIndex, err
}

func (s *TreeList) Truncate(ctx context.Context, namespace string, root cid.Cid, newLen uint64) (cid.Cid, error) {
	rootSlots, oldLen, err := s.loadRoot(ctx, namespace, root)
	if err != nil {
		return cid.Undef, err
	}
	if _, err := fixedRangeMetadata(rootSlots[0]); err == nil && newLen != oldLen {
		return cid.Undef, fmt.Errorf("truncate is not supported for fixed measured list roots")
	}
	if newLen > oldLen {
		return cid.Undef, fmt.Errorf("new length %d exceeds current length %d", newLen, oldLen)
	}
	if newLen == oldLen {
		return root, nil
	}
	if newLen == 0 {
		return s.commitEmptyRoot(ctx, namespace)
	}

	oldHeight := layout.RequiredHeight(oldLen)
	newHeight := layout.RequiredHeight(newLen)
	return s.rebuildPrefix(ctx, namespace, root, true, oldHeight, true, newHeight, newLen)
}

func (s *TreeList) buildPlainFromValues(ctx context.Context, namespace string, values []cid.Cid, height int) (cid.Cid, error) {
	if height < 0 {
		return cid.Undef, fmt.Errorf("height must be non-negative")
	}
	marker, err := layout.EncodeNodeMetadata(layout.NodeMetadata{
		Height:     uint64(height),
		ChildCount: uint64(len(values)),
	})
	if err != nil {
		return cid.Undef, err
	}
	slots := layout.EmptyRootSlots()
	slots[0] = marker
	content := layout.ContentSlots(slots, true)
	if height == 0 {
		copy(content, values)
		return s.commitSlots(ctx, namespace, slots)
	}

	childSpan, err := layout.SubtreeCapacity(height - 1)
	if err != nil {
		return cid.Undef, err
	}
	for childIdx, start := 0, 0; start < len(values); childIdx++ {
		end := start + int(childSpan)
		if end > len(values) {
			end = len(values)
		}
		childRoot, err := s.buildPlainFromValues(ctx, namespace, values[start:end], height-1)
		if err != nil {
			return cid.Undef, err
		}
		content[childIdx] = childRoot
		start = end
	}

	return s.commitSlots(ctx, namespace, slots)
}

func (s *TreeList) buildMeasuredFromValues(ctx context.Context, namespace string, values []cid.Cid, height int, startIndex, chunkSize, totalSize uint64) (cid.Cid, error) {
	if height < 0 {
		return cid.Undef, fmt.Errorf("height must be non-negative")
	}
	nodeSize, err := measuredNodeSize(startIndex, uint64(len(values)), chunkSize, totalSize)
	if err != nil {
		return cid.Undef, err
	}
	marker, err := layout.EncodeNodeMetadata(layout.NodeMetadata{
		Height:     uint64(height),
		ChildCount: uint64(len(values)),
		TotalSize:  nodeSize,
		ChunkSize:  chunkSize,
	})
	if err != nil {
		return cid.Undef, err
	}
	slots := layout.EmptyRootSlots()
	slots[0] = marker
	content := layout.ContentSlots(slots, true)
	if height == 0 {
		copy(content, values)
		return s.commitSlots(ctx, namespace, slots)
	}

	childSpan, err := layout.SubtreeCapacity(height - 1)
	if err != nil {
		return cid.Undef, err
	}
	for childIdx, start := 0, 0; start < len(values); childIdx++ {
		end := start + int(childSpan)
		if end > len(values) {
			end = len(values)
		}
		childRoot, err := s.buildMeasuredFromValues(ctx, namespace, values[start:end], height-1, startIndex+uint64(start), chunkSize, totalSize)
		if err != nil {
			return cid.Undef, err
		}
		content[childIdx] = childRoot
		start = end
	}

	return s.commitSlots(ctx, namespace, slots)
}

func (s *TreeList) growRoot(ctx context.Context, namespace string, root cid.Cid, oldHeight int, oldLen uint64) (cid.Cid, error) {
	return s.rebuildPrefix(ctx, namespace, root, true, oldHeight, false, oldHeight, oldLen)
}

func (s *TreeList) rebuildPrefix(
	ctx context.Context,
	namespace string,
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
			return s.commitEmptyRoot(ctx, namespace)
		}
		return cid.Undef, nil
	}

	slots, err := s.loadNode(ctx, namespace, root, sourceRoot)
	if err != nil {
		return cid.Undef, err
	}
	content := layout.ContentSlots(slots, sourceRoot)

	if targetHeight < sourceHeight {
		if !content[0].Defined() {
			return cid.Undef, fmt.Errorf("cannot descend into empty leftmost subtree")
		}
		return s.rebuildPrefix(ctx, namespace, content[0], false, sourceHeight-1, targetRoot, targetHeight, keepLen)
	}

	var nextSlots []cid.Cid
	if targetRoot {
		nextSlots = layout.EmptyRootSlots()
	} else {
		nextSlots = layout.EmptyNodeSlots()
	}
	marker, err := plainNodeMetadata(targetHeight, keepLen)
	if err != nil {
		return cid.Undef, err
	}
	nextSlots[0] = marker
	nextContent := layout.ContentSlots(nextSlots, targetRoot)

	if targetHeight == 0 {
		if keepLen > uint64(len(content)) {
			return cid.Undef, fmt.Errorf("keep length %d exceeds leaf width %d", keepLen, len(content))
		}
		copy(nextContent, content[:int(keepLen)])
		return s.commitSlots(ctx, namespace, nextSlots)
	}

	childSpan, err := layout.SubtreeCapacity(targetHeight - 1)
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
			namespace,
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

	return s.commitSlots(ctx, namespace, nextSlots)
}

func (s *TreeList) replaceAt(
	ctx context.Context,
	namespace string,
	root cid.Cid,
	isRoot bool,
	height int,
	index uint64,
	oldKey cid.Cid,
	newKey cid.Cid,
) (cid.Cid, error) {
	slots, err := s.loadNode(ctx, namespace, root, isRoot)
	if err != nil {
		return cid.Undef, err
	}
	content := layout.ContentSlots(slots, isRoot)

	if height == 0 {
		if index >= uint64(len(content)) {
			return cid.Undef, fmt.Errorf("index %d out of leaf range", index)
		}
		if !content[index].Equals(oldKey) {
			return cid.Undef, fmt.Errorf("old key mismatch at index %d", index)
		}
		nextSlots := cloneSlots(slots)
		layout.ContentSlots(nextSlots, isRoot)[index] = newKey
		return s.commitSlots(ctx, namespace, nextSlots)
	}

	childSpan, err := layout.SubtreeCapacity(height - 1)
	if err != nil {
		return cid.Undef, err
	}
	digit := int(index / childSpan)
	localIndex := index % childSpan

	if !content[digit].Defined() {
		return cid.Undef, fmt.Errorf("missing child at digit %d", digit)
	}

	newChild, err := s.replaceAt(ctx, namespace, content[digit], false, height-1, localIndex, oldKey, newKey)
	if err != nil {
		return cid.Undef, err
	}

	nextSlots := cloneSlots(slots)
	layout.ContentSlots(nextSlots, isRoot)[digit] = newChild
	return s.commitSlots(ctx, namespace, nextSlots)
}

func (s *TreeList) appendInto(ctx context.Context, namespace string, root cid.Cid, height int, index uint64, key cid.Cid) (cid.Cid, error) {
	slots, err := s.loadNode(ctx, namespace, root, false)
	if err != nil {
		return cid.Undef, err
	}

	if height == 0 {
		nextSlots := cloneSlots(slots)
		content := layout.ContentSlots(nextSlots, false)
		if index >= uint64(len(content)) {
			return cid.Undef, fmt.Errorf("index %d out of leaf range", index)
		}
		if content[index].Defined() {
			return cid.Undef, fmt.Errorf("append slot %d is already occupied", index)
		}
		marker, err := plainNodeMetadata(height, index+1)
		if err != nil {
			return cid.Undef, err
		}
		nextSlots[0] = marker
		content[index] = key
		return s.commitSlots(ctx, namespace, nextSlots)
	}

	childSpan, err := layout.SubtreeCapacity(height - 1)
	if err != nil {
		return cid.Undef, err
	}
	digit := int(index / childSpan)
	localIndex := index % childSpan

	nextSlots := cloneSlots(slots)
	marker, err := plainNodeMetadata(height, index+1)
	if err != nil {
		return cid.Undef, err
	}
	nextSlots[0] = marker
	nextContent := layout.ContentSlots(nextSlots, false)
	if nextContent[digit].Defined() {
		nextContent[digit], err = s.appendInto(ctx, namespace, nextContent[digit], height-1, localIndex, key)
	} else {
		nextContent[digit], err = s.buildSparseSubtree(ctx, namespace, height-1, localIndex, key)
	}
	if err != nil {
		return cid.Undef, err
	}
	return s.commitSlots(ctx, namespace, nextSlots)
}

func (s *TreeList) buildSparseSubtree(ctx context.Context, namespace string, height int, index uint64, key cid.Cid) (cid.Cid, error) {
	if height == 0 {
		slots := layout.EmptyNodeSlots()
		marker, err := plainNodeMetadata(height, index+1)
		if err != nil {
			return cid.Undef, err
		}
		slots[0] = marker
		content := layout.ContentSlots(slots, false)
		if index >= uint64(len(content)) {
			return cid.Undef, fmt.Errorf("index %d out of leaf range", index)
		}
		content[index] = key
		return s.commitSlots(ctx, namespace, slots)
	}

	childSpan, err := layout.SubtreeCapacity(height - 1)
	if err != nil {
		return cid.Undef, err
	}
	digit := int(index / childSpan)
	localIndex := index % childSpan

	slots := layout.EmptyNodeSlots()
	marker, err := plainNodeMetadata(height, index+1)
	if err != nil {
		return cid.Undef, err
	}
	slots[0] = marker
	content := layout.ContentSlots(slots, false)
	content[digit], err = s.buildSparseSubtree(ctx, namespace, height-1, localIndex, key)
	if err != nil {
		return cid.Undef, err
	}
	return s.commitSlots(ctx, namespace, slots)
}

func (s *TreeList) commitEmptyRoot(ctx context.Context, namespace string) (cid.Cid, error) {
	slots := layout.EmptyRootSlots()
	lengthMarker, err := plainNodeMetadata(0, 0)
	if err != nil {
		return cid.Undef, err
	}
	slots[0] = lengthMarker
	return s.commitSlots(ctx, namespace, slots)
}

func (s *TreeList) loadRoot(ctx context.Context, namespace string, root cid.Cid) ([]cid.Cid, uint64, error) {
	slots, err := s.loadNode(ctx, namespace, root, true)
	if err != nil {
		return nil, 0, err
	}
	length, err := layout.DecodeRootLength(slots[0])
	if err != nil {
		return nil, 0, err
	}
	return slots, length, nil
}

func (s *TreeList) loadNode(ctx context.Context, namespace string, root cid.Cid, isRoot bool) ([]cid.Cid, error) {
	width := layout.NodeWidth
	if isRoot {
		width = layout.RootWidth
	}
	slots, err := layout.LoadSlots(ctx, s.arctable, namespace, root, width)
	if err != nil {
		return nil, err
	}
	validateErr := layout.ValidateSlots(s.scheme, root, slots)
	if validateErr == nil {
		return slots, nil
	}
	if !isRoot && width != layout.LegacyNodeWidth {
		legacySlots, err := layout.LoadSlots(ctx, s.arctable, namespace, root, layout.LegacyNodeWidth)
		if err != nil {
			return nil, validateErr
		}
		if err := layout.ValidateSlots(s.scheme, root, legacySlots); err == nil {
			return legacySlots, nil
		}
		return nil, validateErr
	}
	return nil, validateErr
}

func (s *TreeList) commitSlots(ctx context.Context, namespace string, slots []cid.Cid) (cid.Cid, error) {
	root, err := layout.CommitSlots(s.scheme, slots)
	if err != nil {
		return cid.Undef, err
	}

	// Rewrap root into the list-typed codec for the same backend.
	commBytes, err := codec.ExtractCommitment(root)
	if err != nil {
		return cid.Undef, err
	}
	listRoot, err := codec.NewTypedCID(codec.SemanticKindList, codec.BackendKindOf(root), commBytes)
	if err != nil {
		return cid.Undef, err
	}

	if err := layout.StoreSlots(ctx, s.arctable, namespace, listRoot, slots); err != nil {
		return cid.Undef, err
	}
	return listRoot, nil
}

func (s *TreeList) verifyFixedMetadataSlot(root cid.Cid, meta layout.FixedMetadata, proof []byte) (bool, error) {
	nodeMarker, err := layout.EncodeNodeMetadata(layout.NodeMetadata{
		Height:     uint64(layout.RequiredHeight(meta.ChildCount)),
		ChildCount: meta.ChildCount,
		TotalSize:  meta.TotalSize,
		ChunkSize:  meta.ChunkSize,
	})
	if err != nil {
		return false, err
	}
	ok, err := layout.VerifySlot(s.scheme, root, 0, commitment.CellFromCID(nodeMarker), proof)
	if err != nil || ok {
		return ok, err
	}

	legacyMarker, err := layout.EncodeFixedMetadata(meta)
	if err != nil {
		return false, err
	}
	return layout.VerifySlot(s.scheme, root, 0, commitment.CellFromCID(legacyMarker), proof)
}

func encodeProof(query list.Query, envelope proofEnvelope) (list.Query, structure.Proof, error) {
	proofBytes, err := json.Marshal(envelope)
	if err != nil {
		return list.Query{}, nil, err
	}
	return query, structure.Proof(proofBytes), nil
}

func encodeRangeProof(result list.RangeResult, envelope rangeProofEnvelope) (list.RangeResult, structure.Proof, error) {
	proofBytes, err := json.Marshal(envelope)
	if err != nil {
		return list.RangeResult{}, nil, err
	}
	return result, structure.Proof(proofBytes), nil
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
		if !value.Defined() {
			return nil, fmt.Errorf("value at index %d is undefined", i)
		}
		values[i] = value
	}
	return values, nil
}

func newProofStep(target commitment.Cell, proof []byte, slot uint64) proofStep {
	provedSlot := slot
	if !target.Defined() {
		return proofStep{Proof: proof, Slot: &provedSlot}
	}
	return proofStep{
		Target: target.Bytes(),
		Proof:  proof,
		Slot:   &provedSlot,
	}
}

func parseStepTarget(step proofStep) (commitment.Cell, error) {
	if len(step.Target) == 0 {
		return nil, nil
	}
	return commitment.NewCell(step.Target), nil
}

func contentSlotForVerify(step proofStep, isRoot bool, digit int) (uint64, error) {
	v2Slot := uint64(digit) + 1
	if step.Slot == nil {
		return v2Slot, nil
	}
	slot := *step.Slot
	if isRoot {
		if slot != v2Slot {
			return 0, fmt.Errorf("root content digit %d proved slot %d, want %d", digit, slot, v2Slot)
		}
		return slot, nil
	}
	if slot != uint64(digit) && slot != v2Slot {
		return 0, fmt.Errorf("content digit %d proved unsupported slot %d", digit, slot)
	}
	return slot, nil
}

func needsExplicitLengthTarget(marker cid.Cid, length uint64) bool {
	legacy, err := layout.EncodeLengthMarker(length)
	if err != nil {
		return true
	}
	return !marker.Equals(legacy)
}

func lengthTargetForVerify(expectedLength uint64, explicit []byte) (commitment.Cell, error) {
	if len(explicit) == 0 {
		lengthMarker, err := layout.EncodeLengthMarker(expectedLength)
		if err != nil {
			return nil, err
		}
		return commitment.CellFromCID(lengthMarker), nil
	}
	cell := commitment.NewCell(explicit)
	marker, err := cell.AsCID()
	if err != nil {
		return nil, err
	}
	length, err := layout.DecodeRootLength(marker)
	if err != nil {
		return nil, err
	}
	if length != expectedLength {
		return nil, fmt.Errorf("length target commits child count %d, expected %d", length, expectedLength)
	}
	return cell, nil
}

func fixedRangeMetadata(marker cid.Cid) (layout.FixedMetadata, error) {
	meta, err := layout.DecodeFixedMetadata(marker)
	if err != nil {
		return layout.FixedMetadata{}, fmt.Errorf("root does not carry fixed range metadata: %w", err)
	}
	return meta, nil
}

func validateFixedMetadata(meta layout.FixedMetadata) error {
	if meta.ChunkSize == 0 {
		return fmt.Errorf("chunk size is zero")
	}
	if meta.ChildCount != chunkCount(meta.TotalSize, meta.ChunkSize) {
		return fmt.Errorf("child count %d does not match total size %d and chunk size %d", meta.ChildCount, meta.TotalSize, meta.ChunkSize)
	}
	return nil
}

func plainNodeMetadata(height int, childCount uint64) (cid.Cid, error) {
	if height < 0 {
		return cid.Undef, fmt.Errorf("height must be non-negative")
	}
	return layout.EncodeNodeMetadata(layout.NodeMetadata{
		Height:     uint64(height),
		ChildCount: childCount,
	})
}

func measuredNodeSize(startIndex, childCount, chunkSize, totalSize uint64) (uint64, error) {
	if chunkSize == 0 {
		return 0, fmt.Errorf("chunk size is zero")
	}
	if childCount == 0 {
		return 0, nil
	}
	if startIndex > ^uint64(0)/chunkSize {
		return 0, fmt.Errorf("measured node start index %d overflows chunk size %d", startIndex, chunkSize)
	}
	endIndex := startIndex + childCount
	if endIndex < startIndex {
		return 0, fmt.Errorf("measured node child count overflows")
	}
	if endIndex > ^uint64(0)/chunkSize {
		return 0, fmt.Errorf("measured node end index %d overflows chunk size %d", endIndex, chunkSize)
	}
	startByte := startIndex * chunkSize
	if startByte >= totalSize {
		return 0, nil
	}
	endByte := endIndex * chunkSize
	if endByte > totalSize {
		endByte = totalSize
	}
	return endByte - startByte, nil
}

func normalizeRange(start uint64, end *uint64, totalSize uint64) (endExclusive uint64, empty bool, err error) {
	endExclusive = totalSize
	if end != nil {
		endExclusive = *end
		if endExclusive > totalSize {
			endExclusive = totalSize
		}
	}
	if start > endExclusive {
		return 0, false, fmt.Errorf("range start %d exceeds end %d", start, endExclusive)
	}
	if start >= totalSize || endExclusive == start {
		return endExclusive, true, nil
	}
	return endExclusive, false, nil
}

func chunkCount(totalSize, chunkSize uint64) uint64 {
	if totalSize == 0 {
		return 0
	}
	return ((totalSize - 1) / chunkSize) + 1
}

func cloneBytes(data []byte) []byte {
	return append([]byte(nil), data...)
}

func cloneSlots(slots []cid.Cid) []cid.Cid {
	return append([]cid.Cid(nil), slots...)
}

var _ list.Semantics = (*TreeList)(nil)
var _ list.MeasuredSemantics = (*TreeList)(nil)

// Package tree implements the stable-indexed list semantic using a tree-shaped
// indexed layout over a fixed-slot primitive backend.
package tree

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/dewebprotocol/malt/core/sce/commitment"
	"github.com/dewebprotocol/malt/core/structure"
	"github.com/dewebprotocol/malt/core/structure/list"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

const (
	// Fanout is the fixed branching factor for the v1 tree-shaped list layout.
	// Root slot 0 is reserved for the authenticated length marker, so the root
	// stores at most Fanout children or direct keys.
	Fanout = 255

	maxUint64 = ^uint64(0)
)

type Semantic struct {
	backend commitment.ListBackend
}

type proofEnvelope struct {
	LengthProof []byte      `json:"length_proof"`
	Steps       []proofStep `json:"steps,omitempty"`
}

type proofStep struct {
	Target []byte `json:"target"`
	Proof  []byte `json:"proof"`
}

type node struct {
	height   int
	root     cid.Cid
	slots    []cid.Cid
	children []*node
}

type materializedTree struct {
	length   uint64
	height   int
	root     cid.Cid
	rootNode *node
}

func New(backend commitment.ListBackend) (*Semantic, error) {
	if backend == nil {
		return nil, fmt.Errorf("list backend is nil")
	}
	if backend.MaxValues() < Fanout+1 {
		return nil, fmt.Errorf("list backend capacity %d is smaller than required root width %d", backend.MaxValues(), Fanout+1)
	}
	return &Semantic{backend: backend}, nil
}

func (s *Semantic) Commit(ctx context.Context, view list.View) (cid.Cid, error) {
	_ = ctx
	values, err := valuesFromView(view)
	if err != nil {
		return cid.Undef, err
	}
	tree, err := s.materialize(values)
	if err != nil {
		return cid.Undef, err
	}
	return tree.root, nil
}

func (s *Semantic) Prove(ctx context.Context, root cid.Cid, view list.View, index uint64) (list.Query, structure.Proof, error) {
	_ = ctx
	values, err := valuesFromView(view)
	if err != nil {
		return list.Query{}, nil, err
	}
	tree, err := s.materialize(values)
	if err != nil {
		return list.Query{}, nil, err
	}
	if !tree.root.Equals(root) {
		return list.Query{}, nil, fmt.Errorf("root/view mismatch")
	}

	query := list.Query{Length: tree.length}

	lengthMarker, err := lengthCID(tree.length)
	if err != nil {
		return list.Query{}, nil, err
	}
	_, lengthProof, err := s.backend.ProveIndex(tree.root, tree.rootNode.slots, 0)
	if err != nil {
		return list.Query{}, nil, err
	}
	if !lengthMarker.Defined() {
		return list.Query{}, nil, fmt.Errorf("length marker is undefined")
	}

	envelope := proofEnvelope{LengthProof: lengthProof}
	if index >= tree.length {
		query.Key = cid.Undef
		return encodeProof(query, envelope)
	}

	digits, err := indexDigits(index, tree.height)
	if err != nil {
		return list.Query{}, nil, err
	}
	current := tree.rootNode

	for level, digit := range digits {
		slot := uint64(digit)
		if level == 0 {
			slot++
		}

		target, proof, err := s.backend.ProveIndex(current.root, current.slots, slot)
		if err != nil {
			return list.Query{}, nil, err
		}
		envelope.Steps = append(envelope.Steps, newProofStep(target, proof))

		if level == len(digits)-1 {
			query.Key = target
			break
		}
		if digit >= len(current.children) || current.children[digit] == nil {
			return list.Query{}, nil, fmt.Errorf("missing child at level %d digit %d", level, digit)
		}
		current = current.children[digit]
	}

	return encodeProof(query, envelope)
}

func (s *Semantic) Verify(root cid.Cid, index uint64, expected list.Query, proof structure.Proof) (bool, error) {
	var envelope proofEnvelope
	if err := json.Unmarshal(proof, &envelope); err != nil {
		return false, err
	}
	if len(envelope.LengthProof) == 0 {
		return false, fmt.Errorf("missing length proof")
	}

	lengthMarker, err := lengthCID(expected.Length)
	if err != nil {
		return false, err
	}
	ok, err := s.backend.VerifyIndex(root, 0, lengthMarker, envelope.LengthProof)
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

	height := requiredHeight(expected.Length)
	if len(envelope.Steps) != height+1 {
		return false, nil
	}

	digits, err := indexDigits(index, height)
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
		ok, err := s.backend.VerifyIndex(currentRoot, slot, target, step.Proof)
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

func (s *Semantic) Replace(ctx context.Context, root cid.Cid, view list.View, index uint64, oldKey, newKey cid.Cid) (cid.Cid, error) {
	_ = ctx
	values, err := valuesFromView(view)
	if err != nil {
		return cid.Undef, err
	}
	tree, err := s.materialize(values)
	if err != nil {
		return cid.Undef, err
	}
	if !tree.root.Equals(root) {
		return cid.Undef, fmt.Errorf("root/view mismatch")
	}
	if index >= tree.length {
		return cid.Undef, fmt.Errorf("index %d out of range", index)
	}
	if !values[index].Equals(oldKey) {
		return cid.Undef, fmt.Errorf("old key mismatch at index %d", index)
	}

	nextValues := append([]cid.Cid(nil), values...)
	nextValues[index] = newKey
	return s.Commit(ctx, list.NewViewFromSlice(nextValues))
}

func (s *Semantic) Append(ctx context.Context, root cid.Cid, view list.View, key cid.Cid) (cid.Cid, uint64, error) {
	_ = ctx
	values, err := valuesFromView(view)
	if err != nil {
		return cid.Undef, 0, err
	}
	tree, err := s.materialize(values)
	if err != nil {
		return cid.Undef, 0, err
	}
	if !tree.root.Equals(root) {
		return cid.Undef, 0, fmt.Errorf("root/view mismatch")
	}

	newIndex := uint64(len(values))
	nextValues := append(append([]cid.Cid(nil), values...), key)
	newRoot, err := s.Commit(ctx, list.NewViewFromSlice(nextValues))
	return newRoot, newIndex, err
}

func (s *Semantic) Truncate(ctx context.Context, root cid.Cid, view list.View, newLen uint64) (cid.Cid, error) {
	_ = ctx
	values, err := valuesFromView(view)
	if err != nil {
		return cid.Undef, err
	}
	tree, err := s.materialize(values)
	if err != nil {
		return cid.Undef, err
	}
	if !tree.root.Equals(root) {
		return cid.Undef, fmt.Errorf("root/view mismatch")
	}
	if newLen > uint64(len(values)) {
		return cid.Undef, fmt.Errorf("new length %d exceeds current length %d", newLen, len(values))
	}
	if newLen == uint64(len(values)) {
		return root, nil
	}

	nextValues := append([]cid.Cid(nil), values[:newLen]...)
	return s.Commit(ctx, list.NewViewFromSlice(nextValues))
}

func (s *Semantic) materialize(values []cid.Cid) (*materializedTree, error) {
	length := uint64(len(values))
	height := requiredHeight(length)
	rootNode, err := s.buildRoot(values, height)
	if err != nil {
		return nil, err
	}
	return &materializedTree{
		length:   length,
		height:   height,
		root:     rootNode.root,
		rootNode: rootNode,
	}, nil
}

func (s *Semantic) buildRoot(values []cid.Cid, height int) (*node, error) {
	lengthMarker, err := lengthCID(uint64(len(values)))
	if err != nil {
		return nil, err
	}

	if height == 0 {
		slots := make([]cid.Cid, 1+len(values))
		slots[0] = lengthMarker
		copy(slots[1:], values)
		root, err := s.backend.CommitValues(slots)
		if err != nil {
			return nil, err
		}
		return &node{
			height: 0,
			root:   root,
			slots:  slots,
		}, nil
	}

	childChunk, err := nodeCapacity(height - 1)
	if err != nil {
		return nil, err
	}
	childCount := ceilDiv(uint64(len(values)), childChunk)
	children := make([]*node, childCount)
	slots := make([]cid.Cid, 1+childCount)
	slots[0] = lengthMarker

	for i := 0; i < childCount; i++ {
		start := uint64(i) * childChunk
		end := minUint64(start+childChunk, uint64(len(values)))
		child, err := s.buildNode(values[int(start):int(end)], height-1)
		if err != nil {
			return nil, err
		}
		children[i] = child
		slots[1+i] = child.root
	}

	root, err := s.backend.CommitValues(slots)
	if err != nil {
		return nil, err
	}
	return &node{
		height:   height,
		root:     root,
		slots:    slots,
		children: children,
	}, nil
}

func (s *Semantic) buildNode(values []cid.Cid, height int) (*node, error) {
	if height == 0 {
		slots := append([]cid.Cid(nil), values...)
		root, err := s.backend.CommitValues(slots)
		if err != nil {
			return nil, err
		}
		return &node{
			height: 0,
			root:   root,
			slots:  slots,
		}, nil
	}

	childChunk, err := nodeCapacity(height - 1)
	if err != nil {
		return nil, err
	}
	childCount := ceilDiv(uint64(len(values)), childChunk)
	children := make([]*node, childCount)
	slots := make([]cid.Cid, childCount)

	for i := 0; i < childCount; i++ {
		start := uint64(i) * childChunk
		end := minUint64(start+childChunk, uint64(len(values)))
		child, err := s.buildNode(values[int(start):int(end)], height-1)
		if err != nil {
			return nil, err
		}
		children[i] = child
		slots[i] = child.root
	}

	root, err := s.backend.CommitValues(slots)
	if err != nil {
		return nil, err
	}
	return &node{
		height:   height,
		root:     root,
		slots:    slots,
		children: children,
	}, nil
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

func requiredHeight(length uint64) int {
	if length <= Fanout {
		return 0
	}
	capacity := uint64(Fanout)
	height := 0
	for capacity < length {
		height++
		if capacity > maxUint64/uint64(Fanout) {
			return height
		}
		capacity *= uint64(Fanout)
	}
	return height
}

func nodeCapacity(height int) (uint64, error) {
	capacity := uint64(Fanout)
	for i := 0; i < height; i++ {
		if capacity > maxUint64/uint64(Fanout) {
			return 0, fmt.Errorf("list capacity overflow at height %d", height)
		}
		capacity *= uint64(Fanout)
	}
	return capacity, nil
}

func indexDigits(index uint64, height int) ([]int, error) {
	digits := make([]int, height+1)
	remaining := index
	for level := 0; level <= height; level++ {
		exp := height - level
		if exp == 0 {
			digits[level] = int(remaining)
			continue
		}
		chunk, err := nodeCapacity(exp - 1)
		if err != nil {
			return nil, err
		}
		digits[level] = int(remaining / chunk)
		remaining %= chunk
	}
	return digits, nil
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

func ceilDiv(n, d uint64) int {
	if n == 0 {
		return 0
	}
	return int((n + d - 1) / d)
}

func minUint64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

func lengthCID(length uint64) (cid.Cid, error) {
	var payload [8]byte
	binary.BigEndian.PutUint64(payload[:], length)
	data := append([]byte("malt:list:length:"), payload[:]...)
	sum, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return cid.Undef, err
	}
	return cid.NewCidV1(cid.Raw, sum), nil
}

var _ list.Semantic = (*Semantic)(nil)

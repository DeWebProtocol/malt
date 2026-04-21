// Package indexed implements the degenerate one-node stable-indexed list
// semantic over a fixed-slot backend. Runtime operations execute directly
// against the committed root using EAT-backed slot materialization.
package indexed

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dewebprotocol/malt/core/commitment"
	"github.com/dewebprotocol/malt/core/eat"
	"github.com/dewebprotocol/malt/core/structure"
	"github.com/dewebprotocol/malt/core/structure/list"
	listruntime "github.com/dewebprotocol/malt/core/structure/list/internal/runtime"
	cid "github.com/ipfs/go-cid"
)

type IndexedList struct {
	scheme commitment.IndexCommitment
	eat    eat.EAT
}

type proofEnvelope struct {
	LengthProof []byte `json:"length_proof"`
	KeyProof    []byte `json:"key_proof,omitempty"`
}

func NewList(scheme commitment.IndexCommitment, eat eat.EAT) (*IndexedList, error) {
	if err := listruntime.ValidateCommitment(scheme); err != nil {
		return nil, err
	}
	if eat == nil {
		return nil, fmt.Errorf("eat is nil")
	}
	return &IndexedList{
		scheme: scheme,
		eat:    eat,
	}, nil
}

func (s *IndexedList) Commit(ctx context.Context, bucketID string, view list.View) (cid.Cid, error) {
	values, err := valuesFromView(view)
	if err != nil {
		return cid.Undef, err
	}
	if len(values) > listruntime.Fanout {
		return cid.Undef, fmt.Errorf("list length %d exceeds indexed capacity %d", len(values), listruntime.Fanout)
	}

	slots := listruntime.EmptyRootSlots()
	lengthMarker, err := listruntime.EncodeLengthMarker(uint64(len(values)))
	if err != nil {
		return cid.Undef, err
	}
	slots[0] = lengthMarker
	copy(slots[1:], values)
	return s.commitSlots(ctx, bucketID, slots)
}

func (s *IndexedList) Prove(ctx context.Context, bucketID string, root cid.Cid, index uint64) (list.Query, structure.Proof, error) {
	slots, length, err := s.loadRoot(ctx, bucketID, root)
	if err != nil {
		return list.Query{}, nil, err
	}

	_, lengthProof, err := listruntime.ProveSlot(s.scheme, root, slots, 0)
	if err != nil {
		return list.Query{}, nil, err
	}

	envelope := proofEnvelope{LengthProof: lengthProof}
	query := list.Query{Length: length}

	if index < length {
		keyCell, keyProof, err := listruntime.ProveSlot(s.scheme, root, slots, index+1)
		if err != nil {
			return list.Query{}, nil, err
		}
		query.Key, err = keyCell.AsCID()
		if err != nil {
			return list.Query{}, nil, err
		}
		envelope.KeyProof = keyProof
	} else {
		query.Key = cid.Undef
	}

	proofBytes, err := json.Marshal(envelope)
	if err != nil {
		return list.Query{}, nil, err
	}
	return query, structure.Proof(proofBytes), nil
}

func (s *IndexedList) Verify(root cid.Cid, index uint64, expected list.Query, proof structure.Proof) (bool, error) {
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
	ok, err := listruntime.VerifySlot(s.scheme, root, 0, commitment.CellFromCID(lengthMarker), envelope.LengthProof)
	if err != nil || !ok {
		return ok, err
	}

	if index >= expected.Length {
		if expected.Key.Defined() {
			return false, nil
		}
		return len(envelope.KeyProof) == 0, nil
	}
	if !expected.Key.Defined() || len(envelope.KeyProof) == 0 {
		return false, nil
	}
	return listruntime.VerifySlot(s.scheme, root, index+1, commitment.CellFromCID(expected.Key), envelope.KeyProof)
}

func (s *IndexedList) Replace(ctx context.Context, bucketID string, root cid.Cid, index uint64, oldKey, newKey cid.Cid) (cid.Cid, error) {
	slots, length, err := s.loadRoot(ctx, bucketID, root)
	if err != nil {
		return cid.Undef, err
	}
	if index >= length {
		return cid.Undef, fmt.Errorf("index %d out of range", index)
	}
	if !slots[index+1].Equals(oldKey) {
		return cid.Undef, fmt.Errorf("old key mismatch at index %d", index)
	}

	nextSlots := cloneSlots(slots)
	nextSlots[index+1] = newKey
	return s.commitSlots(ctx, bucketID, nextSlots)
}

func (s *IndexedList) Append(ctx context.Context, bucketID string, root cid.Cid, key cid.Cid) (cid.Cid, uint64, error) {
	slots, length, err := s.loadRoot(ctx, bucketID, root)
	if err != nil {
		return cid.Undef, 0, err
	}
	if length >= listruntime.Fanout {
		return cid.Undef, 0, fmt.Errorf("list length %d exceeds indexed capacity %d", length, listruntime.Fanout)
	}
	if slots[length+1].Defined() {
		return cid.Undef, 0, fmt.Errorf("append slot %d is already occupied", length)
	}

	nextSlots := cloneSlots(slots)
	lengthMarker, err := listruntime.EncodeLengthMarker(length + 1)
	if err != nil {
		return cid.Undef, 0, err
	}
	nextSlots[0] = lengthMarker
	nextSlots[length+1] = key

	newRoot, err := s.commitSlots(ctx, bucketID, nextSlots)
	return newRoot, length, err
}

func (s *IndexedList) Truncate(ctx context.Context, bucketID string, root cid.Cid, newLen uint64) (cid.Cid, error) {
	slots, length, err := s.loadRoot(ctx, bucketID, root)
	if err != nil {
		return cid.Undef, err
	}
	if newLen > length {
		return cid.Undef, fmt.Errorf("new length %d exceeds current length %d", newLen, length)
	}
	if newLen == length {
		return root, nil
	}

	nextSlots := listruntime.EmptyRootSlots()
	lengthMarker, err := listruntime.EncodeLengthMarker(newLen)
	if err != nil {
		return cid.Undef, err
	}
	nextSlots[0] = lengthMarker
	copy(nextSlots[1:], slots[1:1+newLen])
	return s.commitSlots(ctx, bucketID, nextSlots)
}

func (s *IndexedList) loadRoot(ctx context.Context, bucketID string, root cid.Cid) ([]cid.Cid, uint64, error) {
	slots, err := listruntime.LoadSlots(ctx, s.eat, bucketID, root, listruntime.RootWidth)
	if err != nil {
		return nil, 0, err
	}
	length, err := listruntime.DecodeLengthMarker(slots[0])
	if err != nil {
		return nil, 0, err
	}
	return slots, length, nil
}

func (s *IndexedList) commitSlots(ctx context.Context, bucketID string, slots []cid.Cid) (cid.Cid, error) {
	root, err := listruntime.CommitSlots(s.scheme, slots)
	if err != nil {
		return cid.Undef, err
	}
	if err := listruntime.StoreSlots(ctx, s.eat, bucketID, root, slots); err != nil {
		return cid.Undef, err
	}
	return root, nil
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

func cloneSlots(slots []cid.Cid) []cid.Cid {
	return append([]cid.Cid(nil), slots...)
}

var _ list.Semantic = (*IndexedList)(nil)

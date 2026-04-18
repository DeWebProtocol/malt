// Package indexed implements the stable-indexed list semantic using a fixed-slot backend.
package indexed

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

type Semantic struct {
	backend commitment.ListBackend
}

type proofEnvelope struct {
	LengthProof []byte `json:"length_proof"`
	ValueProof  []byte `json:"value_proof,omitempty"`
}

func New(backend commitment.ListBackend) (*Semantic, error) {
	if backend == nil {
		return nil, fmt.Errorf("list backend is nil")
	}
	return &Semantic{backend: backend}, nil
}

func (s *Semantic) Commit(ctx context.Context, view list.View) (cid.Cid, error) {
	_ = ctx
	values, err := valuesFromView(view)
	if err != nil {
		return cid.Undef, err
	}
	backing, err := backingValues(values)
	if err != nil {
		return cid.Undef, err
	}
	if len(backing) > s.backend.MaxValues() {
		return cid.Undef, fmt.Errorf("list length %d exceeds backend capacity %d", len(values), s.backend.MaxValues()-1)
	}
	return s.backend.CommitValues(backing)
}

func (s *Semantic) Prove(ctx context.Context, root cid.Cid, view list.View, index uint64) (list.Query, structure.Proof, error) {
	_ = ctx
	values, err := valuesFromView(view)
	if err != nil {
		return list.Query{}, nil, err
	}
	backing, err := backingValues(values)
	if err != nil {
		return list.Query{}, nil, err
	}
	length := uint64(len(values))
	lengthMarker, err := lengthCID(length)
	if err != nil {
		return list.Query{}, nil, err
	}

	_, lengthProof, err := s.backend.ProveIndex(root, backing, 0)
	if err != nil {
		return list.Query{}, nil, err
	}

	envelope := proofEnvelope{LengthProof: lengthProof}
	query := list.Query{
		Length:  length,
		Present: index < length,
	}

	if query.Present {
		value, valueProof, err := s.backend.ProveIndex(root, backing, index+1)
		if err != nil {
			return list.Query{}, nil, err
		}
		query.Value = value
		envelope.ValueProof = valueProof
	} else {
		query.Value = cid.Undef
	}

	proofBytes, err := json.Marshal(envelope)
	if err != nil {
		return list.Query{}, nil, err
	}

	// Force the length marker to be derivable from the query.
	if !lengthMarker.Defined() {
		return list.Query{}, nil, fmt.Errorf("length marker is undefined")
	}

	return query, structure.Proof(proofBytes), nil
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

	if !expected.Present {
		if index < expected.Length {
			return false, nil
		}
		return len(envelope.ValueProof) == 0, nil
	}

	if index >= expected.Length || len(envelope.ValueProof) == 0 {
		return false, nil
	}
	return s.backend.VerifyIndex(root, index+1, expected.Value, envelope.ValueProof)
}

func (s *Semantic) Replace(ctx context.Context, root cid.Cid, view list.View, index uint64, oldValue, newValue cid.Cid) (cid.Cid, error) {
	_ = ctx
	values, err := valuesFromView(view)
	if err != nil {
		return cid.Undef, err
	}
	if index >= uint64(len(values)) {
		return cid.Undef, fmt.Errorf("index %d out of range", index)
	}
	if !values[index].Equals(oldValue) {
		return cid.Undef, fmt.Errorf("old value mismatch at index %d", index)
	}
	backing, err := backingValues(values)
	if err != nil {
		return cid.Undef, err
	}
	return s.backend.ReplaceIndex(root, backing, index+1, oldValue, newValue)
}

func (s *Semantic) Append(ctx context.Context, root cid.Cid, view list.View, value cid.Cid) (cid.Cid, uint64, error) {
	_ = ctx
	values, err := valuesFromView(view)
	if err != nil {
		return cid.Undef, 0, err
	}
	newIndex := uint64(len(values))
	values = append(values, value)
	backing, err := backingValues(values)
	if err != nil {
		return cid.Undef, 0, err
	}
	if len(backing) > s.backend.MaxValues() {
		return cid.Undef, 0, fmt.Errorf("list length %d exceeds backend capacity %d", len(values), s.backend.MaxValues()-1)
	}
	newRoot, err := s.backend.CommitValues(backing)
	return newRoot, newIndex, err
}

func (s *Semantic) Truncate(ctx context.Context, root cid.Cid, view list.View, newLen uint64) (cid.Cid, error) {
	_ = ctx
	values, err := valuesFromView(view)
	if err != nil {
		return cid.Undef, err
	}
	if newLen > uint64(len(values)) {
		return cid.Undef, fmt.Errorf("new length %d exceeds current length %d", newLen, len(values))
	}
	if newLen == uint64(len(values)) {
		return root, nil
	}
	values = values[:newLen]
	backing, err := backingValues(values)
	if err != nil {
		return cid.Undef, err
	}
	return s.backend.CommitValues(backing)
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

func backingValues(values []cid.Cid) ([]cid.Cid, error) {
	lengthMarker, err := lengthCID(uint64(len(values)))
	if err != nil {
		return nil, err
	}
	out := make([]cid.Cid, len(values)+1)
	out[0] = lengthMarker
	copy(out[1:], values)
	return out, nil
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

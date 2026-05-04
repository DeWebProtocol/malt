// Package gateway defines the library-level boundary for semantic mutations.
package gateway

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/dewebprotocol/malt/core/arctable"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/structure/list"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

var (
	// ErrInvalidNamespace is returned when an executor has no internal materialization namespace.
	ErrInvalidNamespace = errors.New("invalid materialization namespace")

	// ErrInvalidBaseRoot is returned when an update mutation has no base root.
	ErrInvalidBaseRoot = errors.New("invalid base root")

	// ErrEmptyPuts is returned when a semantic mutation carries no arcset replacements.
	ErrEmptyPuts = errors.New("empty puts")

	// ErrObjectKindMismatch is returned when a put kind disagrees with its object or arcset kind.
	ErrObjectKindMismatch = errors.New("object kind mismatch")

	// ErrNilArcSet is returned when a put has no canonical arcset.
	ErrNilArcSet = errors.New("nil arcset")
)

// SemanticMutation is the gateway write boundary emitted by application layouts.
type SemanticMutation struct {
	BaseRoot cid.Cid
	Puts     []ArcSetPut
}

// ArcSetPut replaces one touched semantic object's full canonical arcset.
type ArcSetPut struct {
	Object cid.Cid
	Kind   arcset.Kind
	ArcSet *arcset.CanonicalArcSet
}

// WriteReceipt records the library-level outcome of applying a semantic mutation.
type WriteReceipt struct {
	BaseRoot cid.Cid
	NewRoot  cid.Cid
	PutCount int
	ArcCount int
}

// Executor submits semantic mutations to the current map/list semantic backends.
type Executor struct {
	Namespace string
	Maps      mapping.Semantics
	Lists     list.Semantics
	ArcTable  arctable.ArcTable
}

// ValidateSemanticMutation validates the shape of an update semantic mutation.
func ValidateSemanticMutation(mut SemanticMutation) error {
	if !mut.BaseRoot.Defined() {
		return ErrInvalidBaseRoot
	}
	if len(mut.Puts) == 0 {
		return ErrEmptyPuts
	}
	for i, put := range mut.Puts {
		if put.ArcSet == nil {
			return fmt.Errorf("put %d: %w", i, ErrNilArcSet)
		}
		if put.Kind != put.ArcSet.Kind() {
			return fmt.Errorf("put %d: %w", i, ErrObjectKindMismatch)
		}
		if !objectKindMatches(put.Object, put.Kind) {
			return fmt.Errorf("put %d: %w", i, ErrObjectKindMismatch)
		}
	}
	return nil
}

// Apply commits full canonical arcset replacements in order.
//
// The executor treats BaseRoot as the caller's update base for receipt and
// validation purposes only. It does not publish heads, arbitrate freshness, or
// merge concurrent roots.
func (e Executor) Apply(ctx context.Context, mut SemanticMutation) (WriteReceipt, error) {
	if e.Namespace == "" {
		return WriteReceipt{}, ErrInvalidNamespace
	}
	if err := ValidateSemanticMutation(mut); err != nil {
		return WriteReceipt{}, err
	}

	var newRoot cid.Cid
	arcCount := 0
	for i, put := range mut.Puts {
		root, count, err := e.commitPut(ctx, e.Namespace, put)
		if err != nil {
			return WriteReceipt{}, fmt.Errorf("put %d: %w", i, err)
		}
		newRoot = root
		arcCount += count
	}

	return WriteReceipt{
		BaseRoot: mut.BaseRoot,
		NewRoot:  newRoot,
		PutCount: len(mut.Puts),
		ArcCount: arcCount,
	}, nil
}

func (e Executor) commitPut(ctx context.Context, namespace string, put ArcSetPut) (cid.Cid, int, error) {
	switch put.Kind {
	case arcset.KindMap:
		if e.Maps == nil {
			return cid.Undef, 0, errors.New("map semantics is nil")
		}
		view, err := canonicalMapView(put.ArcSet)
		if err != nil {
			return cid.Undef, 0, err
		}
		root, err := e.Maps.Commit(ctx, namespace, view)
		if err != nil {
			return cid.Undef, 0, err
		}
		if e.ArcTable != nil {
			snapshot, err := canonicalMapSnapshot(put.ArcSet)
			if err != nil {
				return cid.Undef, 0, err
			}
			if err := e.ArcTable.Update(ctx, namespace, root, put.Object, snapshot); err != nil {
				return cid.Undef, 0, err
			}
		}
		return root, put.ArcSet.Len(), nil
	case arcset.KindList:
		if e.Lists == nil {
			return cid.Undef, 0, errors.New("list semantics is nil")
		}
		view, err := canonicalListView(put.ArcSet)
		if err != nil {
			return cid.Undef, 0, err
		}
		root, err := e.Lists.Commit(ctx, namespace, view)
		if err != nil {
			return cid.Undef, 0, err
		}
		return root, put.ArcSet.Len(), nil
	default:
		return cid.Undef, 0, fmt.Errorf("%w: %q", arcset.ErrInvalidKind, put.Kind)
	}
}

func canonicalMapView(set *arcset.CanonicalArcSet) (mapping.View, error) {
	entries := make(map[arcset.Path]cid.Cid, set.Len())
	for _, entry := range set.Entries() {
		path := arcset.CanonicalizePath(entry.Coordinate.String())
		if path.IsEmpty() || path.String() != entry.Coordinate.String() {
			return nil, fmt.Errorf("invalid canonical map coordinate %q", entry.Coordinate.String())
		}
		entries[path] = entry.Target.CID()
	}
	return mapping.NewViewFromPaths(entries), nil
}

func canonicalMapSnapshot(set *arcset.CanonicalArcSet) (arcset.ArcSet, error) {
	entries := make(map[arcset.Path]cid.Cid, set.Len())
	for _, entry := range set.Entries() {
		path := arcset.CanonicalizePath(entry.Coordinate.String())
		if path.IsEmpty() || path.String() != entry.Coordinate.String() {
			return nil, fmt.Errorf("invalid canonical map coordinate %q", entry.Coordinate.String())
		}
		entries[path] = entry.Target.CID()
	}
	return arcset.NewArcSetFromPaths(entries)
}

func canonicalListView(set *arcset.CanonicalArcSet) (list.View, error) {
	entries := set.Entries()
	values := make([]cid.Cid, len(entries))
	for i, entry := range entries {
		raw := entry.Coordinate.Bytes()
		if len(raw) != 8 {
			return nil, fmt.Errorf("invalid canonical list coordinate %q", entry.Coordinate.String())
		}
		index := binary.BigEndian.Uint64(raw)
		if index != uint64(i) {
			return nil, fmt.Errorf("canonical list arcset is sparse at index %d", index)
		}
		values[i] = entry.Target.CID()
	}
	return list.NewViewFromSlice(values), nil
}

func objectKindMatches(object cid.Cid, kind arcset.Kind) bool {
	if !object.Defined() {
		return true
	}

	switch codec.SemanticKindOf(object) {
	case codec.SemanticKindUnknown:
		return true
	case codec.SemanticKindMap:
		return kind == arcset.KindMap
	case codec.SemanticKindList:
		return kind == arcset.KindList
	default:
		return false
	}
}

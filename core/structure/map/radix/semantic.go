// Package radix implements the keyed map semantic using the radix backend.
package radix

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/core/sce/commitment"
	"github.com/dewebprotocol/malt/core/structure"
	mapping "github.com/dewebprotocol/malt/core/structure/map"
	"github.com/dewebprotocol/malt/core/types/arcset"
	cid "github.com/ipfs/go-cid"
)

type Semantic struct {
	backend commitment.MappingBackend
}

func New(backend commitment.MappingBackend) (*Semantic, error) {
	if backend == nil {
		return nil, fmt.Errorf("mapping backend is nil")
	}
	return &Semantic{backend: backend}, nil
}

func (s *Semantic) Commit(ctx context.Context, view mapping.View) (cid.Cid, error) {
	_ = ctx
	bindings, err := viewToArcSet(view)
	if err != nil {
		return cid.Undef, err
	}
	return s.backend.CommitBindings(bindings)
}

func (s *Semantic) Prove(ctx context.Context, root cid.Cid, view mapping.View, key arcset.Path) (mapping.Binding, structure.Proof, error) {
	_ = ctx
	bindings, err := viewToArcSet(view)
	if err != nil {
		return mapping.Binding{}, nil, err
	}
	value, present, proof, err := s.backend.ProveBinding(root, bindings, key)
	if err != nil {
		return mapping.Binding{}, nil, err
	}
	return mapping.Binding{Value: value, Present: present}, structure.Proof(proof), nil
}

func (s *Semantic) Verify(root cid.Cid, key arcset.Path, expected mapping.Binding, proof structure.Proof) (bool, error) {
	return s.backend.VerifyBinding(root, key, expected.Value, expected.Present, proof)
}

func (s *Semantic) Update(ctx context.Context, root cid.Cid, view mapping.View, key arcset.Path, oldValue, newValue cid.Cid) (cid.Cid, error) {
	_ = ctx
	bindings, err := viewToArcSet(view)
	if err != nil {
		return cid.Undef, err
	}
	return s.backend.UpdateBinding(root, bindings, key, oldValue, newValue)
}

func viewToArcSet(view mapping.View) (arcset.ArcSet, error) {
	if view == nil {
		return nil, fmt.Errorf("mapping view is nil")
	}
	out := make(map[string]cid.Cid, view.Len())
	iter := view.Iterate()
	for {
		key, value, ok := iter.Next()
		if !ok {
			break
		}
		out[key.String()] = value
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}
	return arcset.NewSetFrom(out), nil
}

var _ mapping.Semantic = (*Semantic)(nil)

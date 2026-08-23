// Package memory provides an in-memory CAS for runtime tests.
package memory

import (
	"context"
	"fmt"
	"sync"

	"github.com/dewebprotocol/malt-client/internal/cas"
	cid "github.com/ipfs/go-cid"
)

// Store is a concurrency-safe in-memory content-addressed store.
type Store struct {
	mu     sync.RWMutex
	blocks map[string][]byte
}

// New creates an empty store.
func New() *Store { return &Store{blocks: make(map[string][]byte)} }

func (s *Store) Get(ctx context.Context, c cid.Cid) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !c.Defined() {
		return nil, fmt.Errorf("%w: undefined CID", cas.ErrCorruptedBlock)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	data, ok := s.blocks[c.String()]
	if !ok {
		return nil, fmt.Errorf("%w: %s", cas.ErrNotFound, c)
	}
	return append([]byte(nil), data...), nil
}

func (s *Store) Has(ctx context.Context, c cid.Cid) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if !c.Defined() {
		return false, fmt.Errorf("%w: undefined CID", cas.ErrCorruptedBlock)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.blocks[c.String()]
	return ok, nil
}

func (s *Store) Put(ctx context.Context, data []byte) (cid.Cid, error) {
	return s.PutWithCodec(ctx, data, cid.Raw)
}

func (s *Store) PutWithCodec(ctx context.Context, data []byte, codec uint64) (cid.Cid, error) {
	if err := ctx.Err(); err != nil {
		return cid.Undef, err
	}
	c, err := cas.CIDForBlock(cas.Block{Data: data, Codec: codec})
	if err != nil {
		return cid.Undef, err
	}
	s.mu.Lock()
	s.blocks[c.String()] = append([]byte(nil), data...)
	s.mu.Unlock()
	return c, nil
}

var _ cas.Client = (*Store)(nil)

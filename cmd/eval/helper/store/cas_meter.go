package store

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/core/cas"
	"github.com/dewebprotocol/malt/core/kvstore"
	cid "github.com/ipfs/go-cid"
)

// MeteredCAS is a deterministic local CAS with per-system write accounting.
type MeteredCAS struct {
	kv    kvstore.KVStore
	meter *Meter
}

// NewMeteredCAS creates a CAS backed by kv.
func NewMeteredCAS(kv kvstore.KVStore, meter *Meter) *MeteredCAS {
	return &MeteredCAS{kv: kv, meter: meter}
}

func (c *MeteredCAS) Get(ctx context.Context, block cid.Cid) ([]byte, error) {
	data, err := c.kv.Get(ctx, casKey(block))
	if err != nil {
		return nil, fmt.Errorf("block not found: %s", block)
	}
	return data, nil
}

func (c *MeteredCAS) Has(ctx context.Context, block cid.Cid) (bool, error) {
	return c.kv.Has(ctx, casKey(block))
}

func (c *MeteredCAS) HasBatch(ctx context.Context, blocks []cid.Cid) ([]bool, error) {
	out := make([]bool, len(blocks))
	for i, block := range blocks {
		ok, err := c.Has(ctx, block)
		if err != nil {
			return nil, err
		}
		out[i] = ok
	}
	return out, nil
}

func (c *MeteredCAS) Put(ctx context.Context, data []byte) (cid.Cid, error) {
	return c.PutWithCodec(ctx, data, cid.Raw)
}

func (c *MeteredCAS) PutWithCodec(ctx context.Context, data []byte, codec uint64) (cid.Cid, error) {
	blockCID, err := cas.CIDForBlock(cas.Block{Data: data, Codec: codec})
	if err != nil {
		return cid.Undef, err
	}
	key := casKey(blockCID)
	exists, err := c.kv.Has(ctx, key)
	if err != nil {
		return cid.Undef, err
	}
	if !exists {
		if err := c.kv.Put(ctx, key, data); err != nil {
			return cid.Undef, err
		}
	}
	c.meter.RecordCASPut(categoryForCodec(codec), len(data), !exists)
	return blockCID, nil
}

func (c *MeteredCAS) PutBatch(ctx context.Context, blocks []cas.Block) ([]cas.PutResult, error) {
	results := make([]cas.PutResult, len(blocks))
	seen := make(map[string]struct{}, len(blocks))
	for i, block := range blocks {
		codec := cas.NormalizeCodec(block.Codec)
		blockCID, err := cas.CIDForBlock(cas.Block{Data: block.Data, Codec: codec})
		if err != nil {
			return nil, err
		}
		key := casKey(blockCID)
		keyString := string(key)
		if _, ok := seen[keyString]; ok {
			c.meter.RecordCASPut(categoryForCodec(codec), len(block.Data), false)
			results[i] = cas.PutResult{CID: blockCID, Status: cas.PutStatusDuplicate}
			continue
		}
		seen[keyString] = struct{}{}
		exists, err := c.kv.Has(ctx, key)
		if err != nil {
			return nil, err
		}
		status := cas.PutStatusAlreadyPresent
		if !exists {
			if err := c.kv.Put(ctx, key, block.Data); err != nil {
				return nil, err
			}
			status = cas.PutStatusStored
		}
		c.meter.RecordCASPut(categoryForCodec(codec), len(block.Data), !exists)
		results[i] = cas.PutResult{CID: blockCID, Status: status}
	}
	return results, nil
}

func casKey(block cid.Cid) []byte {
	return []byte("cas:" + block.String())
}

func categoryForCodec(codec uint64) Category {
	if cas.NormalizeCodec(codec) == cid.Raw {
		return CategoryCASPayload
	}
	return CategoryCASMetadata
}

var _ cas.Client = (*MeteredCAS)(nil)
var _ cas.TypedWriter = (*MeteredCAS)(nil)
var _ cas.BatchReader = (*MeteredCAS)(nil)
var _ cas.BatchWriter = (*MeteredCAS)(nil)

// Package hybrid owns transport-level composition policy. It keeps backend
// selection and cache behavior outside application, filesystem, and trust
// packages.
package hybrid

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
	cid "github.com/ipfs/go-cid"
)

// CASErrorObserver receives non-authoritative cache failures. Cache failures
// never turn verified primary bytes into unverified output or make cache state
// authoritative.
type CASErrorObserver func(error)

// CASOptions configures a primary CAS plus a read-through local cache. Primary
// alone decides Has and must persist every successful write; Cache is only an
// availability/performance optimization.
type CASOptions struct {
	Primary      transportcap.CAS
	Cache        transportcap.CAS
	OnCacheError CASErrorObserver
}

// CAS composes an authoritative transport with a non-authoritative verified
// cache. It is topology policy, not application policy.
type CAS struct {
	primary      transportcap.CAS
	cache        transportcap.CAS
	onCacheError CASErrorObserver
}

func NewCAS(options CASOptions) (*CAS, error) {
	if nilInterface(options.Primary) {
		return nil, fmt.Errorf("hybrid CAS primary is nil")
	}
	if nilInterface(options.Cache) {
		return nil, fmt.Errorf("hybrid CAS cache is nil")
	}
	return &CAS{primary: options.Primary, cache: options.Cache, onCacheError: options.OnCacheError}, nil
}

func (c *CAS) Put(ctx context.Context, data []byte) (cid.Cid, error) {
	return c.PutWithCodec(ctx, data, cid.Raw)
}

func (c *CAS) PutWithCodec(ctx context.Context, data []byte, codec uint64) (cid.Cid, error) {
	if err := c.ready(); err != nil {
		return cid.Undef, err
	}
	want, err := clientcas.CIDForBlock(transportcap.Block{Data: data, Codec: codec})
	if err != nil {
		return cid.Undef, err
	}
	got, err := c.primary.PutWithCodec(ctx, data, codec)
	if err != nil {
		return cid.Undef, err
	}
	if !got.Equals(want) {
		return cid.Undef, fmt.Errorf("%w: hybrid primary returned %s, want %s", transportcap.ErrCorruptedBlock, got, want)
	}
	c.fill(ctx, want, data, codec)
	return want, nil
}

func (c *CAS) Get(ctx context.Context, key cid.Cid) ([]byte, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	if !key.Defined() {
		return nil, fmt.Errorf("%w: hybrid CAS key is undefined", transportcap.ErrCorruptedBlock)
	}
	if data, err := verifiedGet(ctx, c.cache, key); err == nil {
		return data, nil
	} else {
		if !errors.Is(err, transportcap.ErrNotFound) {
			c.observeCacheError(err)
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	data, err := verifiedGet(ctx, c.primary, key)
	if err != nil {
		return nil, err
	}
	c.fill(ctx, key, data, key.Prefix().Codec)
	return data, nil
}

// Has deliberately consults only Primary. A cache hit cannot assert that an
// upload target, Gateway, or peer already persists the block.
func (c *CAS) Has(ctx context.Context, key cid.Cid) (bool, error) {
	if err := c.ready(); err != nil {
		return false, err
	}
	if !key.Defined() {
		return false, fmt.Errorf("%w: hybrid CAS key is undefined", transportcap.ErrCorruptedBlock)
	}
	return c.primary.Has(ctx, key)
}

func (c *CAS) PutBatch(ctx context.Context, blocks []transportcap.Block) ([]transportcap.PutResult, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		return []transportcap.PutResult{}, nil
	}
	results, err := clientcas.NewVerifyingReader(c.primary).PutBatch(ctx, blocks)
	if err != nil {
		return nil, err
	}
	for index, result := range results {
		if !transportcap.IsValidPutStatus(result.Status) {
			return nil, fmt.Errorf("%w: hybrid primary batch result %d has unsupported status %q", transportcap.ErrCorruptedBlock, index, result.Status)
		}
		c.fill(ctx, result.CID, blocks[index].Data, blocks[index].Codec)
	}
	return results, nil
}

// HasBatch preserves Primary authority even when Cache contains every block.
func (c *CAS) HasBatch(ctx context.Context, keys []cid.Cid) ([]bool, error) {
	if err := c.ready(); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return []bool{}, nil
	}
	for index, key := range keys {
		if !key.Defined() {
			return nil, fmt.Errorf("%w: hybrid CAS batch CID %d is undefined", transportcap.ErrCorruptedBlock, index)
		}
	}
	if batch, ok := c.primary.(interface {
		HasBatch(context.Context, []cid.Cid) ([]bool, error)
	}); ok {
		result, err := batch.HasBatch(ctx, keys)
		if err != nil {
			return nil, err
		}
		if len(result) != len(keys) {
			return nil, fmt.Errorf("%w: hybrid primary returned %d has results for %d CIDs", transportcap.ErrCorruptedBlock, len(result), len(keys))
		}
		return result, nil
	}
	result := make([]bool, len(keys))
	for index, key := range keys {
		present, err := c.primary.Has(ctx, key)
		if err != nil {
			return nil, err
		}
		result[index] = present
	}
	return result, nil
}

func (c *CAS) fill(ctx context.Context, want cid.Cid, data []byte, codec uint64) {
	if err := ctx.Err(); err != nil {
		return
	}
	got, err := c.cache.PutWithCodec(ctx, data, codec)
	if err == nil && !got.Equals(want) {
		err = fmt.Errorf("%w: hybrid cache returned %s, want %s", transportcap.ErrCorruptedBlock, got, want)
	}
	if err != nil {
		c.observeCacheError(err)
	}
}

func (c *CAS) observeCacheError(err error) {
	if err != nil && c.onCacheError != nil {
		c.onCacheError(err)
	}
}

func (c *CAS) ready() error {
	if c == nil || nilInterface(c.primary) || nilInterface(c.cache) {
		return fmt.Errorf("hybrid CAS is nil or uninitialized")
	}
	return nil
}

func verifiedGet(ctx context.Context, source transportcap.CAS, key cid.Cid) ([]byte, error) {
	data, err := source.Get(ctx, key)
	if err != nil {
		return nil, err
	}
	got, err := key.Prefix().Sum(data)
	if err != nil || !got.Equals(key) {
		return nil, fmt.Errorf("%w: hybrid source body does not match %s", transportcap.ErrCorruptedBlock, key)
	}
	return data, nil
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

var _ transportcap.BatchCAS = (*CAS)(nil)

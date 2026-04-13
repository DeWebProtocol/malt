// Package mock provides a mock CAS implementation for testing.
// It uses a KVStore-backed block service with configurable latency
// to simulate IPFS-like behavior.
package mock

import (
	"context"
	"fmt"
	"math/rand"
	"time"

	"github.com/dewebprotocol/malt/core/cas"
	"github.com/dewebprotocol/malt/core/kvstore/memory"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// CAS is a mock CAS for testing, backed by KVStore with simulated latency.
type CAS struct {
	kv       *memory.KV
	latency  time.Duration
	jitter   time.Duration
}

// Option configures a mock CAS.
type Option func(*options)

type options struct {
	latency time.Duration
	jitter  time.Duration
}

func defaultOptions() *options {
	return &options{
		latency: 100 * time.Microsecond,
		jitter:  50 * time.Microsecond,
	}
}

// WithLatency sets the simulated per-operation latency.
func WithLatency(d time.Duration) Option {
	return func(o *options) {
		o.latency = d
	}
}

// WithJitter sets the simulated latency jitter (random ± value).
func WithJitter(d time.Duration) Option {
	return func(o *options) {
		o.jitter = d
	}
}

// WithoutLatency disables latency simulation entirely.
func WithoutLatency() Option {
	return func(o *options) {
		o.latency = 0
		o.jitter = 0
	}
}

// NewCAS creates a mock CAS backed by an in-memory KVStore.
// By default, operations have ~100µs latency ± 50µs jitter to simulate
// IPFS daemon behavior. Use WithLatency/WithoutLatency to adjust.
func NewCAS(opts ...Option) *CAS {
	options := defaultOptions()
	for _, opt := range opts {
		opt(options)
	}

	return &CAS{
		kv:      memory.New(),
		latency: options.latency,
		jitter:  options.jitter,
	}
}

// simulateLatency sleeps for latency ± jitter.
func (m *CAS) simulateLatency() {
	if m.latency == 0 {
		return
	}
	if m.jitter == 0 {
		time.Sleep(m.latency)
		return
	}
	jittered := m.latency + time.Duration(rand.Int63n(int64(m.jitter)*2)-int64(m.jitter))
	if jittered < 0 {
		jittered = 0
	}
	time.Sleep(jittered)
}

func blockKey(c cid.Cid) []byte {
	return []byte("block/" + c.String())
}

// Get retrieves a block from mock storage.
func (m *CAS) Get(ctx context.Context, c cid.Cid) ([]byte, error) {
	m.simulateLatency()

	data, err := m.kv.Get(ctx, blockKey(c))
	if err != nil {
		return nil, fmt.Errorf("block not found: %s", c.String())
	}
	return data, nil
}

// Put stores a block in mock storage.
func (m *CAS) Put(ctx context.Context, data []byte) (cid.Cid, error) {
	m.simulateLatency()

	mhash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return cid.Cid{}, err
	}
	c := cid.NewCidV1(cid.Raw, mhash)

	if err := m.kv.Put(ctx, blockKey(c), data); err != nil {
		return cid.Cid{}, fmt.Errorf("failed to store block: %w", err)
	}
	return c, nil
}

// Has checks if a block exists in mock storage.
func (m *CAS) Has(ctx context.Context, c cid.Cid) (bool, error) {
	m.simulateLatency()

	exists, err := m.kv.Has(ctx, blockKey(c))
	if err != nil {
		return false, fmt.Errorf("failed to check block: %w", err)
	}
	return exists, nil
}

// AddBlock adds a pre-existing block to mock storage.
func (m *CAS) AddBlock(c cid.Cid, data []byte) {
	_ = m.kv.Put(context.Background(), blockKey(c), data)
}

// Ensure CAS implements cas.Client.
var _ cas.Client = (*CAS)(nil)

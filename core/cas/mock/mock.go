// Package mock provides a mock CAS implementation for testing.
// It uses a KVStore-backed block service with configurable latency
// to simulate IPFS Kubo behavior.
//
// Default latencies are based on ProbeLab Kubo v0.39.0 e2e measurements
// (Europe Frankfurt, DHT):
//
//	Get: ~2.1s total (TTFB + provider discovery + broadcast)
//	Put: ~1.4s Add Duration (merkle-izing + block storage)
//	Has: ~100ms (index lookup, no data transfer)
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

// Default latency values based on ProbeLab Kubo v0.39.0 e2e measurements
// (Europe Frankfurt, DHT).
const (
	DefaultGetLatency = 2100 * time.Millisecond // TTFB + provider discovery + broadcast
	DefaultPutLatency = 1400 * time.Millisecond // Add Duration: merkle-izing + block storage
	DefaultHasLatency = 100 * time.Millisecond  // index lookup only
	DefaultJitter     = 200 * time.Millisecond  // ± jitter for all operations
)

// CAS is a mock CAS for testing, backed by KVStore with simulated latency.
type CAS struct {
	kv          *memory.KV
	getLatency  time.Duration
	putLatency  time.Duration
	hasLatency  time.Duration
	jitter      time.Duration
}

// Option configures a mock CAS.
type Option func(*options)

type options struct {
	getLatency time.Duration
	putLatency time.Duration
	hasLatency time.Duration
	jitter     time.Duration
}

func defaultOptions() *options {
	return &options{
		getLatency: DefaultGetLatency,
		putLatency: DefaultPutLatency,
		hasLatency: DefaultHasLatency,
		jitter:     DefaultJitter,
	}
}

// WithGetLatency sets the simulated Get operation latency.
func WithGetLatency(d time.Duration) Option {
	return func(o *options) {
		o.getLatency = d
	}
}

// WithPutLatency sets the simulated Put operation latency.
func WithPutLatency(d time.Duration) Option {
	return func(o *options) {
		o.putLatency = d
	}
}

// WithHasLatency sets the simulated Has operation latency.
func WithHasLatency(d time.Duration) Option {
	return func(o *options) {
		o.hasLatency = d
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
		o.getLatency = 0
		o.putLatency = 0
		o.hasLatency = 0
		o.jitter = 0
	}
}

// NewCAS creates a mock CAS backed by an in-memory KVStore.
// By default, operations simulate ProbeLab Kubo v0.39.0 e2e latency.
// Use WithoutLatency for fast unit tests, or individual WithXLatency to tune.
func NewCAS(opts ...Option) *CAS {
	options := defaultOptions()
	for _, opt := range opts {
		opt(options)
	}

	return &CAS{
		kv:         memory.New(),
		getLatency: options.getLatency,
		putLatency: options.putLatency,
		hasLatency: options.hasLatency,
		jitter:     options.jitter,
	}
}

// simulateLatency sleeps for base ± jitter.
func simulateLatency(base, jitter time.Duration) {
	if base == 0 {
		return
	}
	if jitter == 0 {
		time.Sleep(base)
		return
	}
	jittered := base + time.Duration(rand.Int63n(int64(jitter)*2)-int64(jitter))
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
	simulateLatency(m.getLatency, m.jitter)

	data, err := m.kv.Get(ctx, blockKey(c))
	if err != nil {
		return nil, fmt.Errorf("block not found: %s", c.String())
	}
	return data, nil
}

// Put stores a block in mock storage.
func (m *CAS) Put(ctx context.Context, data []byte) (cid.Cid, error) {
	simulateLatency(m.putLatency, m.jitter)

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
	simulateLatency(m.hasLatency, m.jitter)

	exists, err := m.kv.Has(ctx, blockKey(c))
	if err != nil {
		return false, fmt.Errorf("failed to check block: %w", err)
	}
	return exists, nil
}

// AddBlock adds a pre-existing block to mock storage without latency.
func (m *CAS) AddBlock(c cid.Cid, data []byte) {
	_ = m.kv.Put(context.Background(), blockKey(c), data)
}

// Ensure CAS implements cas.Client.
var _ cas.Client = (*CAS)(nil)

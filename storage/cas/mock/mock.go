// Package mock provides a mock CAS implementation for testing.
// It uses a KVStore-backed block service with configurable latency
// for controlled evaluation scenarios. Use WithGetLatency, WithPutLatency,
// and WithHasLatency to set latency values explicitly.
package mock

import (
	"context"
	"fmt"
	"time"

	"github.com/dewebprotocol/malt/storage/cas"
	kvstore "github.com/dewebprotocol/malt/storage/kv"
	"github.com/dewebprotocol/malt/storage/kv/memory"
	cid "github.com/ipfs/go-cid"
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
	kv         kvstore.KVStore
	getLatency time.Duration
	putLatency time.Duration
	hasLatency time.Duration
	jitter     time.Duration
	stats      cas.StatsRecorder
}

// CASStats is a point-in-time snapshot of mock CAS counters.
type CASStats = cas.Stats

// Option configures a mock CAS.
type Option func(*options)

type options struct {
	getLatency time.Duration
	putLatency time.Duration
	hasLatency time.Duration
	jitter     time.Duration
	kv         kvstore.KVStore
}

func defaultOptions() *options {
	return &options{}
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

// WithKVStore stores mock CAS blocks in the supplied KVStore.
func WithKVStore(kv kvstore.KVStore) Option {
	return func(o *options) {
		o.kv = kv
	}
}

// NewCAS creates a mock CAS backed by an in-memory KVStore.
// Latency defaults to zero. Use WithGetLatency, WithPutLatency, WithHasLatency
// and WithJitter to simulate network conditions for evaluation scenarios.
func NewCAS(opts ...Option) *CAS {
	options := defaultOptions()
	for _, opt := range opts {
		opt(options)
	}
	kv := options.kv
	if kv == nil {
		kv = memory.New()
	}

	return &CAS{
		kv:         kv,
		getLatency: options.getLatency,
		putLatency: options.putLatency,
		hasLatency: options.hasLatency,
		jitter:     options.jitter,
	}
}

// startLatencyEnvelope records the entry time for a fixed-latency envelope.
// Call endLatencyEnvelope after the actual operation to guarantee that the
// total time from start to end equals exactly base.
func startLatencyEnvelope() time.Time {
	return time.Now()
}

// endLatencyEnvelope sleeps until base has elapsed since start, ensuring a
// fixed total latency regardless of underlying operation duration.
func endLatencyEnvelope(start time.Time, base time.Duration) {
	if base <= 0 {
		return
	}
	if remaining := base - time.Since(start); remaining > 0 {
		time.Sleep(remaining)
	}
}

func blockKey(c cid.Cid) []byte {
	return []byte("block/" + c.String())
}

// Get retrieves a block from mock storage.
func (m *CAS) Get(ctx context.Context, c cid.Cid) ([]byte, error) {
	m.stats.RecordGetCall()
	start := startLatencyEnvelope()

	data, err := m.kv.Get(ctx, blockKey(c))

	endLatencyEnvelope(start, m.getLatency)
	if err != nil {
		return nil, fmt.Errorf("block not found: %s", c.String())
	}
	m.stats.RecordGetBytes(len(data))
	return data, nil
}

// Put stores a block in mock storage.
func (m *CAS) Put(ctx context.Context, data []byte) (cid.Cid, error) {
	return m.PutWithCodec(ctx, data, cid.Raw)
}

// PutWithCodec stores a block under the requested CID codec.
func (m *CAS) PutWithCodec(ctx context.Context, data []byte, codec uint64) (cid.Cid, error) {
	m.stats.RecordPutCall()
	start := startLatencyEnvelope()

	c, err := cas.CIDForBlock(cas.Block{Data: data, Codec: codec})
	if err != nil {
		endLatencyEnvelope(start, m.putLatency)
		return cid.Cid{}, err
	}

	if err := m.kv.Put(ctx, blockKey(c), data); err != nil {
		endLatencyEnvelope(start, m.putLatency)
		return cid.Cid{}, fmt.Errorf("failed to store block: %w", err)
	}

	endLatencyEnvelope(start, m.putLatency)
	m.stats.RecordPutBytes(len(data))
	return c, nil
}

// PutBatch stores a batch of blocks in mock storage.
func (m *CAS) PutBatch(ctx context.Context, blocks []cas.Block) ([]cas.PutResult, error) {
	if len(blocks) == 0 {
		return []cas.PutResult{}, nil
	}
	for range blocks {
		m.stats.RecordPutCall()
	}
	start := startLatencyEnvelope()

	results := make([]cas.PutResult, len(blocks))
	for i, block := range blocks {
		blockCID, err := cas.CIDForBlock(block)
		if err != nil {
			endLatencyEnvelope(start, m.putLatency)
			return nil, err
		}
		exists, err := m.kv.Has(ctx, blockKey(blockCID))
		if err != nil {
			endLatencyEnvelope(start, m.putLatency)
			return nil, fmt.Errorf("failed to check block: %w", err)
		}
		status := cas.PutStatusStored
		if exists {
			status = cas.PutStatusAlreadyPresent
		}
		if !exists {
			if err := m.kv.Put(ctx, blockKey(blockCID), block.Data); err != nil {
				endLatencyEnvelope(start, m.putLatency)
				return nil, fmt.Errorf("failed to store block: %w", err)
			}
		}
		m.stats.RecordPutBytes(len(block.Data))
		results[i] = cas.PutResult{CID: blockCID, Status: status}
	}

	endLatencyEnvelope(start, m.putLatency)
	return results, nil
}

// Has checks if a block exists in mock storage.
func (m *CAS) Has(ctx context.Context, c cid.Cid) (bool, error) {
	m.stats.RecordHasCall()
	start := startLatencyEnvelope()

	exists, err := m.kv.Has(ctx, blockKey(c))

	endLatencyEnvelope(start, m.hasLatency)
	if err != nil {
		return false, fmt.Errorf("failed to check block: %w", err)
	}
	return exists, nil
}

// HasBatch checks if each block exists in mock storage.
func (m *CAS) HasBatch(ctx context.Context, cids []cid.Cid) ([]bool, error) {
	if len(cids) == 0 {
		return []bool{}, nil
	}
	for range cids {
		m.stats.RecordHasCall()
	}
	start := startLatencyEnvelope()

	results := make([]bool, len(cids))
	for i, c := range cids {
		exists, err := m.kv.Has(ctx, blockKey(c))
		if err != nil {
			endLatencyEnvelope(start, m.hasLatency)
			return nil, fmt.Errorf("failed to check block: %w", err)
		}
		results[i] = exists
	}

	endLatencyEnvelope(start, m.hasLatency)
	return results, nil
}

// AddBlock adds a pre-existing block to mock storage without latency.
func (m *CAS) AddBlock(c cid.Cid, data []byte) {
	_ = m.kv.Put(context.Background(), blockKey(c), data)
}

// SnapshotStats returns the current mock CAS counters.
func (m *CAS) SnapshotStats() CASStats {
	return m.stats.Snapshot()
}

// ResetStats clears mock CAS counters.
func (m *CAS) ResetStats() {
	m.stats.Reset()
}

// Ensure CAS implements cas.Client.
var _ cas.Client = (*CAS)(nil)
var _ cas.BatchReader = (*CAS)(nil)
var _ cas.BatchWriter = (*CAS)(nil)

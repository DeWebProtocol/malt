// Package local provides transport capabilities backed by user-controlled
// local storage. It does not own trusted roots or application policy.
package local

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

const (
	DefaultMaxBlockBytes  int64 = 64 << 20
	DefaultMaxBatchBytes  int64 = 64 << 20
	DefaultMaxBatchBlocks       = 4096
)

// Options configures one durable local CAS. Directory is the store boundary;
// blocks are written below it using immutable CID-derived names.
type Options struct {
	Directory      string
	MaxBlockBytes  int64
	MaxBatchBytes  int64
	MaxBatchBlocks int
}

// CAS is an owner-private, durable, content-addressed local transport. Local
// disk contents are treated as untrusted: Get and Has re-hash the complete
// bounded body before reporting success.
type CAS struct {
	directory      string
	blocks         string
	platform       blockStore
	maxBlockBytes  int64
	maxBatchBytes  int64
	maxBatchBlocks int
	mu             sync.Mutex
	terminal       atomic.Bool
}

var errCASClosed = errors.New("local CAS is closed")

// Open creates or opens a durable local CAS.
func Open(options Options) (*CAS, error) {
	directory := filepath.Clean(options.Directory)
	if options.Directory == "" || directory == "." {
		return nil, fmt.Errorf("local CAS directory is required")
	}
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("resolve local CAS directory: %w", err)
	}
	if absolute == filepath.VolumeName(absolute)+string(filepath.Separator) {
		return nil, fmt.Errorf("local CAS directory cannot be a filesystem root")
	}
	maxBlockBytes := options.MaxBlockBytes
	if maxBlockBytes == 0 {
		maxBlockBytes = DefaultMaxBlockBytes
	}
	maxBatchBytes := options.MaxBatchBytes
	if maxBatchBytes == 0 {
		maxBatchBytes = DefaultMaxBatchBytes
	}
	maxBatchBlocks := options.MaxBatchBlocks
	if maxBatchBlocks == 0 {
		maxBatchBlocks = DefaultMaxBatchBlocks
	}
	if maxBlockBytes < 1 || maxBlockBytes == math.MaxInt64 {
		return nil, fmt.Errorf("local CAS max block bytes must be between 1 and %d", int64(math.MaxInt64-1))
	}
	if maxBatchBytes < 1 || maxBatchBytes == math.MaxInt64 {
		return nil, fmt.Errorf("local CAS max batch bytes must be between 1 and %d", int64(math.MaxInt64-1))
	}
	if maxBatchBlocks < 1 {
		return nil, fmt.Errorf("local CAS max batch blocks must be positive")
	}
	platform, err := openPlatformStore(absolute)
	if err != nil {
		return nil, err
	}
	store := &CAS{
		directory: absolute, blocks: filepath.Join(absolute, "blocks"), platform: platform,
		maxBlockBytes: maxBlockBytes, maxBatchBytes: maxBatchBytes, maxBatchBlocks: maxBatchBlocks,
	}
	runtime.SetFinalizer(store, func(store *CAS) { _ = store.Close() })
	return store, nil
}

// Directory returns the absolute local store boundary.
func (s *CAS) Directory() string {
	if s == nil {
		return ""
	}
	return s.directory
}

// Close releases the platform handles that pin the store boundary. Callers
// must quiesce concurrent operations before closing the CAS. Close is
// idempotent; the first attempt terminally disables I/O even if cleanup reports
// an error, and later calls retry cleanup ownership only. Process-lifetime
// runtimes may leave the store open until exit.
func (s *CAS) Close() error {
	if s == nil {
		return nil
	}
	// The first cleanup attempt terminally closes the service for I/O even if a
	// platform handle reports a diagnostic. The retained platform owner below is
	// cleanup-only and may be retried; its os.File handles are never reused.
	s.terminal.Store(true)
	s.mu.Lock()
	defer s.mu.Unlock()
	runtime.SetFinalizer(s, nil)
	if s.platform == nil {
		return nil
	}
	if err := s.platform.close(); err != nil {
		// Retain the platform owner after an unconfirmed close. A later Close
		// retries only components whose successful release was not observed.
		return err
	}
	s.platform = nil
	return nil
}

func (s *CAS) Put(ctx context.Context, data []byte) (cid.Cid, error) {
	return s.PutWithCodec(ctx, data, cid.Raw)
}

func (s *CAS) PutWithCodec(ctx context.Context, data []byte, codec uint64) (cid.Cid, error) {
	if err := s.ready(); err != nil {
		return cid.Undef, err
	}
	if err := ctx.Err(); err != nil {
		return cid.Undef, err
	}
	if int64(len(data)) > s.maxBlockBytes {
		return cid.Undef, fmt.Errorf("local CAS block exceeds %d bytes", s.maxBlockBytes)
	}
	key, err := clientcas.CIDForBlock(transportcap.Block{Data: data, Codec: codec})
	if err != nil {
		return cid.Undef, fmt.Errorf("compute local CAS CID: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err = s.putLocked(ctx, key, data)
	if err != nil {
		return cid.Undef, err
	}
	return key, nil
}

func (s *CAS) Get(ctx context.Context, key cid.Cid) ([]byte, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	return s.readBlock(ctx, key)
}

func (s *CAS) Has(ctx context.Context, key cid.Cid) (bool, error) {
	if err := s.ready(); err != nil {
		return false, err
	}
	_, err := s.readBlock(ctx, key)
	if errors.Is(err, transportcap.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *CAS) PutBatch(ctx context.Context, blocks []transportcap.Block) ([]transportcap.PutResult, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		return []transportcap.PutResult{}, nil
	}
	if len(blocks) > s.maxBatchBlocks {
		return nil, fmt.Errorf("local CAS batch exceeds %d blocks", s.maxBatchBlocks)
	}
	keys := make([]cid.Cid, len(blocks))
	total := int64(0)
	for index, block := range blocks {
		if int64(len(block.Data)) > s.maxBlockBytes || total > s.maxBatchBytes-int64(len(block.Data)) {
			return nil, fmt.Errorf("local CAS batch exceeds %d bytes", s.maxBatchBytes)
		}
		total += int64(len(block.Data))
		key, err := clientcas.CIDForBlock(block)
		if err != nil {
			return nil, fmt.Errorf("compute local CAS batch CID %d: %w", index, err)
		}
		keys[index] = key
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	results := make([]transportcap.PutResult, len(blocks))
	seen := make(map[string]struct{}, len(blocks))
	for index, block := range blocks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := keys[index]
		if _, duplicate := seen[key.String()]; duplicate {
			results[index] = transportcap.PutResult{CID: key, Status: transportcap.PutStatusDuplicateInRequest}
			continue
		}
		seen[key.String()] = struct{}{}
		status, err := s.putLocked(ctx, key, block.Data)
		if err != nil {
			return nil, fmt.Errorf("store local CAS batch block %d: %w", index, err)
		}
		results[index] = transportcap.PutResult{CID: key, Status: status}
	}
	return results, nil
}

func (s *CAS) HasBatch(ctx context.Context, keys []cid.Cid) ([]bool, error) {
	if err := s.ready(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return []bool{}, nil
	}
	if len(keys) > s.maxBatchBlocks {
		return nil, fmt.Errorf("local CAS has batch exceeds %d CIDs", s.maxBatchBlocks)
	}
	for index, key := range keys {
		if !key.Defined() {
			return nil, fmt.Errorf("%w: local CAS batch CID %d is undefined", transportcap.ErrCorruptedBlock, index)
		}
	}
	result := make([]bool, len(keys))
	for index, key := range keys {
		present, err := s.Has(ctx, key)
		if err != nil {
			return nil, err
		}
		result[index] = present
	}
	return result, nil
}

func (s *CAS) putLocked(ctx context.Context, key cid.Cid, data []byte) (transportcap.PutStatus, error) {
	shard, name := blockIdentity(key)
	if _, err := s.readBlock(ctx, key); err == nil {
		if err := s.platform.ensureDurable(ctx, shard); err != nil {
			return "", fmt.Errorf("confirm existing local CAS block durability: %w", err)
		}
		return transportcap.PutStatusAlreadyPresent, nil
	} else if !errors.Is(err, transportcap.ErrNotFound) && !errors.Is(err, transportcap.ErrCorruptedBlock) {
		return "", err
	}

	if err := s.platform.writeBlock(ctx, shard, name, data); err != nil {
		return "", err
	}
	if _, err := s.readBlock(ctx, key); err != nil {
		return "", fmt.Errorf("verify installed local CAS block: %w", err)
	}
	if err := s.platform.ensureDurable(ctx, shard); err != nil {
		return "", fmt.Errorf("confirm installed local CAS block durability: %w", err)
	}
	return transportcap.PutStatusStored, nil
}

func (s *CAS) readBlock(ctx context.Context, key cid.Cid) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !key.Defined() {
		return nil, fmt.Errorf("%w: local CAS key is undefined", transportcap.ErrCorruptedBlock)
	}
	shard, name := blockIdentity(key)
	data, err := s.platform.readBlock(ctx, shard, name, s.maxBlockBytes)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	got, err := key.Prefix().Sum(data)
	if err != nil || !got.Equals(key) {
		return nil, fmt.Errorf("%w: local CAS body does not match %s", transportcap.ErrCorruptedBlock, key)
	}
	return data, nil
}

func (s *CAS) blockPath(key cid.Cid) string {
	shard, name := blockIdentity(key)
	return filepath.Join(s.blocks, shard, name)
}

func blockIdentity(key cid.Cid) (string, string) {
	decoded, err := mh.Decode(key.Hash())
	var digest []byte
	if err == nil {
		digest = decoded.Digest
	}
	shard := "00"
	if len(digest) != 0 {
		const hexadecimal = "0123456789abcdef"
		shard = string([]byte{hexadecimal[digest[0]>>4], hexadecimal[digest[0]&0x0f]})
	}
	return shard, key.String() + ".block"
}

func (s *CAS) ready() error {
	if s == nil {
		return fmt.Errorf("local CAS is nil or uninitialized")
	}
	if s.terminal.Load() {
		return errCASClosed
	}
	if s.directory == "" || s.blocks == "" || s.platform == nil || s.maxBlockBytes < 1 || s.maxBatchBytes < 1 || s.maxBatchBlocks < 1 {
		return fmt.Errorf("local CAS is nil or uninitialized")
	}
	return nil
}

var _ transportcap.BatchCAS = (*CAS)(nil)

type blockStore interface {
	readBlock(context.Context, string, string, int64) ([]byte, error)
	writeBlock(context.Context, string, string, []byte) error
	ensureDurable(context.Context, string) error
	close() error
}

// Package hamt implements the Resolver interface for HAMT (Hash Array Mapped Trie).
// HAMT is an efficient dictionary data structure used in IPFS/IPLD for large-scale
// key-value stores with O(log n) lookup complexity.
package hamt

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/cas"
	"github.com/dewebprotocol/malt/core/types/evidence"
	cid "github.com/ipfs/go-cid"
)

// Default configuration values.
const (
	// DefaultBitWidth is the default number of bits per level (5 → 32-way branching).
	DefaultBitWidth = 5

	// DefaultMaxDepth is the maximum traversal depth to prevent infinite loops.
	DefaultMaxDepth = 256
)

// Config holds HAMT configuration.
type Config struct {
	// BitWidth is the number of bits used per level (default 5 → 32-way branching).
	BitWidth int

	// HashFunc computes the hash of a key for routing.
	// Default is murmur3 (compatible with IPFS HAMT).
	HashFunc func([]byte) []byte

	// MaxDepth is the safety limit for traversal depth.
	MaxDepth int
}

// Option is a functional option for configuring the resolver.
type Option func(*Config)

// WithBitWidth sets the bit width for HAMT routing.
func WithBitWidth(w int) Option {
	return func(c *Config) {
		c.BitWidth = w
	}
}

// WithHashFunc sets the hash function for key routing.
func WithHashFunc(f func([]byte) []byte) Option {
	return func(c *Config) {
		c.HashFunc = f
	}
}

// WithMaxDepth sets the maximum traversal depth.
func WithMaxDepth(d int) Option {
	return func(c *Config) {
		c.MaxDepth = d
	}
}

// Resolver implements resolver.Resolver for HAMT data structures.
type Resolver struct {
	cas    cas.Client
	config Config
}

// NewResolver creates a new HAMT resolver with default configuration.
func NewResolver(c cas.Client, opts ...Option) *Resolver {
	config := Config{
		BitWidth: DefaultBitWidth,
		HashFunc: murmur3Hash,
		MaxDepth: DefaultMaxDepth,
	}

	for _, opt := range opts {
		opt(&config)
	}

	return &Resolver{
		cas:    c,
		config: config,
	}
}

// Resolve resolves a path through the HAMT.
// The path is treated as a key to look up in the HAMT.
// Returns the matched path (key), the target CID (value), and evidence.
func (r *Resolver) Resolve(root cid.Cid, path string) (matchedPath string, target cid.Cid, ev evidence.Evidence, err error) {
	if !root.Defined() {
		return "", cid.Cid{}, nil, fmt.Errorf("root is not defined")
	}

	if r.cas == nil {
		return "", cid.Cid{}, nil, fmt.Errorf("CAS client is nil")
	}

	// Empty path returns the root
	if path == "" {
		return "", root, nil, nil
	}

	// Resolve the key in HAMT
	valueCID, proof, err := r.resolveKey(root, path)
	if err != nil {
		return "", cid.Cid{}, nil, err
	}

	// Build evidence from the traversal proof
	ev = evidence.NewHAMTEvidence(proof)

	return path, valueCID, ev, nil
}

// Verify verifies the HAMT evidence.
func (r *Resolver) Verify(root cid.Cid, path string, target cid.Cid, ev evidence.Evidence) (bool, error) {
	if ev == nil {
		return false, fmt.Errorf("evidence is nil")
	}

	_, ok := ev.(*evidence.HAMTEvidence)
	if !ok {
		return false, fmt.Errorf("expected HAMTEvidence, got %T", ev)
	}

	// Re-resolve to verify
	actualCID, _, err := r.resolveKey(root, path)
	if err != nil {
		return false, err
	}

	return actualCID.Equals(target), nil
}

// resolveKey finds a key in the HAMT and returns its value CID and proof.
func (r *Resolver) resolveKey(root cid.Cid, key string) (cid.Cid, []byte, error) {
	// Hash the key for routing
	keyHash := r.config.HashFunc([]byte(key))

	current := root
	bitPos := 0
	proof := make([]byte, 0)

	for depth := 0; depth < r.config.MaxDepth; depth++ {
		// Fetch the node from CAS
		block, err := r.cas.Get(context.Background(), current)
		if err != nil {
			return cid.Cid{}, nil, fmt.Errorf("failed to fetch HAMT node %s: %w", current, err)
		}

		// Parse the HAMT node
		node, err := ParseNode(block)
		if err != nil {
			return cid.Cid{}, nil, fmt.Errorf("failed to parse HAMT node: %w", err)
		}

		// Get the bucket index from hash
		idx := r.extractBits(keyHash, bitPos)

		// Check if the bit is set in the bitfield
		if !isBitSet(node.Bitfield, idx) {
			return cid.Cid{}, nil, fmt.Errorf("key not found: %s", key)
		}

		// Get the actual array index (count set bits before position)
		arrIdx := countSetBitsBefore(node.Bitfield, idx)

		// Get the entry
		if arrIdx >= len(node.Entries) {
			return cid.Cid{}, nil, fmt.Errorf("invalid HAMT node: array index out of range")
		}

		entry := node.Entries[arrIdx]

		// Add to proof
		proof = append(proof, block...)

		// Check if it's a value (leaf) or link (intermediate)
		if entry.IsValue() {
			// Found the value
			return entry.Value, proof, nil
		}

		// Follow the link to the next level
		if !entry.Link.Defined() {
			return cid.Cid{}, nil, fmt.Errorf("invalid HAMT node: undefined link")
		}

		current = entry.Link
		bitPos += r.config.BitWidth
	}

	return cid.Cid{}, nil, fmt.Errorf("max depth exceeded")
}

// extractBits extracts n bits from the hash at the given position.
func (r *Resolver) extractBits(hash []byte, bitPos int) int {
	// Number of bits to extract
	n := r.config.BitWidth

	// Calculate byte and bit offsets
	bytePos := bitPos / 8
	bitOffset := bitPos % 8

	// Extract bits, handling cross-byte boundaries
	var result int
	bitsRemaining := n
	currentByte := bytePos
	currentBitOffset := bitOffset

	for bitsRemaining > 0 {
		if currentByte >= len(hash) {
			// Wrap around if we exceed hash length
			currentByte = 0
		}

		// How many bits we can take from current byte
		bitsAvailable := 8 - currentBitOffset
		bitsToTake := bitsRemaining
		if bitsToTake > bitsAvailable {
			bitsToTake = bitsAvailable
		}

		// Extract bits
		mask := byte(0xFF >> currentBitOffset)
		mask &= byte(0xFF << (8 - currentBitOffset - bitsToTake))
		bits := int((hash[currentByte] & mask) >> (8 - currentBitOffset - bitsToTake))

		result = (result << bitsToTake) | bits

		bitsRemaining -= bitsToTake
		currentByte++
		currentBitOffset = 0
	}

	return result
}
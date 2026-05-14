// Package bloom provides Bloom Filter implementations for ArcTable.
// Bloom filters provide fast negative membership tests.
// They are optional optimization hooks behind ArcTable implementations, not
// semantic state or part of the root-centric trust boundary.
//
// Bloom Filter properties:
//   - False negative: If Test returns false, the item is definitely not in the set
//   - False positive: If Test returns true, the item might be in the set
package bloom

import "encoding"

// BloomFilter is an interface for bloom filter operations.
type BloomFilter interface {
	// Add adds an item to the bloom filter.
	Add(item []byte)

	// Test checks if an item might be in the set.
	// Returns false if definitely not in the set.
	// Returns true if might be in the set (could be false positive).
	Test(item []byte) bool

	// Clear resets the bloom filter.
	Clear()

	// Size returns the number of items added.
	Size() uint64

	// M returns the size of the bitset.
	M() uint

	// K returns the number of hash functions.
	K() uint

	// MarshalBinary serializes the bloom filter to bytes.
	encoding.BinaryMarshaler

	// UnmarshalBinary deserializes the bloom filter from bytes.
	encoding.BinaryUnmarshaler
}

// Default bloom filter parameters.
const (
	DefaultExpectedItems     = 10000
	DefaultFalsePositiveRate = 0.01
)

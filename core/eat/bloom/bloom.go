// Package bloom provides Bloom Filter implementations for EAT.
// Bloom filters provide fast negative membership tests.
//
// Bloom Filter properties:
//   - False negative: If Test returns false, the item is definitely not in the set
//   - False positive: If Test returns true, the item might be in the set
package bloom

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
}

// Default bloom filter parameters.
const (
	DefaultExpectedItems     = 10000
	DefaultFalsePositiveRate = 0.01
)
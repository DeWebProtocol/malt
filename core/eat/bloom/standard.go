// Package bloom provides a standard Bloom Filter implementation.
// This is a simple implementation using murmur3 hashing and bitset.
package bloom

import (
	"bytes"
	"encoding/gob"
	"math"

	"github.com/bits-and-blooms/bitset"
	"github.com/spaolacci/murmur3"
)

// StandardBloom is a standard Bloom Filter implementation.
type StandardBloom struct {
	bitset   *bitset.BitSet
	k        uint   // number of hash functions
	m        uint   // size of bitset
	n        uint64 // number of items added
	hashSeed uint32 // seed for murmur3
}

// NewStandardBloom creates a new StandardBloom filter.
// expectedItems: expected number of items to add
// falsePositiveRate: desired false positive rate (e.g., 0.01 for 1%)
func NewStandardBloom(expectedItems int, falsePositiveRate float64) *StandardBloom {
	// Calculate optimal m and k using the formulas:
	// m = -n * ln(p) / (ln(2)^2)
	// k = m / n * ln(2)

	n := float64(expectedItems)
	p := falsePositiveRate

	m := uint(math.Ceil(-n * math.Log(p) / math.Pow(math.Log(2), 2)))
	k := uint(math.Ceil(float64(m) / n * math.Log(2)))

	// Ensure minimum reasonable values
	if m < 64 {
		m = 64
	}
	if k < 1 {
		k = 1
	}
	if k > 32 {
		k = 32 // limit hash functions for performance
	}

	return &StandardBloom{
		bitset:   bitset.New(m),
		k:        k,
		m:        m,
		n:        0,
		hashSeed: 0,
	}
}

// NewStandardBloomFromData creates a StandardBloom from serialized data.
func NewStandardBloomFromData(k, m uint, bitsetBytes []byte) (*StandardBloom, error) {
	bs := bitset.New(m)
	buf := bytes.NewBuffer(bitsetBytes)
	dec := gob.NewDecoder(buf)
	if err := dec.Decode(bs); err != nil {
		return nil, err
	}

	return &StandardBloom{
		bitset:   bs,
		k:        k,
		m:        m,
		n:        0,
		hashSeed: 0,
	}, nil
}

// K returns the number of hash functions.
func (b *StandardBloom) K() uint {
	return b.k
}

// M returns the size of the bitset.
func (b *StandardBloom) M() uint {
	return b.m
}

// Bitset returns the serialized bitset.
func (b *StandardBloom) Bitset() []byte {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(b.bitset); err != nil {
		return nil
	}
	return buf.Bytes()
}

// Add adds an item to the bloom filter.
func (b *StandardBloom) Add(item []byte) {
	h1, h2 := b.hash(item)
	for i := uint(0); i < b.k; i++ {
		pos := b.position(h1, h2, i)
		b.bitset.Set(pos)
	}
	b.n++
}

// Test checks if an item might be in the set.
func (b *StandardBloom) Test(item []byte) bool {
	h1, h2 := b.hash(item)
	for i := uint(0); i < b.k; i++ {
		pos := b.position(h1, h2, i)
		if !b.bitset.Test(pos) {
			return false
		}
	}
	return true
}

// Clear resets the bloom filter.
func (b *StandardBloom) Clear() {
	b.bitset.ClearAll()
	b.n = 0
}

// Size returns the number of items added.
func (b *StandardBloom) Size() uint64 {
	return b.n
}

// hash computes two hash values using murmur3.
// These are combined to generate k hash positions using double hashing.
func (b *StandardBloom) hash(item []byte) (uint32, uint32) {
	h1 := murmur3.Sum32WithSeed(item, b.hashSeed)
	h2 := murmur3.Sum32WithSeed(item, b.hashSeed+1)
	return h1, h2
}

// position computes the bit position for hash function i.
// Uses the double hashing technique: h(i) = (h1 + i * h2) mod m
func (b *StandardBloom) position(h1, h2 uint32, i uint) uint {
	pos := (uint(h1) + i*uint(h2)) % b.m
	return pos
}

// EstimatedFalsePositiveRate returns the estimated false positive rate
// based on current number of items.
func (b *StandardBloom) EstimatedFalsePositiveRate() float64 {
	if b.n == 0 {
		return 0
	}
	// p = (1 - e^(-kn/m))^k
	n := float64(b.n)
	k := float64(b.k)
	m := float64(b.m)
	return math.Pow(1-math.Exp(-k*n/m), k)
}

// Capacity returns the expected capacity.
func (b *StandardBloom) Capacity() uint64 {
	return uint64(float64(b.m) * math.Log(2) / float64(b.k))
}
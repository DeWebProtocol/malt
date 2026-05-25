package hamt

import (
	"hash/fnv"

	"github.com/spaolacci/murmur3"
)

// murmur3Hash computes a murmur3 hash of the input bytes.
// This is the default hash function used by IPFS HAMT.
func murmur3Hash(data []byte) []byte {
	h := murmur3.New64()
	h.Write(data)
	return h.Sum(nil)
}

// fnvHash computes an FNV-1a hash of the input bytes.
// This is an alternative hash function for HAMT.
func fnvHash(data []byte) []byte {
	h := fnv.New128a()
	h.Write(data)
	return h.Sum(nil)
}

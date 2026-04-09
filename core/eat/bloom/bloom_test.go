// Package bloom_test tests the Bloom Filter implementation.
package bloom_test

import (
	"testing"

	"github.com/dewebprotocol/malt/core/eat/bloom"
)

func TestStandardBloomBasic(t *testing.T) {
	bf := bloom.NewStandardBloom(100, 0.01)

	// Add some items
	items := []string{"apple", "banana", "cherry", "date", "elderberry"}
	for _, item := range items {
		bf.Add([]byte(item))
	}

	// Test items that were added
	for _, item := range items {
		if !bf.Test([]byte(item)) {
			t.Errorf("expected true for %s", item)
		}
	}

	// Test items that weren't added
	notItems := []string{"fig", "grape", "honeydew"}
	falsePositives := 0
	for _, item := range notItems {
		if bf.Test([]byte(item)) {
			falsePositives++
		}
	}

	// False positive rate should be close to expected
	fpr := float64(falsePositives) / float64(len(notItems))
	if fpr > 0.1 { // allow 10% tolerance
		t.Errorf("false positive rate too high: %f", fpr)
	}

	t.Logf("False positive rate: %f (expected ~0.01)", fpr)
}

func TestStandardBloomClear(t *testing.T) {
	bf := bloom.NewStandardBloom(100, 0.01)

	bf.Add([]byte("test"))
	if !bf.Test([]byte("test")) {
		t.Error("expected true after add")
	}

	bf.Clear()
	if bf.Test([]byte("test")) {
		t.Error("expected false after clear")
	}
}

func TestStandardBloomSize(t *testing.T) {
	bf := bloom.NewStandardBloom(100, 0.01)

	if bf.Size() != 0 {
		t.Error("expected size 0")
	}

	bf.Add([]byte("a"))
	bf.Add([]byte("b"))
	bf.Add([]byte("c"))

	if bf.Size() != 3 {
		t.Errorf("expected size 3, got %d", bf.Size())
	}
}

func TestStandardBloomSerialization(t *testing.T) {
	bf := bloom.NewStandardBloom(100, 0.01)

	// Add some items
	items := []string{"apple", "banana", "cherry"}
	for _, item := range items {
		bf.Add([]byte(item))
	}

	// Serialize
	bitsetBytes := bf.Bitset()
	if bitsetBytes == nil {
		t.Fatal("bitset serialization failed")
	}

	// Deserialize
	bf2, err := bloom.NewStandardBloomFromData(bf.K(), bf.M(), bitsetBytes)
	if err != nil {
		t.Fatalf("deserialization failed: %v", err)
	}

	// Test that deserialized filter works the same
	for _, item := range items {
		if !bf2.Test([]byte(item)) {
			t.Errorf("expected true for %s after deserialization", item)
		}
	}
}
// Package structure defines the public structural semantics layer for MALT.
package structure

// Proof is an opaque proof payload returned by a structural semantic.
// Concrete implementations define the exact encoding.
type Proof []byte

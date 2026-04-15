// Package sce defines the Structure Commitment Engine.
// SCE coordinates arc set management and delegates to commitment schemes.

package sce

import "errors"

// Sentinel errors for SCE operations.
var (
	// ErrPathNotFound is returned when a path does not exist in the arc set.
	ErrPathNotFound = errors.New("path not found in arc set")

	// ErrNilArcSet is returned when a nil arc set is passed to Commit or other operations.
	ErrNilArcSet = errors.New("arc set is nil")
)

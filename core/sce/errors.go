// Package sce defines the Structure Commitment Engine.
// SCE coordinates arc set management and delegates to commitment schemes.

package sce

import "errors"

// Sentinel errors for SCE operations.
var (
	// ErrSessionNotFound is returned when a commitment session cannot be found for a given root.
	// This typically means the root was not created by this SCE instance or the session was lost.
	ErrSessionNotFound = errors.New("commitment session not found")

	// ErrPathNotFound is returned when a path does not exist in the arc set or session.
	ErrPathNotFound = errors.New("path not found in arc set")

	// ErrNilArcSet is returned when a nil arc set is passed to Commit or other operations.
	ErrNilArcSet = errors.New("arc set is nil")
)

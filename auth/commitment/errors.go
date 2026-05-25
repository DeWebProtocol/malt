// Package commitment defines abstract interfaces for cryptographic commitment schemes.
package commitment

import "errors"

// Sentinel errors for commitment operations.
var (
	// ErrInvalidProof is returned when a cryptographic proof fails verification.
	ErrInvalidProof = errors.New("invalid proof")

	// ErrInvalidCommitment is returned when a commitment value is malformed or invalid.
	ErrInvalidCommitment = errors.New("invalid commitment")

	// ErrArcSetTooSmall is returned when the arc set has insufficient entries for the operation.
	ErrArcSetTooSmall = errors.New("arc set too small")
)

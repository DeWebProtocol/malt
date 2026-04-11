// Package resolver implements hybrid resolution with prefix consumption.

package resolver

import "errors"

// Sentinel errors for resolver operations.
var (
	// ErrUndefinedRoot is returned when the root CID is undefined.
	ErrUndefinedRoot = errors.New("root is not defined")

	// ErrResolutionFailed is returned when a resolution step fails.
	ErrResolutionFailed = errors.New("resolution failed")

	// ErrTranscriptNil is returned when a nil transcript is passed for verification.
	ErrTranscriptNil = errors.New("transcript is nil")

	// ErrUnknownEvidenceKind is returned when an evidence kind is not recognized.
	ErrUnknownEvidenceKind = errors.New("unknown evidence kind")

	// ErrStepExecutorNotAvailable is returned when no step executor is available for an evidence kind.
	ErrStepExecutorNotAvailable = errors.New("step executor not available")
)

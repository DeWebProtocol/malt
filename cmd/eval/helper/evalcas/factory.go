// Package evalcas provides eval-specific CAS instances with precise, jitter-free
// latency. The main mock CAS (storage/cas/mock) uses realistic IPFS latency
// defaults with random jitter; this package provides deterministic latency for
// benchmark measurements.
package evalcas

import (
	"time"

	casmock "github.com/dewebprotocol/malt/storage/cas/mock"
)

// NewWithLatency creates a mock CAS with fixed latency (no jitter).
// Get/Has/Put all use the same latency for uniform simulation.
func NewWithLatency(d time.Duration) *casmock.CAS {
	return casmock.NewCAS(
		casmock.WithGetLatency(d),
		casmock.WithPutLatency(d),
		casmock.WithHasLatency(d),
		casmock.WithJitter(0),
	)
}

// NewNoLatency creates a mock CAS with zero latency.
func NewNoLatency() *casmock.CAS {
	return casmock.NewCAS(casmock.WithoutLatency())
}

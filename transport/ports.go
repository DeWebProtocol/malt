package transport

import (
	"context"

	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
)

// Diagnostics exposes operator measurements only. It is never part of a
// client trust decision.
type Diagnostics interface {
	Health(context.Context) (*HealthResponse, error)
	Metrics(context.Context) (*MetricsSnapshot, error)
	MetricsWithStorage(context.Context) (*MetricsSnapshot, error)
}

// MerkleDAGProfile exposes only the two fixed compatibility routes. It does
// not permit application packages to select arbitrary gateway paths.
type MerkleDAGProfile interface {
	PostMerkleDAGResolve(context.Context, []byte) ([]byte, error)
	PostMerkleDAGRead(context.Context, []byte) ([]byte, error)
}

var (
	_ transportcap.Authentication       = (*Client)(nil)
	_ transportcap.AuthenticationWriter = (*Client)(nil)
	_ transportcap.AuthenticationBatch  = (*Client)(nil)
	_ transportcap.CAS                  = (*Client)(nil)
	_ transportcap.BatchCAS             = (*Client)(nil)
	_ Diagnostics                       = (*Client)(nil)
	_ MerkleDAGProfile                  = (*Client)(nil)
)

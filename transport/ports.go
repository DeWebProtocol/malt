package transport

import (
	"context"

	"github.com/dewebprotocol/malt-client/nodeapi"
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
	_ nodeapi.Authentication       = (*Client)(nil)
	_ nodeapi.AuthenticationWriter = (*Client)(nil)
	_ nodeapi.AuthenticationBatch  = (*Client)(nil)
	_ nodeapi.CAS                  = (*Client)(nil)
	_ nodeapi.BatchCAS             = (*Client)(nil)
	_ Diagnostics                  = (*Client)(nil)
	_ MerkleDAGProfile             = (*Client)(nil)
)

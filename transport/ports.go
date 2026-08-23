package transport

import (
	"context"

	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
	"github.com/dewebprotocol/malt-core/mutation"
	"github.com/dewebprotocol/malt-core/protocol"
	cid "github.com/ipfs/go-cid"
)

// Native is the untrusted transport for MALT resolve/read contracts. A caller
// must verify every returned result against the original caller-selected root.
type Native = transportcap.Native

// GatewayMutations is the compatibility HTTP-DTO writer surface. New
// application code should depend on capability.Mutations (also exported here
// as SemanticMutations).
type GatewayMutations interface {
	ApplyRootSemanticMutation(context.Context, string, *SemanticMutationRequest) (*SemanticMutationResponse, error)
	CreateRootStructure(context.Context, map[string]string) (*CreateStructureResponse, error)
}

// Deprecated: use SemanticMutations.
type Mutations = GatewayMutations

// CAS is the immutable byte transport. Implementations bind response bytes to
// requested or returned CIDs before exposing them.
type CAS = transportcap.CAS
type BatchCAS = transportcap.BatchCAS

// SemanticMutations and DatasetBranch are transport-neutral capability aliases
// implemented by the Gateway HTTP Client and future local/peer transports.
type SemanticMutations = transportcap.Mutations
type DatasetBranch = transportcap.DatasetBranch

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

// ClientRoot is the untrusted stateful-writer transport. Implementations
// return complete state candidates and exact durability receipts; callers
// independently verify both and keep trust promotion separate.
type ClientRoot interface {
	FetchUpdateView(context.Context, cid.Cid, *protocol.UpdateViewBounds) (*UpdateViewResponse, error)
	SubmitClientRoot(context.Context, mutation.ClientRootBundle) (*ClientRootResponse, error)
}

var (
	_ Native           = (*Client)(nil)
	_ GatewayMutations = (*Client)(nil)
	_ CAS              = (*Client)(nil)
	_ BatchCAS         = (*Client)(nil)
	_ Diagnostics      = (*Client)(nil)
	_ MerkleDAGProfile = (*Client)(nil)
	_ ClientRoot       = (*Client)(nil)
)

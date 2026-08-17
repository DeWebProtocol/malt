// Package capability defines transport-neutral, untrusted data-access
// capabilities consumed by the MALT local runtime. Implementations may use a
// Gateway, a peer, local storage, or a hybrid policy; none of these interfaces
// can mutate local trust state.
package capability

import (
	"context"

	"github.com/dewebprotocol/malt-core/mutation"
	"github.com/dewebprotocol/malt-core/protocol"
	cid "github.com/ipfs/go-cid"
)

// Native resolves and reads MALT objects. Results remain untrusted until the
// caller verifies them against its selected root.
type Native interface {
	Resolve(context.Context, protocol.ResolveRequest) (*protocol.ResolveResult, error)
	Read(context.Context, protocol.ReadRequest) (*protocol.ReadResult, error)
}

// CAS transfers immutable bytes. Get implementations must bind returned bytes
// to the requested CID; callers still enforce application-specific bindings.
type CAS interface {
	Put(context.Context, []byte) (cid.Cid, error)
	PutWithCodec(context.Context, []byte, uint64) (cid.Cid, error)
	Get(context.Context, cid.Cid) ([]byte, error)
	Has(context.Context, cid.Cid) (bool, error)
}

// MutationResult is an untrusted candidate produced by executing a canonical
// semantic mutation. It carries no accepted/trusted state.
type MutationResult struct {
	BaseRoot        cid.Cid
	CandidateRoot   cid.Cid
	DeltaCount      int
	ArcCount        int
	MALTObjectCount int
	MapCount        int
	ListCount       int
}

// Mutations is the semantic writer capability shared by remote and local
// transports. A successful result is only a candidate for local verification.
type Mutations interface {
	ApplyMutation(context.Context, mutation.SemanticMutation) (MutationResult, error)
	CreateStructureCandidate(context.Context, map[string]string) (cid.Cid, error)
}

// DatasetBinding identifies a transport instance's selected logical dataset
// and branch independently from any URL or network topology.
type DatasetBinding struct {
	DatasetID string
	Branch    string
}

// DatasetBranch observes and updates one bound logical dataset branch. Every
// returned value is untrusted and must be validated against the exact request.
type DatasetBranch interface {
	DatasetBinding() DatasetBinding
	ObserveHead(context.Context) (*ObservedHead, error)
	ApplyCandidate(context.Context, ApplyRequest) (*ApplyResult, error)
}

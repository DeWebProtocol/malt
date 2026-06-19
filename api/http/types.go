// Package httpapi defines the daemon HTTP API payloads shared by server and client.
package httpapi

import (
	"github.com/dewebprotocol/malt/auth/proof/prooflist"
)

// ErrorResponse represents a structured API error.
type ErrorResponse struct {
	Error string `json:"error"`
}

// HealthResponse is returned by the daemon health endpoint.
type HealthResponse struct {
	Status string `json:"status"`
}

// LifecycleIdentityResponse is returned by the local managed-daemon identity
// endpoint.
type LifecycleIdentityResponse struct {
	Status string `json:"status"`
}

// MetricsResponse wraps a node-local metrics snapshot.
type MetricsResponse struct {
	Snapshot MetricsSnapshot `json:"snapshot"`
}

// MetricsSnapshot is the API projection of node-local evaluation counters.
type MetricsSnapshot struct {
	CAS      CASStats      `json:"cas"`
	ArcTable ArcTableStats `json:"arctable"`
	Proof    ProofStats    `json:"proof"`
}

// CASStats is the API projection of CAS operation counters.
type CASStats struct {
	PutCount uint64 `json:"put_count"`
	GetCount uint64 `json:"get_count"`
	HasCount uint64 `json:"has_count"`
	BytesPut uint64 `json:"bytes_put"`
	BytesGet uint64 `json:"bytes_get"`
}

// ArcTableStats is the API projection of ArcTable operation counters.
type ArcTableStats struct {
	GetCount          uint64 `json:"get_count"`
	BatchGetCount     uint64 `json:"batch_get_count"`
	BatchGetPathCount uint64 `json:"batch_get_path_count"`
	UpdateCount       uint64 `json:"update_count"`
	UpdateArcCount    uint64 `json:"update_arc_count"`
	SnapshotCount     uint64 `json:"snapshot_count"`
	SnapshotArcCount  uint64 `json:"snapshot_arc_count"`
	IterateCount      uint64 `json:"iterate_count"`
}

// ProofStats is the API projection of verifier proof byte counters.
type ProofStats struct {
	ProofListCount uint64 `json:"proof_list_count"`
	StepCount      uint64 `json:"step_count"`
	EvidenceBytes  uint64 `json:"evidence_bytes"`
	ProofBytes     uint64 `json:"proof_bytes"`
	TotalBytes     uint64 `json:"total_bytes"`
}

// SemanticMutationRequest materializes a root-relative semantic mutation.
type SemanticMutationRequest struct {
	Deltas []SemanticMutationDelta `json:"deltas"`
}

// SemanticMutationDelta applies coordinate-level changes to one semantic object.
type SemanticMutationDelta struct {
	Object       string                    `json:"object,omitempty"`
	ExpectedRoot string                    `json:"expected_root,omitempty"`
	Kind         string                    `json:"kind"`
	Changes      []SemanticMutationChange  `json:"changes"`
	Commit       *SemanticCommitDescriptor `json:"commit,omitempty"`
}

// SemanticMutationChange is one canonical coordinate transition.
type SemanticMutationChange struct {
	Path   string                  `json:"path,omitempty"`
	Index  *uint64                 `json:"index,omitempty"`
	Before *SemanticMutationTarget `json:"before,omitempty"`
	After  *SemanticMutationTarget `json:"after,omitempty"`
}

// SemanticMutationTarget is a typed mutation target CID.
type SemanticMutationTarget struct {
	Target     string `json:"target"`
	TargetKind string `json:"target_kind,omitempty"`
}

// SemanticCommitDescriptor records the commit profile for a delta.
type SemanticCommitDescriptor struct {
	FixedList *SemanticFixedListCommit `json:"fixed_list,omitempty"`
}

// SemanticFixedListCommit describes a measured fixed-width list commit.
type SemanticFixedListCommit struct {
	TotalSize uint64 `json:"total_size"`
	ChunkSize uint64 `json:"chunk_size"`
}

// SemanticMutationResponse returns a writer mutation receipt.
type SemanticMutationResponse struct {
	BaseRoot        string `json:"base_root"`
	NewRoot         string `json:"new_root"`
	ResultRoot      string `json:"result_root,omitempty"`
	DeltaCount      int    `json:"delta_count"`
	ArcCount        int    `json:"arc_count"`
	MALTObjectCount int    `json:"malt_object_count,omitempty"`
	MapCount        int    `json:"map_count,omitempty"`
	ListCount       int    `json:"list_count,omitempty"`
}

// PathStatResponse is the locked stat contract for content inspection.
type PathStatResponse struct {
	Kind        string   `json:"kind"`              // file|dir
	StorageKind string   `json:"storage_kind"`      // raw|list|map
	Key         string   `json:"key"`               // terminal key CID
	Payload     string   `json:"payload,omitempty"` // directory payload CID when available
	Size        *int64   `json:"size,omitempty"`    // required for files, omitted for directories
	Entries     []string `json:"entries,omitempty"` // directory entries when available
}

// UnixFSWriteResponse returns the result of a UnixFS layout mutation.
type UnixFSWriteResponse struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	OldRoot  string `json:"old_root,omitempty"`
	NewRoot  string `json:"new_root"`
	ArcCount int    `json:"arc_count"`
}

// ResolveResponse returns a resolved target and, by default, verifier-facing
// ProofList evidence.
type ResolveResponse struct {
	Target    string               `json:"target"`
	ProofList *prooflist.ProofList `json:"prooflist,omitempty"`
}

// CreateStructureRequest creates a new structure from an arc set.
type CreateStructureRequest struct {
	Arcs map[string]string `json:"arcs"`
}

// CreateStructureResponse returns the created root.
type CreateStructureResponse struct {
	Root string `json:"root"`
}

// VerifyRequest verifies a ProofList.
type VerifyRequest struct {
	ProofList prooflist.ProofList `json:"prooflist"`
}

// VerifyResponse returns the verification result.
type VerifyResponse struct {
	Valid bool `json:"valid"`
}

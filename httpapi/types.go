// Package httpapi defines the daemon HTTP API payloads shared by server and client.
package httpapi

import (
	"github.com/dewebprotocol/malt/core/metrics"
	"github.com/dewebprotocol/malt/core/types/prooflist"
)

// ErrorResponse represents a structured API error.
type ErrorResponse struct {
	Error string `json:"error"`
}

// HealthResponse is returned by the daemon health endpoint.
type HealthResponse struct {
	Status string `json:"status"`
}

// MetricsResponse wraps a node-local metrics snapshot.
type MetricsResponse struct {
	Snapshot metrics.Snapshot `json:"snapshot"`
}

// SemanticMutationRequest materializes a root-relative semantic mutation.
type SemanticMutationRequest struct {
	Puts []SemanticMutationPut `json:"puts"`
}

// SemanticMutationPut replaces one semantic object's full canonical arc set.
type SemanticMutationPut struct {
	Object  string                  `json:"object,omitempty"`
	Kind    string                  `json:"kind"`
	Entries []SemanticMutationEntry `json:"entries"`
}

// SemanticMutationEntry is one canonical coordinate-to-target binding.
type SemanticMutationEntry struct {
	Path       string  `json:"path,omitempty"`
	Index      *uint64 `json:"index,omitempty"`
	Target     string  `json:"target"`
	TargetKind string  `json:"target_kind,omitempty"`
}

// SemanticMutationResponse returns a gateway materialization receipt.
type SemanticMutationResponse struct {
	BaseRoot        string `json:"base_root"`
	NewRoot         string `json:"new_root"`
	ResultRoot      string `json:"result_root,omitempty"`
	PutCount        int    `json:"put_count"`
	ArcCount        int    `json:"arc_count"`
	MALTObjectCount int    `json:"malt_object_count,omitempty"`
	MapCount        int    `json:"map_count,omitempty"`
	ListCount       int    `json:"list_count,omitempty"`
}

// MapCreateRequest creates a map root inside the current namespace.
type MapCreateRequest struct {
	Bindings map[string]string `json:"bindings"`
}

// MapCreateResponse returns a created map root.
type MapCreateResponse struct {
	Root string `json:"root"`
}

// MapSnapshotResponse returns a map root snapshot.
type MapSnapshotResponse struct {
	Root     string            `json:"root"`
	Bindings map[string]string `json:"bindings"`
}

// MapResolveResponse returns a resolved key under a map root.
type MapResolveResponse struct {
	Key string `json:"key"`
}

// ListCreateRequest creates a list root from ordered chunk CIDs.
type ListCreateRequest struct {
	Chunks    []string `json:"chunks"`
	ChunkSize int      `json:"chunk_size"`
}

// ListStatResponse is the response shape for list create/stat.
type ListStatResponse struct {
	Root       string `json:"root"`
	ChunkCount int    `json:"chunk_count"`
	ChunkSize  int    `json:"chunk_size"`
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

// UnixFSBatchRequest applies a flat UnixFS path-map mutation.
type UnixFSBatchRequest struct {
	BaseRoot string             `json:"base_root,omitempty"`
	Entries  []UnixFSBatchEntry `json:"entries"`
}

// UnixFSBatchEntry binds one query path to a payload CID or chunk list.
type UnixFSBatchEntry struct {
	Path   string   `json:"path"`
	Target string   `json:"target,omitempty"`
	Chunks []string `json:"chunks,omitempty"`
}

// UnixFSBatchResponse returns the result of a flat UnixFS batch write.
type UnixFSBatchResponse struct {
	OldRoot  string `json:"old_root,omitempty"`
	NewRoot  string `json:"new_root"`
	PutCount int    `json:"put_count"`
	ArcCount int    `json:"arc_count"`
}

// StepEvidence is a single transcript step.
type StepEvidence struct {
	Path     string `json:"path"`
	Target   string `json:"target"`
	Evidence string `json:"evidence"`
	Kind     string `json:"kind"`
}

// ResolveResponse returns a resolved target and, by default, verifier-facing
// ProofList evidence.
type ResolveResponse struct {
	Target     string               `json:"target"`
	ProofList  *prooflist.ProofList `json:"prooflist,omitempty"`
	Transcript []StepEvidence       `json:"transcript,omitempty"`
}

// CreateStructureRequest creates a new structure from an arc set.
type CreateStructureRequest struct {
	Arcs map[string]string `json:"arcs"`
}

// CreateStructureResponse returns the created root.
type CreateStructureResponse struct {
	Root string `json:"root"`
}

// SnapshotResponse returns a root snapshot.
type SnapshotResponse struct {
	Root string            `json:"root"`
	Arcs map[string]string `json:"arcs"`
}

// VerifyRequest verifies a ProofList.
type VerifyRequest struct {
	ProofList prooflist.ProofList `json:"prooflist"`
}

// VerifyResponse returns the verification result.
type VerifyResponse struct {
	Valid bool `json:"valid"`
}

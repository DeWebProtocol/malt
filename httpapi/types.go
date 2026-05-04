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
	BaseRoot string `json:"base_root"`
	NewRoot  string `json:"new_root"`
	PutCount int    `json:"put_count"`
	ArcCount int    `json:"arc_count"`
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

// ContentRange describes the HTTP-equivalent byte range metadata for a
// proof-bearing JSON content read.
type ContentRange struct {
	Start         int64  `json:"start"`
	EndExclusive  int64  `json:"end_exclusive"`
	ContentLength int64  `json:"content_length"`
	TotalSize     int64  `json:"total_size"`
	Partial       bool   `json:"partial"`
	StatusCode    int    `json:"status_code"`
	AcceptRanges  string `json:"accept_ranges"`
	ContentRange  string `json:"content_range,omitempty"`
}

// ContentProofResponse returns content bytes with range metadata and the
// verifier-facing ProofList for the same read.
type ContentProofResponse struct {
	Path        string              `json:"path,omitempty"`
	StorageKind string              `json:"storage_kind"`
	Key         string              `json:"key"`
	Content     []byte              `json:"content"`
	Range       ContentRange        `json:"range"`
	ProofList   prooflist.ProofList `json:"prooflist"`
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

// ResolveResponse returns a resolved target plus transcript.
type ResolveResponse struct {
	Target     string         `json:"target"`
	Transcript []StepEvidence `json:"transcript"`
}

// ProofListResponse returns a resolved target plus verifier-facing ProofList.
type ProofListResponse struct {
	Target    string              `json:"target"`
	ProofList prooflist.ProofList `json:"prooflist"`
}

// UpdateRequest updates a single binding.
type UpdateRequest struct {
	Path   string `json:"path,omitempty"`
	Target string `json:"target"`
}

// BatchUpdateRequest updates multiple bindings.
type BatchUpdateRequest struct {
	Updates map[string]string `json:"updates"`
}

// CreateStructureRequest creates a new structure from an arc set.
type CreateStructureRequest struct {
	Arcs map[string]string `json:"arcs"`
}

// WriteUpdateResponse describes a single arc mutation.
type WriteUpdateResponse struct {
	OldRoot   string `json:"old_root"`
	NewRoot   string `json:"new_root"`
	Path      string `json:"path"`
	OldTarget string `json:"old_target"`
	NewTarget string `json:"new_target"`
	Op        string `json:"op"`
}

// WriteBatchResponse describes a batch mutation.
type WriteBatchResponse struct {
	OldRoot string                          `json:"old_root"`
	NewRoot string                          `json:"new_root"`
	PerArc  map[string]*WriteUpdateResponse `json:"per_arc"`
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

// VerifyStep is one verification transcript step.
type VerifyStep struct {
	Path     string `json:"path"`
	Target   string `json:"target"`
	Evidence string `json:"evidence"`
	Kind     string `json:"kind"`
}

// VerifyRequest verifies a transcript under a root.
type VerifyRequest struct {
	Root       string       `json:"root"`
	Transcript []VerifyStep `json:"transcript"`
}

// VerifyResponse returns the verification result.
type VerifyResponse struct {
	Valid bool `json:"valid"`
}

// LineageRecordResponse returns metadata about one root.
type LineageRecordResponse struct {
	Root      string `json:"root"`
	Parent    string `json:"parent"`
	Timestamp string `json:"timestamp"`
	Depth     int    `json:"depth"`
	ArcCount  int    `json:"arc_count"`
}

// LineageListResponse wraps lineage records.
type LineageListResponse struct {
	Records []LineageRecordResponse `json:"records"`
}

// CIDListResponse wraps a list of CIDs.
type CIDListResponse struct {
	Items []string `json:"items"`
}

// CountResponse wraps a count result.
type CountResponse struct {
	Count int `json:"count"`
}

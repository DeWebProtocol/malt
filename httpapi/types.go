// Package httpapi defines the daemon HTTP API payloads shared by server and client.
package httpapi

// ErrorResponse represents a structured API error.
type ErrorResponse struct {
	Error string `json:"error"`
}

// HealthResponse is returned by the daemon health endpoint.
type HealthResponse struct {
	Status string `json:"status"`
}

// Graph describes graph metadata in daemon responses.
type Graph struct {
	ID        string `json:"id"`
	Root      string `json:"root,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	ArcCount  int    `json:"arc_count"`
	Backend   string `json:"backend"`
	EATType   string `json:"eat_type"`
	State     string `json:"state"`
}

// GraphCreateRequest creates a managed graph.
type GraphCreateRequest struct {
	ID      string `json:"id"`
	Backend string `json:"backend,omitempty"`
}

// GraphResponse wraps a single graph.
type GraphResponse struct {
	Graph *Graph `json:"graph"`
}

// GraphListResponse wraps multiple graphs.
type GraphListResponse struct {
	Graphs []*Graph `json:"graphs"`
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

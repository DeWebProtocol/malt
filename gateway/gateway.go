// Package gateway provides the HTTP gateway for MALT.
// It exposes MALT resolution, graph management, and write-side operations via REST API.
package gateway

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/dewebprotocol/malt/core/graph"
	cid "github.com/ipfs/go-cid"
)

// Graph represents a MALT graph (bucket) with metadata.
type Graph struct {
	ID          string    `json:"id"`
	Root        string    `json:"root"`
	CreatedAt   time.Time `json:"created_at"`
	ArcCount    int       `json:"arc_count"`
	Codec       string    `json:"codec"`
	KVStoreType string    `json:"kvstore_type"`
}

// Server is the MALT HTTP gateway server.
type Server struct {
	gm     *graph.Manager
	node   *NodeAdapter
	addr   string
	server *http.Server
}

// NewServer creates a new gateway server.
func NewServer(node *NodeAdapter, addr string) *Server {
	return &Server{
		gm:   node.GraphManager(),
		node: node,
		addr: addr,
	}
}

// Handler returns an http.Handler with all routes registered.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	return mux
}

// Start starts the HTTP server.
func (s *Server) Start() error {
	mux := http.NewServeMux()
	s.registerRoutes(mux)

	s.server = &http.Server{
		Addr:    s.addr,
		Handler: mux,
	}

	return s.server.ListenAndServe()
}

// Shutdown gracefully shuts down the server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.server != nil {
		return s.server.Shutdown(ctx)
	}
	return nil
}

// GraphManager returns the graph manager.
func (s *Server) GraphManager() *graph.Manager {
	return s.gm
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	// Health
	mux.HandleFunc("/health", s.handleHealth)

	// Graph management
	mux.HandleFunc("POST /graph", s.handleGraphCreate)
	mux.HandleFunc("GET /graph/{id}", s.handleGraphGet)
	mux.HandleFunc("DELETE /graph/{id}", s.handleGraphDelete)
	mux.HandleFunc("GET /graphs", s.handleGraphList)

	// Resolution (read-side)
	mux.HandleFunc("GET /resolve/{root}", s.handleResolve)
	mux.HandleFunc("GET /resolve/{root}/{path...}", s.handleResolveWithPath)
	mux.HandleFunc("POST /resolve", s.handleResolvePOST)

	// Proof and arc queries
	mux.HandleFunc("GET /proof/{root}/{path...}", s.handleProof)
	mux.HandleFunc("GET /arc/{root}/{path...}", s.handleArc)
	mux.HandleFunc("GET /snapshot/{root}", s.handleSnapshot)
	mux.HandleFunc("GET /content/{cid}", s.handleContent)

	// Write-side
	mux.HandleFunc("POST /update/{root}/{path}", s.handleUpdateWithPath)
	mux.HandleFunc("POST /update/batch/{root}", s.handleBatchUpdate)
	mux.HandleFunc("POST /structure", s.handleCreateStructure)

	// Verification
	mux.HandleFunc("POST /verify", s.handleVerify)
}

// writeJSON writes a JSON response with the given status and data.
func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// writeError writes an error response.
func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// writeBadRequest writes a 400 error.
func writeBadRequest(w http.ResponseWriter, message string) {
	writeError(w, http.StatusBadRequest, message)
}

// writeNotFound writes a 404 error.
func writeNotFound(w http.ResponseWriter, message string) {
	writeError(w, http.StatusNotFound, message)
}

// writeServerError writes a 500 error.
func writeServerError(w http.ResponseWriter, message string) {
	writeError(w, http.StatusInternalServerError, message)
}

// StepEvidenceResponse is the JSON representation of a resolution step.
type StepEvidenceResponse struct {
	Path     string `json:"path"`
	Target   string `json:"target"`
	Evidence string `json:"evidence"` // base64-encoded
	Kind     string `json:"kind"`
}

// ResolveResponse is the response for /resolve.
type ResolveResponse struct {
	Target     string                 `json:"target"`
	Transcript []StepEvidenceResponse `json:"transcript"`
}

// ResolveRequest is the POST body for /resolve.
type ResolveRequest struct {
	Root string `json:"root"`
	Path string `json:"path"`
}

// UpdateRequest is the POST body for single arc update.
type UpdateRequest struct {
	Path   string `json:"path"`
	Target string `json:"target"`
}

// BatchUpdateRequest is the POST body for batch update.
type BatchUpdateRequest struct {
	Updates map[string]string `json:"updates"` // path -> target CID
}

// CreateStructureRequest is the POST body for creating a structure.
type CreateStructureRequest struct {
	GraphID string            `json:"graph_id"`
	Arcs    map[string]string `json:"arcs"` // path -> target CID
}

// VerifyRequest is the POST body for /verify.
type VerifyRequest struct {
	Root       string                  `json:"root"`
	Transcript []VerifyStepRequest     `json:"transcript"`
}

// VerifyStepRequest is a single step in verification.
type VerifyStepRequest struct {
	Path     string `json:"path"`
	Target   string `json:"target"`
	Evidence string `json:"evidence"` // base64-encoded
	Kind     string `json:"kind"`
}

// VerifyResponse is the response for /verify.
type VerifyResponse struct {
	Valid bool `json:"valid"`
}

// WriteUpdateResponse is the response for a single arc update.
type WriteUpdateResponse struct {
	OldRoot   string `json:"old_root"`
	NewRoot   string `json:"new_root"`
	Path      string `json:"path"`
	OldTarget string `json:"old_target"`
	NewTarget string `json:"new_target"`
	Op        string `json:"op"`
}

// WriteBatchResponse is the response for batch update.
type WriteBatchResponse struct {
	OldRoot string                      `json:"old_root"`
	NewRoot string                      `json:"new_root"`
	PerArc  map[string]*WriteUpdateResponse `json:"per_arc"`
}

// ProofResponse is the response for /proof.
type ProofResponse struct {
	Target   string `json:"target"`
	Evidence string `json:"evidence"` // base64-encoded
}

// ArcResponse is the response for /arc.
type ArcResponse struct {
	Path   string `json:"path"`
	Target string `json:"target"`
}

// SnapshotResponse is the response for /snapshot.
type SnapshotResponse struct {
	Root string            `json:"root"`
	Arcs map[string]string `json:"arcs"` // path -> target CID
}

// GraphCreateRequest is the POST body for /graph.
type GraphCreateRequest struct {
	ID string `json:"id"`
}

// GraphResponse is the response for /graph.
type GraphResponse struct {
	Graph *Graph `json:"graph"`
}

// GraphListResponse is the response for /graphs.
type GraphListResponse struct {
	Graphs []*Graph `json:"graphs"`
}

// ContentResponse is the response for /content.
type ContentResponse struct {
	CID     string `json:"cid"`
	Content string `json:"content"` // base64-encoded raw content
	Size    int    `json:"size"`
}

// evidenceKind maps an evidence kind string.
func evidenceKind(kind string) string {
	switch kind {
	case "explicit":
		return "explicit"
	case "implicit":
		return "implicit"
	case "hamt":
		return "hamt"
	default:
		return "unknown"
	}
}

// decodeCID parses a CID string.
func decodeCID(s string) (cid.Cid, error) {
	if s == "" {
		return cid.Undef, fmt.Errorf("empty CID")
	}
	return cid.Decode(s)
}

// encodeBase64 encodes bytes to base64 string.
func encodeBase64(b []byte) string {
	return base64.StdEncoding.EncodeToString(b)
}

// decodeBase64 decodes a base64 string to bytes.
func decodeBase64(s string) ([]byte, error) {
	return base64.StdEncoding.DecodeString(s)
}

// enableCORS adds CORS headers to responses.
func enableCORS(w http.ResponseWriter, r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Expose-Headers", "Content-Type")
	}

	if r.Method == "OPTIONS" {
		w.WriteHeader(http.StatusOK)
		return true
	}
	return false
}

// ===== Internal types used by NodeAdapter =====

// ResolveResult contains the result of a resolution operation (internal).
type ResolveResult struct {
	Target     string     `json:"target"`
	Transcript *Transcript `json:"transcript"`
}

// Transcript records the evidence for each resolution step (internal).
type Transcript struct {
	Steps []StepEvidence `json:"steps"`
}

// StepEvidence represents evidence for a single resolution step (internal).
type StepEvidence struct {
	Path     string `json:"path"`
	Target   string `json:"target"`
	Evidence []byte `json:"evidence"`
	Kind     string `json:"kind"`
}

// WriteUpdateResult contains the result of a single arc update (internal).
type WriteUpdateResult struct {
	OldRoot   string `json:"old_root"`
	NewRoot   string `json:"new_root"`
	Path      string `json:"path"`
	OldTarget string `json:"old_target"`
	NewTarget string `json:"new_target"`
	Op        string `json:"op"`
}

// WriteBatchResult contains the result of a batch update (internal).
type WriteBatchResult struct {
	OldRoot string                      `json:"old_root"`
	NewRoot string                      `json:"new_root"`
	PerArc  map[string]*WriteUpdateResult `json:"per_arc"`
}

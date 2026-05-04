// Package server provides the MALT daemon HTTP API.
package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	"github.com/dewebprotocol/malt/core/api"
	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/types/evidence"
	"github.com/dewebprotocol/malt/httpapi"
	cid "github.com/ipfs/go-cid"
)

const defaultRootGraphID = "default"

// Server serves the daemon HTTP API.
type Server struct {
	node         *api.Node
	addr         string
	server       *http.Server
	defaultGraph *graph.Graph
	graphMu      sync.Mutex
}

// New creates a new daemon server.
func New(node *api.Node, addr string) *Server {
	return &Server{
		node: node,
		addr: addr,
	}
}

// Handler returns the configured HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	return mux
}

// Start starts the HTTP server.
func (s *Server) Start() error {
	s.server = &http.Server{
		Addr:    s.addr,
		Handler: s.Handler(),
	}
	return s.server.ListenAndServe()
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

func (s *Server) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/metrics", s.handleMetrics)
	mux.HandleFunc("POST /api/v1/metrics:reset", s.handleMetricsReset)

	// Read (Kubo-style: /{verb}/{root}/{path...})
	mux.HandleFunc("GET /api/v1/resolve/{root}/{path...}", s.handleResolveRead)
	mux.HandleFunc("GET /api/v1/proof/{root}/{path...}", s.handleProofRead)
	mux.HandleFunc("GET /api/v1/prooflist/{root}/{path...}", s.handleProofListRead)
	mux.HandleFunc("GET /api/v1/stat/{root}/{path...}", s.handleStatRead)
	mux.HandleFunc("GET /api/v1/content/{root}/{path...}", s.handleContentRead)
	mux.HandleFunc("GET /api/v1/content-proof/{root}/{path...}", s.handleContentProofRead)

	// Write (stateless materialization)
	mux.HandleFunc("POST /api/v1/roots", s.handleCreateStructure)
	mux.HandleFunc("POST /api/v1/roots/{root}/semantic-mutations", s.handleSemanticMutation)
	mux.HandleFunc("POST /api/v1/roots/{root}/update", s.handleUpdate)
	mux.HandleFunc("POST /api/v1/roots/{root}/updates:batch", s.handleBatchUpdate)
	mux.HandleFunc("POST /api/v1/roots/{root}/unixfs/file/{path...}", s.handleUnixFSFile)
	mux.HandleFunc("POST /api/v1/roots/{root}/unixfs/directory/{path...}", s.handleUnixFSDirectory)

	// Verify
	mux.HandleFunc("POST /api/v1/verify", s.handleVerify)

	// Lineage
	mux.HandleFunc("GET /api/v1/lineage", s.handleLineageList)
	mux.HandleFunc("GET /api/v1/lineage/count", s.handleLineageCount)
	mux.HandleFunc("GET /api/v1/lineage/{root}", s.handleLineageGet)
	mux.HandleFunc("GET /api/v1/lineage/{root}/ancestors", s.handleLineageAncestors)
	mux.HandleFunc("GET /api/v1/lineage/{root}/descendants", s.handleLineageDescendants)
}

func (s *Server) getOrCreateGraph(ctx context.Context) (*graph.Graph, error) {
	s.graphMu.Lock()
	defer s.graphMu.Unlock()
	if s.defaultGraph != nil {
		return s.defaultGraph, nil
	}
	var err error
	s.defaultGraph, err = s.node.NewGraph("default")
	return s.defaultGraph, err
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, &httpapi.ErrorResponse{Error: msg})
}

func decodeCID(raw string) (cid.Cid, error) {
	if raw == "" {
		return cid.Undef, fmt.Errorf("empty CID")
	}
	return cid.Decode(raw)
}

func encodeTranscript(transcript *resolver.Transcript) []httpapi.StepEvidence {
	if transcript == nil {
		return nil
	}
	steps := make([]httpapi.StepEvidence, len(transcript.Steps))
	for i, step := range transcript.Steps {
		steps[i] = httpapi.StepEvidence{
			Path:     step.Path.String(),
			Target:   step.Target.String(),
			Evidence: base64.StdEncoding.EncodeToString(step.Evidence.Bytes()),
			Kind:     evidenceKind(step.Evidence.Kind()),
		}
	}
	return steps
}

func evidenceKind(kind evidence.EvidenceKind) string {
	switch kind {
	case evidence.EvidenceKindExplicit:
		return "explicit"
	case evidence.EvidenceKindImplicit:
		return "implicit"
	case evidence.EvidenceKindHAMT:
		return "hamt"
	default:
		return "unknown"
	}
}

func snapshotToMap(snapshot arcset.ArcSet) (map[string]string, int, error) {
	arcs := make(map[string]string)
	iter := snapshot.Iterate()
	count := 0
	for {
		path, target, ok := iter.Next()
		if !ok {
			break
		}
		arcs[path.String()] = target.String()
		count++
	}
	if err := iter.Err(); err != nil {
		return nil, 0, err
	}
	return arcs, count, nil
}


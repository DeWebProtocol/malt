// Package server provides the MALT daemon HTTP API.
package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

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
	node   *api.Node
	addr   string
	server *http.Server

	gm *graph.Manager

	mu     sync.Mutex
	graphs map[string]*graph.Graph
}

// New creates a new daemon server.
func New(node *api.Node, addr string) *Server {
	return &Server{
		node:   node,
		addr:   addr,
		gm:     node.GraphManager(),
		graphs: make(map[string]*graph.Graph),
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

	mux.HandleFunc("POST /api/v1/graphs", s.handleGraphCreate)
	mux.HandleFunc("GET /api/v1/graphs", s.handleGraphList)
	mux.HandleFunc("GET /api/v1/graphs/{id}", s.handleGraphGet)
	mux.HandleFunc("DELETE /api/v1/graphs/{id}", s.handleGraphDelete)
	mux.HandleFunc("POST /api/v1/graphs/{id}/freeze", s.handleGraphFreeze)
	mux.HandleFunc("POST /api/v1/graphs/{id}/structure", s.handleGraphCreateStructure)
	mux.HandleFunc("GET /api/v1/graphs/{id}/resolve", s.handleGraphResolve)
	mux.HandleFunc("GET /api/v1/graphs/{id}/proof", s.handleGraphProof)
	mux.HandleFunc("GET /api/v1/graphs/{id}/snapshot", s.handleGraphSnapshot)
	mux.HandleFunc("POST /api/v1/graphs/{id}/update", s.handleGraphUpdate)
	mux.HandleFunc("POST /api/v1/graphs/{id}/updates:batch", s.handleGraphBatchUpdate)

	mux.HandleFunc("POST /api/v1/roots", s.handleRootCreateStructure)
	mux.HandleFunc("GET /api/v1/roots/{root}/resolve", s.handleRootResolve)
	mux.HandleFunc("GET /api/v1/roots/{root}/proof", s.handleRootProof)
	mux.HandleFunc("GET /api/v1/roots/{root}/snapshot", s.handleRootSnapshot)
	mux.HandleFunc("POST /api/v1/roots/{root}/update", s.handleRootUpdate)
	mux.HandleFunc("POST /api/v1/roots/{root}/updates:batch", s.handleRootBatchUpdate)

	mux.HandleFunc("POST /api/v1/verify", s.handleVerify)

	mux.HandleFunc("GET /api/v1/lineage", s.handleLineageList)
	mux.HandleFunc("GET /api/v1/lineage/count", s.handleLineageCount)
	mux.HandleFunc("GET /api/v1/lineage/{root}", s.handleLineageGet)
	mux.HandleFunc("GET /api/v1/lineage/{root}/ancestors", s.handleLineageAncestors)
	mux.HandleFunc("GET /api/v1/lineage/{root}/descendants", s.handleLineageDescendants)
}

func (s *Server) getGraph(ctx context.Context, id string) (*graph.Graph, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if g, ok := s.graphs[id]; ok {
		return g, nil
	}

	g, err := s.node.OpenGraph(ctx, id)
	if err != nil {
		if id == defaultRootGraphID && err == graph.ErrNotFound {
			g, err = s.node.NewGraph(id)
		}
		if err != nil {
			return nil, err
		}
	}

	s.graphs[id] = g
	return g, nil
}

func (s *Server) openManagedGraph(ctx context.Context, id string, requireActive bool) (*graph.GraphMeta, *graph.Graph, error) {
	var (
		meta *graph.GraphMeta
		err  error
	)
	if requireActive {
		meta, err = s.gm.RequireActive(ctx, id)
	} else {
		meta, err = s.gm.GetGraph(ctx, id)
	}
	if err != nil {
		return nil, nil, err
	}

	g, err := s.getGraph(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	return meta, g, nil
}

func graphHead(meta *graph.GraphMeta) (cid.Cid, error) {
	if meta == nil || !meta.Root.Defined() {
		return cid.Undef, fmt.Errorf("graph head is not defined")
	}
	return meta.Root, nil
}

func (s *Server) updateManagedGraphHead(ctx context.Context, graphID string, newRoot cid.Cid, arcCount int) error {
	_, err := s.gm.UpdateGraph(ctx, graphID, newRoot, arcCount)
	return err
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

func graphToResponse(meta *graph.GraphMeta) *httpapi.Graph {
	if meta == nil {
		return nil
	}
	return &httpapi.Graph{
		ID:        meta.ID,
		Root:      meta.Root.String(),
		CreatedAt: meta.CreatedAt.Format(time.RFC3339),
		UpdatedAt: meta.UpdatedAt.Format(time.RFC3339),
		ArcCount:  meta.ArcCount,
		Backend:   meta.Backend,
		EATType:   meta.EATType,
		State:     string(meta.State),
	}
}

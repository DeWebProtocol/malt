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

	// Bucket-first managed metadata surface (Phase 2).
	mux.HandleFunc("POST /api/v1/buckets", s.handleBucketCreate)
	mux.HandleFunc("GET /api/v1/buckets", s.handleBucketList)
	mux.HandleFunc("GET /api/v1/buckets/{id}", s.handleBucketGet)
	mux.HandleFunc("DELETE /api/v1/buckets/{id}", s.handleBucketDelete)
	mux.HandleFunc("POST /api/v1/buckets/{id}/freeze", s.handleBucketFreeze)
	mux.HandleFunc("POST /api/v1/buckets/{id}/structure", s.handleBucketCreateStructure)
	mux.HandleFunc("PUT /api/v1/buckets/{id}/head", s.handleBucketHeadSet)
	mux.HandleFunc("POST /api/v1/buckets/{id}/semantic-mutations", s.handleBucketSemanticMutation)
	mux.HandleFunc("POST /api/v1/buckets/{id}/maps", s.handleBucketMapsCreate)
	mux.HandleFunc("GET /api/v1/buckets/{id}/maps/{root}/snapshot", s.handleBucketMapsSnapshot)
	mux.HandleFunc("GET /api/v1/buckets/{id}/maps/{root}/resolve", s.handleBucketMapsResolve)
	mux.HandleFunc("POST /api/v1/buckets/{id}/maps/{root}/update", s.handleBucketMapsUpdate)
	mux.HandleFunc("POST /api/v1/buckets/{id}/maps/{root}/updates:batch", s.handleBucketMapsBatchUpdate)
	mux.HandleFunc("POST /api/v1/buckets/{id}/lists", s.handleBucketListsCreate)
	mux.HandleFunc("GET /api/v1/buckets/{id}/lists/{root}", s.handleBucketListsGet)
	mux.HandleFunc("POST /api/v1/buckets/{id}/unixfs/files", s.handleBucketUnixFSFile)
	mux.HandleFunc("POST /api/v1/buckets/{id}/unixfs/directories", s.handleBucketUnixFSDirectory)
	mux.HandleFunc("GET /api/v1/buckets/{id}/stat", s.handleBucketStat)
	mux.HandleFunc("GET /api/v1/buckets/{id}/content", s.handleBucketContent)
	mux.HandleFunc("GET /api/v1/buckets/{id}/content:proof", s.handleBucketContentProof)
	mux.HandleFunc("GET /api/v1/buckets/{id}/resolve", s.handleBucketResolve)
	mux.HandleFunc("GET /api/v1/buckets/{id}/proof", s.handleBucketProof)
	mux.HandleFunc("GET /api/v1/buckets/{id}/prooflist", s.handleBucketProofList)
	mux.HandleFunc("GET /api/v1/buckets/{id}/snapshot", s.handleBucketSnapshot)
	mux.HandleFunc("POST /api/v1/buckets/{id}/update", s.handleBucketUpdate)
	mux.HandleFunc("POST /api/v1/buckets/{id}/updates:batch", s.handleBucketBatchUpdate)

	mux.HandleFunc("POST /api/v1/roots", s.handleRootCreateStructure)
	mux.HandleFunc("GET /api/v1/roots/{root}/resolve", s.handleRootResolve)
	mux.HandleFunc("GET /api/v1/roots/{root}/proof", s.handleRootProof)
	mux.HandleFunc("GET /api/v1/roots/{root}/prooflist", s.handleRootProofList)
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

func bucketToResponse(meta *graph.GraphMeta) *httpapi.Bucket {
	if meta == nil {
		return nil
	}
	root := ""
	if meta.Root.Defined() {
		root = meta.Root.String()
	}
	return &httpapi.Bucket{
		ID:           meta.ID,
		Root:         root,
		CreatedAt:    meta.CreatedAt.Format(time.RFC3339),
		UpdatedAt:    meta.UpdatedAt.Format(time.RFC3339),
		ArcCount:     meta.ArcCount,
		Backend:      meta.Backend,
		ArcTableType: meta.ArcTableType,
		State:        string(meta.State),
	}
}

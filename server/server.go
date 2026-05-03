// Package server provides the MALT daemon HTTP API.
package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	mux.HandleFunc("GET /api/v1/metrics", s.handleMetrics)
	mux.HandleFunc("POST /api/v1/metrics:reset", s.handleMetricsReset)

	mux.HandleFunc("GET /api/v1/current/root", s.handleCurrentRootGet)
	mux.HandleFunc("POST /api/v1/current/root", s.handleCurrentRootGet)
	mux.HandleFunc("PUT /api/v1/current/root", s.handleCurrentRootSet)
	mux.HandleFunc("POST /api/v1/current/structure", s.handleCurrentCreateStructure)
	mux.HandleFunc("POST /api/v1/current/semantic-mutations", s.handleCurrentSemanticMutation)
	mux.HandleFunc("POST /api/v1/current/maps", s.handleCurrentMapsCreate)
	mux.HandleFunc("GET /api/v1/current/maps/{root}/snapshot", s.handleCurrentMapsSnapshot)
	mux.HandleFunc("GET /api/v1/current/maps/{root}/resolve", s.handleCurrentMapsResolve)
	mux.HandleFunc("POST /api/v1/current/maps/{root}/update", s.handleCurrentMapsUpdate)
	mux.HandleFunc("POST /api/v1/current/maps/{root}/updates:batch", s.handleCurrentMapsBatchUpdate)
	mux.HandleFunc("POST /api/v1/current/lists", s.handleCurrentListsCreate)
	mux.HandleFunc("GET /api/v1/current/lists/{root}", s.handleCurrentListsGet)
	mux.HandleFunc("POST /api/v1/current/unixfs:batch", s.handleCurrentUnixFSBatch)
	mux.HandleFunc("POST /api/v1/current/unixfs/files", s.handleCurrentUnixFSFile)
	mux.HandleFunc("POST /api/v1/current/unixfs/directories", s.handleCurrentUnixFSDirectory)
	mux.HandleFunc("GET /api/v1/current/stat", s.handleCurrentStat)
	mux.HandleFunc("GET /api/v1/current/content", s.handleCurrentContent)
	mux.HandleFunc("GET /api/v1/current/content:proof", s.handleCurrentContentProof)
	mux.HandleFunc("GET /api/v1/current/resolve", s.handleCurrentResolve)
	mux.HandleFunc("GET /api/v1/current/proof", s.handleCurrentProof)
	mux.HandleFunc("GET /api/v1/current/prooflist", s.handleCurrentProofList)
	mux.HandleFunc("GET /api/v1/current/snapshot", s.handleCurrentSnapshot)
	mux.HandleFunc("POST /api/v1/current/update", s.handleCurrentUpdate)
	mux.HandleFunc("POST /api/v1/current/updates:batch", s.handleCurrentBatchUpdate)

	mux.HandleFunc("POST /api/v1/roots", s.handleRootCreateStructure)
	mux.HandleFunc("GET /api/v1/roots/{root}/resolve", s.handleRootResolve)
	mux.HandleFunc("GET /api/v1/roots/{root}/proof", s.handleRootProof)
	mux.HandleFunc("GET /api/v1/roots/{root}/prooflist", s.handleRootProofList)
	mux.HandleFunc("GET /api/v1/roots/{root}/snapshot", s.handleRootSnapshot)
	mux.HandleFunc("POST /api/v1/roots/{root}/semantic-mutations", s.handleRootSemanticMutation)
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
		if id == defaultRootGraphID && errors.Is(err, graph.ErrNotFound) {
			if _, createErr := s.node.CreateManagedGraph(ctx, id, ""); createErr != nil && !errors.Is(createErr, graph.ErrAlreadyExists) {
				return nil, createErr
			}
			g, err = s.node.OpenGraph(ctx, id)
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

func (s *Server) openCurrentGraph(ctx context.Context, requireActive bool) (*graph.GraphMeta, *graph.Graph, error) {
	if _, err := s.gm.GetGraph(ctx, defaultRootGraphID); err != nil {
		if !errors.Is(err, graph.ErrNotFound) {
			return nil, nil, err
		}
		if _, err := s.node.CreateManagedGraph(ctx, defaultRootGraphID, ""); err != nil && !errors.Is(err, graph.ErrAlreadyExists) {
			return nil, nil, err
		}
	}
	return s.openManagedGraph(ctx, defaultRootGraphID, requireActive)
}

func graphHead(meta *graph.GraphMeta) (cid.Cid, error) {
	if meta == nil || !meta.Root.Defined() {
		return cid.Undef, fmt.Errorf("current root is not defined")
	}
	return meta.Root, nil
}

func (s *Server) updateCurrentRoot(ctx context.Context, newRoot cid.Cid, arcCount int) error {
	_, err := s.gm.UpdateGraph(ctx, defaultRootGraphID, newRoot, arcCount)
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

func currentRootResponse(meta *graph.GraphMeta) *httpapi.CurrentRootResponse {
	if meta == nil {
		return nil
	}
	root := ""
	if meta.Root.Defined() {
		root = meta.Root.String()
	}
	return &httpapi.CurrentRootResponse{
		Root:         root,
		ArcCount:     meta.ArcCount,
		Backend:      meta.Backend,
		ArcTableType: meta.ArcTableType,
		State:        string(meta.State),
	}
}

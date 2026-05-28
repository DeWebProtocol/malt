// Package server provides the MALT daemon HTTP API.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/dewebprotocol/malt/api/http"
	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/graph"
	"github.com/dewebprotocol/malt/runtime/node"
	cid "github.com/ipfs/go-cid"
)

const defaultRootGraphID = "default"

// Server serves the daemon HTTP API.
type Server struct {
	node           *node.Node
	addr           string
	lifecycleToken string
	browserOrigins map[string]struct{}
	server         *http.Server
	defaultGraph   graph.Runtime
	graphMu        sync.Mutex
}

// Option configures the daemon server.
type Option func(*Server)

// WithLifecycleToken exposes the managed-process identity token through
// /health for local lifecycle commands.
func WithLifecycleToken(token string) Option {
	return func(s *Server) {
		s.lifecycleToken = token
	}
}

// WithBrowserOrigins allows browser-based tools to call the local daemon from
// the configured origins.
func WithBrowserOrigins(origins []string) Option {
	return func(s *Server) {
		s.browserOrigins = browserOriginSet(origins)
	}
}

// New creates a new daemon server.
func New(node *node.Node, addr string, opts ...Option) *Server {
	s := &Server{
		node: node,
		addr: addr,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Handler returns the configured HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isRemovedPublicRoute(r.Method, r.URL.Path) {
			s.handleRemovedPublicRoute(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
	if len(s.browserOrigins) == 0 {
		return handler
	}
	return s.browserCORS(handler)
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
	// Admin (specific routes, matched first by Go ServeMux)
	mux.HandleFunc("GET /health", s.handleHealth)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("POST /metrics:reset", s.handleMetricsReset)
	mux.HandleFunc("POST /verify", s.handleVerify)

	// Removed public APIs stay reserved so they do not fall through to root
	// content routes.
	mux.HandleFunc("GET /lineage", s.handleRemovedPublicRoute)
	mux.HandleFunc("GET /lineage/count", s.handleRemovedPublicRoute)
	mux.HandleFunc("GET /lineage/{root}", s.handleRemovedPublicRoute)
	mux.HandleFunc("GET /lineage/{root}/ancestors", s.handleRemovedPublicRoute)
	mux.HandleFunc("GET /lineage/{root}/descendants", s.handleRemovedPublicRoute)

	// Semantic mutation is the writer route boundary.
	mux.HandleFunc("POST /{root}/_mutate", s.handleSemanticMutation)

	// UnixFS writes that start from an empty root. The path is supplied as a
	// query parameter so this fixed endpoint does not overlap with root-scoped
	// writer routes such as /{root}/_mutate.
	mux.HandleFunc("POST /_unixfs", s.handleWriteNewUnixFSRoot)

	// Resolve - explicit proof-producing path resolution.
	mux.HandleFunc("GET /resolve/{root}", s.handleResolve)
	mux.HandleFunc("GET /resolve/{root}/{path...}", s.handleResolve)

	// Core read/write (/{root}/{path...} format). POST is a UnixFS layout
	// convenience that converts file operations into semantic mutations before
	// writer application.
	mux.HandleFunc("GET /{root}", s.handleContent)
	mux.HandleFunc("GET /{root}/{path...}", s.handleContent)
	mux.HandleFunc("POST /{root}/{path...}", s.handleWrite)

	// Root creation
	mux.HandleFunc("POST /_", s.handleCreateStructure)
}

func isRemovedPublicRoute(method, rawPath string) bool {
	trimmed := strings.Trim(rawPath, "/")
	if trimmed == "" {
		return false
	}
	parts := strings.Split(trimmed, "/")
	if parts[0] == "lineage" {
		switch len(parts) {
		case 1, 2:
			return true
		case 3:
			return parts[2] == "ancestors" || parts[2] == "descendants"
		default:
			return false
		}
	}
	if method == http.MethodPost && len(parts) == 2 && parts[1] == "_batch-update" {
		return true
	}
	return false
}

func (s *Server) handleRemovedPublicRoute(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not found")
}

func (s *Server) getOrCreateGraph(ctx context.Context) (graph.Runtime, error) {
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

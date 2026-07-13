// Package server provides the MALT reference-executor HTTP API.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/dewebprotocol/malt/api/http"
	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/graph/querypath"
	"github.com/dewebprotocol/malt/runtime/node"
	cid "github.com/ipfs/go-cid"
)

const defaultRootGraphID = "default"

// Conservative HTTP server hardening defaults. These are intentionally generous
// for normal MALT workloads (large UnixFS uploads, slow CAS materialization)
// but tight enough to keep a single misbehaving client from holding sockets
// open indefinitely (slowloris) or burying the executor under one giant header
// stream.
//
// Callers that need different limits can override them via the
// WithServerLimits option; tests in particular use much shorter timeouts.
const (
	// DefaultReadHeaderTimeout caps how long a client has to send the request
	// line and headers. Bodies are read separately and may be longer-lived.
	DefaultReadHeaderTimeout = 10 * time.Second

	// DefaultIdleTimeout caps how long an idle keep-alive connection stays
	// open between requests.
	DefaultIdleTimeout = 60 * time.Second

	// DefaultReadTimeout caps the total time for reading the request,
	// including the body. Long uploads and JSON payloads may need this raised
	// at the operator level.
	DefaultReadTimeout = 5 * time.Minute

	// DefaultWriteTimeout caps the total time spent writing a response back
	// to a client. Range reads of very large list payloads may need this
	// raised.
	DefaultWriteTimeout = 5 * time.Minute

	// DefaultMaxHeaderBytes caps total header size to prevent header-flood
	// abuse and keep ProofList headers from silently exploding.
	DefaultMaxHeaderBytes = 64 * 1024
)

// ServerLimits configures HTTP-level resource bounds for the reference executor.
// Zero values fall back to the defaults above.
type ServerLimits struct {
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
}

func (l ServerLimits) withDefaults() ServerLimits {
	if l.ReadHeaderTimeout <= 0 {
		l.ReadHeaderTimeout = DefaultReadHeaderTimeout
	}
	if l.IdleTimeout <= 0 {
		l.IdleTimeout = DefaultIdleTimeout
	}
	if l.ReadTimeout <= 0 {
		l.ReadTimeout = DefaultReadTimeout
	}
	if l.WriteTimeout <= 0 {
		l.WriteTimeout = DefaultWriteTimeout
	}
	if l.MaxHeaderBytes <= 0 {
		l.MaxHeaderBytes = DefaultMaxHeaderBytes
	}
	return l
}

// Server serves the reference-executor HTTP API.
type Server struct {
	node           *node.Node
	addr           string
	lifecycleToken string
	browserOrigins *browserOriginPolicy
	limits         ServerLimits
	bodyLimits     BodyLimits
	server         *http.Server
	defaultGraph   runtimeGraph
	graphMu        sync.Mutex
	verifierCache  portableVerifierCache
}

// Option configures the reference-executor server.
type Option func(*Server)

// WithLifecycleToken configures the local managed-process identity token used
// by lifecycle commands.
func WithLifecycleToken(token string) Option {
	return func(s *Server) {
		s.lifecycleToken = token
	}
}

// WithBrowserOrigins allows browser-based tools to call the local executor from
// the configured origins.
func WithBrowserOrigins(origins []string) Option {
	return func(s *Server) {
		s.browserOrigins = browserOriginSet(origins)
	}
}

// WithServerLimits overrides the default HTTP-level resource bounds. Any zero
// field falls back to the package defaults; this lets callers tune one knob
// without restating the rest.
func WithServerLimits(limits ServerLimits) Option {
	return func(s *Server) {
		s.limits = limits
	}
}

// New creates a new reference-executor server.
func New(node *node.Node, addr string, opts ...Option) *Server {
	s := &Server{
		node: node,
		addr: addr,
	}
	for _, opt := range opts {
		opt(s)
	}
	s.limits = s.limits.withDefaults()
	s.bodyLimits = s.bodyLimits.withDefaults()
	return s
}

// Handler returns the configured HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.registerRoutes(mux)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := validateRawReadQueryPath(r); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if isRemovedPublicRoute(r.Method, r.URL.Path) {
			s.handleRemovedPublicRoute(w, r)
			return
		}
		mux.ServeHTTP(w, r)
	})
	if s.browserOrigins == nil {
		return handler
	}
	return s.browserCORS(handler)
}

// Start starts the HTTP server.
func (s *Server) Start() error {
	s.server = s.buildHTTPServer()
	return s.server.ListenAndServe()
}

// buildHTTPServer constructs the *http.Server with the configured limits
// applied. It is split out so tests can verify timeout propagation without
// racing against a goroutine that calls ListenAndServe.
func (s *Server) buildHTTPServer() *http.Server {
	return &http.Server{
		Addr:              s.addr,
		Handler:           s.Handler(),
		ReadHeaderTimeout: s.limits.ReadHeaderTimeout,
		ReadTimeout:       s.limits.ReadTimeout,
		WriteTimeout:      s.limits.WriteTimeout,
		IdleTimeout:       s.limits.IdleTimeout,
		MaxHeaderBytes:    s.limits.MaxHeaderBytes,
	}
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
	mux.HandleFunc("GET /_lifecycle/identity", s.handleLifecycleIdentity)
	mux.HandleFunc("GET /metrics", s.handleMetrics)
	mux.HandleFunc("POST /metrics:reset", s.handleMetricsReset)
	mux.HandleFunc("POST /verify", s.handleVerify)
	mux.HandleFunc("POST /v1/artifacts/resolve", s.handleArtifactResolve)
	mux.HandleFunc("POST /v1/artifacts/prove", s.handleArtifactProve)
	mux.HandleFunc("POST /v1/artifacts/verify", s.handleArtifactVerify)

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

	// Core read/write (/{root}/{path...} format). POST is a UnixFS application
	// convenience for UnixFS roots; legacy root migration requires explicit
	// opt-in before file operations are converted into semantic mutations.
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

func validateRawReadQueryPath(r *http.Request) error {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return nil
	}
	rawPath := r.URL.EscapedPath()
	if rawPath == "" {
		rawPath = r.URL.Path
	}

	if strings.HasPrefix(rawPath, "/resolve/") {
		afterPrefix := strings.TrimPrefix(rawPath, "/resolve/")
		_, rawQueryPath, ok := strings.Cut(afterPrefix, "/")
		if !ok {
			return nil
		}
		return validateEscapedQueryPath(rawQueryPath)
	}

	trimmed := strings.TrimPrefix(rawPath, "/")
	root, rawQueryPath, ok := strings.Cut(trimmed, "/")
	if !ok || root == "" || !contentReadRootSegment(root) {
		return nil
	}
	return validateEscapedQueryPath(rawQueryPath)
}

func contentReadRootSegment(root string) bool {
	switch root {
	case "health", "metrics", "metrics:reset", "resolve", "verify", "lineage", "_", "_unixfs":
		return false
	default:
		return true
	}
}

func validateEscapedQueryPath(rawPath string) error {
	if rawPath == "" {
		return nil
	}
	segments := strings.Split(rawPath, "/")
	for _, segment := range segments {
		if segment == "" {
			return fmt.Errorf("%w: contains empty segment", querypath.ErrInvalidQueryPath)
		}
		decoded, err := url.PathUnescape(segment)
		if err != nil {
			return fmt.Errorf("%w: invalid escape sequence", querypath.ErrInvalidQueryPath)
		}
		if strings.ContainsRune(decoded, '\x00') {
			return fmt.Errorf("%w: contains NUL byte", querypath.ErrInvalidQueryPath)
		}
		switch decoded {
		case ".":
			return fmt.Errorf("%w: contains current-directory segment", querypath.ErrInvalidQueryPath)
		case "..":
			return fmt.Errorf("%w: contains parent-directory segment", querypath.ErrInvalidQueryPath)
		}
	}
	return nil
}

func (s *Server) handleRemovedPublicRoute(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, "not found")
}

func (s *Server) getOrCreateGraph(ctx context.Context) (runtimeGraph, error) {
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

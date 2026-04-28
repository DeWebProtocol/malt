package mock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/dewebprotocol/malt/core/cas"
	cid "github.com/ipfs/go-cid"
)

// HTTPServer exposes a Kubo-compatible /api/v0 subset for the mock CAS.
type HTTPServer struct {
	addr   string
	cas    cas.Client
	server *http.Server
}

// NewHTTPServer creates a new mock CAS HTTP server.
func NewHTTPServer(addr string, c cas.Client) *HTTPServer {
	return &HTTPServer{
		addr: addr,
		cas:  c,
	}
}

// Handler returns the configured HTTP handler.
func (s *HTTPServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v0/block/get", s.handleBlockGet)
	mux.HandleFunc("POST /api/v0/block/put", s.handleBlockPut)
	mux.HandleFunc("POST /api/v0/block/stat", s.handleBlockStat)
	return mux
}

// Start starts serving the Kubo-compatible API.
func (s *HTTPServer) Start() error {
	s.server = &http.Server{
		Addr:    s.addr,
		Handler: s.Handler(),
	}
	return s.server.ListenAndServe()
}

// Shutdown gracefully stops the HTTP server.
func (s *HTTPServer) Shutdown(ctx context.Context) error {
	if s.server == nil {
		return nil
	}
	return s.server.Shutdown(ctx)
}

func (s *HTTPServer) handleBlockGet(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("arg")
	blockCID, err := cid.Decode(raw)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid cid: %v", err), http.StatusBadRequest)
		return
	}

	data, err := s.cas.Get(r.Context(), blockCID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *HTTPServer) handleBlockPut(w http.ResponseWriter, r *http.Request) {
	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid multipart upload: %v", err), http.StatusBadRequest)
		return
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, fmt.Sprintf("read upload: %v", err), http.StatusBadRequest)
		return
	}

	codec, err := blockPutCodec(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	var blockCID cid.Cid
	if codec == cid.Raw {
		blockCID, err = s.cas.Put(r.Context(), data)
	} else {
		if typed, ok := s.cas.(cas.TypedWriter); ok {
			blockCID, err = typed.PutWithCodec(r.Context(), data, codec)
		} else {
			err = fmt.Errorf("configured CAS does not support typed writes")
		}
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := struct {
		Key  string `json:"Key"`
		Size int    `json:"Size"`
	}{
		Key:  blockCID.String(),
		Size: len(data),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func blockPutCodec(r *http.Request) (uint64, error) {
	raw := r.URL.Query().Get("format")
	if raw == "" || raw == "raw" {
		return cid.Raw, nil
	}
	codec, err := strconv.ParseUint(raw, 0, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid block format %q: %w", raw, err)
	}
	return codec, nil
}

func (s *HTTPServer) handleBlockStat(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("arg")
	blockCID, err := cid.Decode(raw)
	if err != nil {
		http.Error(w, fmt.Sprintf("invalid cid: %v", err), http.StatusBadRequest)
		return
	}

	exists, err := s.cas.Has(r.Context(), blockCID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !exists {
		http.Error(w, "block not found", http.StatusNotFound)
		return
	}

	data, err := s.cas.Get(r.Context(), blockCID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp := struct {
		Key  string `json:"Key"`
		Size int    `json:"Size"`
		Time string `json:"Time"`
	}{
		Key:  blockCID.String(),
		Size: len(data),
		Time: time.Now().Format(time.RFC3339),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

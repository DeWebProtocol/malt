// Package main provides an HTTP Gateway for MALT.
// It exposes MALT resolution capabilities via HTTP endpoints.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/dewebprotocol/malt/config"
	"github.com/dewebprotocol/malt/core/api"
	cid "github.com/ipfs/go-cid"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var (
	Version = "dev"
	cfgFile string
	listen  string
)

func main() {
	config.Init()

	var rootCmd = &cobra.Command{
		Use:   "malt-gateway",
		Short: "MALT HTTP Gateway",
		Long: `HTTP Gateway for MALT (Mutable structure LAyer on Top).

Provides HTTP endpoints for:
- Resolving paths from MALT structure roots
- Verifying resolution transcripts
- Health checks`,
		Version: Version,
		Run:     runGateway,
	}

	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file")
	rootCmd.PersistentFlags().StringVarP(&listen, "listen", "l", ":8080", "listen address")

	viper.BindPFlag("listen", rootCmd.PersistentFlags().Lookup("listen"))

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runGateway(cmd *cobra.Command, args []string) {
	// Create MALT node
	var nodeOpts []api.Option
	if cfgFile != "" {
		nodeOpts = append(nodeOpts, api.WithConfigFile(cfgFile))
	}

	node, err := api.NewNode(nodeOpts...)
	if err != nil {
		log.Fatalf("Failed to create MALT node: %v", err)
	}
	defer node.Close()

	log.Printf("MALT Gateway v%s starting...", Version)
	log.Printf("Configuration: %s", node.Config())

	// Create HTTP handlers
	h := &handlers{node: node}

	// Setup routes
	mux := http.NewServeMux()
	mux.HandleFunc("/health", h.health)
	mux.HandleFunc("/resolve/", h.resolve)
	mux.HandleFunc("/verify", h.verify)

	// Start server
	log.Printf("Listening on %s", listen)
	if err := http.ListenAndServe(listen, mux); err != nil {
		log.Fatalf("Server failed: %v", err)
	}
}

// handlers holds HTTP handlers for the gateway.
type handlers struct {
	node *api.Node
}

// health handles GET /health
func (h *handlers) health(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"status":  "ok",
		"version": Version,
	}
	h.writeJSON(w, http.StatusOK, resp)
}

// resolveRequest is the request body for /resolve
type resolveRequest struct {
	Root string `json:"root"`
	Path string `json:"path"`
}

// resolveResponse is the response for /resolve
type resolveResponse struct {
	Target     string                 `json:"target"`
	Transcript []stepEvidenceResponse `json:"transcript"`
}

// stepEvidenceResponse is the JSON representation of a step evidence
type stepEvidenceResponse struct {
	Path     string `json:"path"`
	Target   string `json:"target"`
	Evidence []byte `json:"evidence"`
}

// resolve handles POST /resolve and GET /resolve/{cid}/{path}
func (h *handlers) resolve(w http.ResponseWriter, r *http.Request) {
	var rootCid cid.Cid
	var path string

	if r.Method == http.MethodGet {
		// GET /resolve/{cid}/{path}
		// Path format: /resolve/{cid}/{path...}
		urlPath := strings.TrimPrefix(r.URL.Path, "/resolve/")
		parts := strings.SplitN(urlPath, "/", 2)
		if len(parts) < 1 || parts[0] == "" {
			h.writeError(w, http.StatusBadRequest, "missing CID in path")
			return
		}

		var err error
		rootCid, err = cid.Decode(parts[0])
		if err != nil {
			h.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid CID: %v", err))
			return
		}

		if len(parts) > 1 {
			path = parts[1]
		}
	} else if r.Method == http.MethodPost {
		// POST /resolve with JSON body
		var req resolveRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			h.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
			return
		}

		var err error
		rootCid, err = cid.Decode(req.Root)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid CID: %v", err))
			return
		}
		path = req.Path
	} else {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	// Resolve
	result, err := h.node.HybridResolver().Resolve(rootCid, path)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, fmt.Sprintf("resolution failed: %v", err))
		return
	}

	// Build response
	resp := resolveResponse{
		Target:     result.Target.String(),
		Transcript: make([]stepEvidenceResponse, len(result.Transcript.Steps)),
	}

	for i, step := range result.Transcript.Steps {
		resp.Transcript[i] = stepEvidenceResponse{
			Path:     step.Path,
			Target:   step.Target.String(),
			Evidence: step.Evidence.Bytes(),
		}
	}

	h.writeJSON(w, http.StatusOK, resp)
}

// verifyRequest is the request body for /verify
type verifyRequest struct {
	Root       string                `json:"root"`
	Transcript []stepEvidenceRequest `json:"transcript"`
}

// stepEvidenceRequest is the JSON representation of a step evidence for verification
type stepEvidenceRequest struct {
	Path     string `json:"path"`
	Target   string `json:"target"`
	Evidence []byte `json:"evidence"`
	Kind     string `json:"kind"` // "explicit", "implicit", or "hamt"
}

// verifyResponse is the response for /verify
type verifyResponse struct {
	Valid bool `json:"valid"`
}

// verify handles POST /verify
func (h *handlers) verify(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req verifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	rootCid, err := cid.Decode(req.Root)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid CID: %v", err))
		return
	}

	// Build transcript from request
	// Note: This is a simplified verification that checks the transcript structure
	// In a full implementation, you would reconstruct the Evidence objects

	// For now, we just verify that the transcript is well-formed
	// and that the root CID is valid
	valid := rootCid.Defined()
	for _, step := range req.Transcript {
		if step.Path == "" {
			valid = false
			break
		}
		_, err := cid.Decode(step.Target)
		if err != nil {
			valid = false
			break
		}
		if len(step.Evidence) == 0 {
			valid = false
			break
		}
	}

	resp := verifyResponse{Valid: valid}
	h.writeJSON(w, http.StatusOK, resp)
}

// writeJSON writes a JSON response
func (h *handlers) writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// errorResponse is an error response
type errorResponse struct {
	Error string `json:"error"`
}

// writeError writes an error response
func (h *handlers) writeError(w http.ResponseWriter, status int, message string) {
	h.writeJSON(w, status, errorResponse{Error: message})
}

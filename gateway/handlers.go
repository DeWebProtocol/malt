package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/dewebprotocol/malt/core/graph"
	cid "github.com/ipfs/go-cid"
)

// ===== Health =====

// handleHealth handles GET /health
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status": "ok",
	})
}

// ===== Graph Management =====

// handleGraphCreate handles POST /graph
func (s *Server) handleGraphCreate(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	var req GraphCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	if req.ID == "" {
		writeBadRequest(w, "graph id is required")
		return
	}

	// Check if graph already exists
	_, err := s.gm.GetGraph(context.Background(), req.ID)
	if err == nil {
		writeError(w, http.StatusConflict, fmt.Sprintf("graph %q already exists", req.ID))
		return
	}
	if err != graph.ErrNotFound {
		writeServerError(w, fmt.Sprintf("failed to check graph: %v", err))
		return
	}

	g, err := s.node.node.CreateManagedGraph(context.Background(), req.ID, "")
	if err != nil {
		writeServerError(w, fmt.Sprintf("failed to create graph: %v", err))
		return
	}
	writeJSON(w, http.StatusCreated, GraphResponse{Graph: graphResponseFromGraph(g)})
}

// handleGraphGet handles GET /graph/{id}
func (s *Server) handleGraphGet(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeBadRequest(w, "graph id is required")
		return
	}

	g, err := s.gm.GetGraph(context.Background(), id)
	if err == graph.ErrNotFound || err == graph.ErrDeleted {
		writeNotFound(w, fmt.Sprintf("graph %q not found", id))
		return
	}
	if err != nil {
		writeServerError(w, fmt.Sprintf("failed to get graph: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, GraphResponse{Graph: graphResponseFromGraph(g)})
}

// handleGraphDelete handles DELETE /graph/{id}
func (s *Server) handleGraphDelete(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	id := r.PathValue("id")
	if id == "" {
		writeBadRequest(w, "graph id is required")
		return
	}

	err := s.gm.DeleteGraph(context.Background(), id)
	if err != nil {
		writeNotFound(w, fmt.Sprintf("graph %q not found", id))
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"message": fmt.Sprintf("graph %q deleted", id)})
}

// handleGraphList handles GET /graphs
func (s *Server) handleGraphList(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	graphs, err := s.gm.ListGraphs(context.Background())
	if err != nil {
		writeServerError(w, fmt.Sprintf("failed to list graphs: %v", err))
		return
	}

	resp := &GraphListResponse{Graphs: make([]*Graph, len(graphs))}
	for i, g := range graphs {
		resp.Graphs[i] = graphToGateway(g)
	}
	writeJSON(w, http.StatusOK, resp)
}

// ===== Resolution =====

// handleResolve handles GET /resolve/{root} (no path, resolve to structure root or payload)
func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	root := r.PathValue("root")
	if root == "" {
		writeBadRequest(w, "root CID is required")
		return
	}

	result, err := s.node.HybridResolve(root, "")
	if err != nil {
		writeServerError(w, err.Error())
		return
	}

	resp := ResolveResponse{
		Target:     result.Target,
		Transcript: make([]StepEvidenceResponse, len(result.Transcript.Steps)),
	}
	for i, step := range result.Transcript.Steps {
		resp.Transcript[i] = StepEvidenceResponse{
			Path:     step.Path,
			Target:   step.Target,
			Evidence: encodeBase64(step.Evidence),
			Kind:     evidenceKind(step.Kind),
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleResolveWithPath handles GET /resolve/{root}/{path...}
func (s *Server) handleResolveWithPath(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	root := r.PathValue("root")
	path := r.PathValue("path")

	result, err := s.node.HybridResolve(root, path)
	if err != nil {
		writeServerError(w, err.Error())
		return
	}

	resp := ResolveResponse{
		Target:     result.Target,
		Transcript: make([]StepEvidenceResponse, len(result.Transcript.Steps)),
	}
	for i, step := range result.Transcript.Steps {
		resp.Transcript[i] = StepEvidenceResponse{
			Path:     step.Path,
			Target:   step.Target,
			Evidence: encodeBase64(step.Evidence),
			Kind:     evidenceKind(step.Kind),
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleResolvePOST handles POST /resolve
func (s *Server) handleResolvePOST(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	var req ResolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	result, err := s.node.HybridResolve(req.Root, req.Path)
	if err != nil {
		writeServerError(w, err.Error())
		return
	}

	resp := ResolveResponse{
		Target:     result.Target,
		Transcript: make([]StepEvidenceResponse, len(result.Transcript.Steps)),
	}
	for i, step := range result.Transcript.Steps {
		resp.Transcript[i] = StepEvidenceResponse{
			Path:     step.Path,
			Target:   step.Target,
			Evidence: encodeBase64(step.Evidence),
			Kind:     evidenceKind(step.Kind),
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// ===== Proof and Arc Queries =====

// handleProof handles GET /proof/{root}/{path...}
func (s *Server) handleProof(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	rootStr := r.PathValue("root")
	path := r.PathValue("path")

	// For proof generation, we need to resolve the path to get the target and evidence.
	// The resolve endpoint already returns both, so we reuse it.
	result, err := s.node.HybridResolve(rootStr, path)
	if err != nil {
		writeServerError(w, err.Error())
		return
	}

	if len(result.Transcript.Steps) == 0 {
		writeNotFound(w, "no matching arc found for path")
		return
	}

	// Return the last step's evidence (the one that matched the full path)
	lastStep := result.Transcript.Steps[len(result.Transcript.Steps)-1]
	writeJSON(w, http.StatusOK, ProofResponse{
		Target:   lastStep.Target,
		Evidence: encodeBase64(lastStep.Evidence),
	})
}

// handleArc handles GET /arc/{root}/{path...}
func (s *Server) handleArc(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	rootStr := r.PathValue("root")
	path := r.PathValue("path")

	target, err := s.node.GetArc(defaultGatewayGraphID, rootStr, path)
	if err != nil {
		writeNotFound(w, fmt.Sprintf("arc %q not found: %v", path, err))
		return
	}

	writeJSON(w, http.StatusOK, ArcResponse{
		Path:   path,
		Target: target,
	})
}

// handleSnapshot handles GET /snapshot/{root}
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	rootStr := r.PathValue("root")
	arcs, err := s.node.GetArcSetSnapshot(defaultGatewayGraphID, rootStr)
	if err != nil {
		writeServerError(w, fmt.Sprintf("failed to get snapshot: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, SnapshotResponse{
		Root: rootStr,
		Arcs: arcs,
	})
}

// handleContent handles GET /content/{cid}
func (s *Server) handleContent(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	cidStr := r.PathValue("cid")
	content, err := s.node.CASGet(cidStr)
	if err != nil {
		writeNotFound(w, fmt.Sprintf("content %q not found: %v", cidStr, err))
		return
	}

	writeJSON(w, http.StatusOK, ContentResponse{
		CID:     cidStr,
		Content: encodeBase64(content),
		Size:    len(content),
	})
}

// ===== Write-Side Operations =====

// handleUpdateWithPath handles POST /update/{root}/{path}
// Request body: {"target": "<cid>"}
func (s *Server) handleUpdateWithPath(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	rootStr := r.PathValue("root")
	path := r.PathValue("path")

	var req UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	wa, err := s.node.Writer()
	if err != nil {
		writeServerError(w, err.Error())
		return
	}
	result, err := wa.UpdateArc(context.Background(), defaultGatewayGraphID, rootStr, path, req.Target)
	if err != nil {
		writeServerError(w, err.Error())
		return
	}

	writeJSON(w, http.StatusOK, WriteUpdateResponse{
		OldRoot:   result.OldRoot,
		NewRoot:   result.NewRoot,
		Path:      result.Path,
		OldTarget: result.OldTarget,
		NewTarget: result.NewTarget,
		Op:        result.Op,
	})
}

// handleBatchUpdate handles POST /update/batch/{root}
func (s *Server) handleBatchUpdate(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	rootStr := r.PathValue("root")

	var req BatchUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	wa, err := s.node.Writer()
	if err != nil {
		writeServerError(w, err.Error())
		return
	}
	result, err := wa.BatchUpdateArcs(context.Background(), defaultGatewayGraphID, rootStr, req.Updates)
	if err != nil {
		writeServerError(w, err.Error())
		return
	}

	perArc := make(map[string]*WriteUpdateResponse)
	for path, r := range result.PerArc {
		perArc[path] = &WriteUpdateResponse{
			OldRoot:   r.OldRoot,
			NewRoot:   r.NewRoot,
			Path:      r.Path,
			OldTarget: r.OldTarget,
			NewTarget: r.NewTarget,
			Op:        r.Op,
		}
	}

	writeJSON(w, http.StatusOK, WriteBatchResponse{
		OldRoot: result.OldRoot,
		NewRoot: result.NewRoot,
		PerArc:  perArc,
	})
}

// handleCreateStructure handles POST /structure
func (s *Server) handleCreateStructure(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	var req CreateStructureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	if len(req.Arcs) == 0 {
		writeBadRequest(w, "arcs is required")
		return
	}

	graphID := req.GraphID
	if graphID == "" {
		graphID = defaultGatewayGraphID
	}

	wa, err := s.node.Writer()
	if err != nil {
		writeServerError(w, err.Error())
		return
	}
	rootStr, err := wa.CreateStructure(context.Background(), graphID, req.Arcs)
	if err != nil {
		writeServerError(w, err.Error())
		return
	}

	// Update graph metadata if a graph was specified
	if req.GraphID != "" {
		g, err := s.gm.GetGraph(context.Background(), graphID)
		if err != nil {
			// Log the error but don't fail the response — structure was still created
			_ = fmt.Errorf("failed to get graph %q: %w", graphID, err)
		} else {
			if _, err := s.gm.UpdateGraph(context.Background(), graphID, decodeCIDOr(rootStr), len(req.Arcs)); err != nil {
				_ = fmt.Errorf("failed to update graph %q: %w", graphID, err)
			}
			_ = g // mark used
		}
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"root": rootStr,
	})
}

// graphResponseFromGraph converts a core graph.Graph to a gateway Graph response.
func graphResponseFromGraph(g *graph.GraphMeta) *Graph {
	return &Graph{
		ID:          g.ID,
		Root:        g.Root.String(),
		CreatedAt:   g.CreatedAt,
		ArcCount:    g.ArcCount,
		Codec:       g.Backend,
		KVStoreType: "kvstore",
	}
}

// graphToGraph converts a core graph.Graph to a gateway Graph.
func graphToGateway(g *graph.GraphMeta) *Graph {
	return graphResponseFromGraph(g)
}

// decodeCIDOr decodes a CID string, returning cid.Undef on error.
func decodeCIDOr(s string) cid.Cid {
	c, err := decodeCID(s)
	if err != nil {
		return cid.Undef
	}
	return c
}

// ===== Verification =====

// handleVerify handles POST /verify
func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	var req VerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	transcript := &Transcript{
		Steps: make([]StepEvidence, len(req.Transcript)),
	}
	for i, step := range req.Transcript {
		evBytes, err := decodeBase64(step.Evidence)
		if err != nil {
			writeBadRequest(w, fmt.Sprintf("invalid evidence at step %d: %v", i, err))
			return
		}
		transcript.Steps[i] = StepEvidence{
			Path:     step.Path,
			Target:   step.Target,
			Evidence: evBytes,
			Kind:     evidenceKind(step.Kind),
		}
	}

	valid, err := s.node.VerifyTranscript(req.Root, transcript)
	if err != nil {
		writeServerError(w, fmt.Sprintf("verification failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, VerifyResponse{Valid: valid})
}

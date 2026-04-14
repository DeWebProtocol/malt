package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/types/evidence"
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

func writeManagedGraphError(w http.ResponseWriter, graphID string, err error) {
	switch {
	case errors.Is(err, graph.ErrNotFound), errors.Is(err, graph.ErrDeleted):
		writeNotFound(w, fmt.Sprintf("graph %q not found", graphID))
	case errors.Is(err, graph.ErrFrozen), errors.Is(err, graph.ErrInvalidState):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeServerError(w, err.Error())
	}
}

func (s *Server) openManagedGraph(ctx context.Context, graphID string) (*graph.GraphMeta, *graph.Graph, error) {
	meta, err := s.gm.GetGraph(ctx, graphID)
	if err != nil {
		return nil, nil, err
	}

	g, err := s.node.ManagedGraph(graphID)
	if err != nil {
		return nil, nil, err
	}
	return meta, g, nil
}

func (s *Server) openActiveManagedGraph(ctx context.Context, graphID string) (*graph.GraphMeta, *graph.Graph, error) {
	meta, err := s.gm.RequireActive(ctx, graphID)
	if err != nil {
		return nil, nil, err
	}

	g, err := s.node.ManagedGraph(graphID)
	if err != nil {
		return nil, nil, err
	}
	return meta, g, nil
}

func managedGraphHead(meta *graph.GraphMeta) (cid.Cid, error) {
	if meta == nil {
		return cid.Undef, fmt.Errorf("graph metadata is nil")
	}
	if !meta.Root.Defined() {
		return cid.Undef, fmt.Errorf("graph %q has no head root", meta.ID)
	}
	return meta.Root, nil
}

func snapshotToMap(snapshot arcset.Snapshot) (map[string]string, int, error) {
	arcs := make(map[string]string)
	count := 0
	iter := snapshot.Iterate()
	for {
		path, target, ok := iter.Next()
		if !ok {
			break
		}
		arcs[path] = target.String()
		count++
	}
	if iter.Err() != nil {
		return nil, 0, iter.Err()
	}
	return arcs, count, nil
}

func (s *Server) updateManagedGraphHead(ctx context.Context, graphID string, g *graph.Graph, newRoot cid.Cid) error {
	snapshot, err := g.Snapshot(ctx, newRoot)
	if err != nil {
		return fmt.Errorf("snapshot new root: %w", err)
	}

	_, arcCount, err := snapshotToMap(snapshot)
	if err != nil {
		return fmt.Errorf("count arcs: %w", err)
	}

	_, err = s.gm.UpdateGraph(ctx, graphID, newRoot, arcCount)
	return err
}

// handleGraphResolve handles GET /graph/{id}/resolve
func (s *Server) handleGraphResolve(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	graphID := r.PathValue("id")
	meta, g, err := s.openManagedGraph(context.Background(), graphID)
	if err != nil {
		writeManagedGraphError(w, graphID, err)
		return
	}

	root, err := managedGraphHead(meta)
	if err != nil {
		writeNotFound(w, err.Error())
		return
	}

	result, err := g.Resolver().Resolve(root, "")
	if err != nil {
		writeServerError(w, fmt.Sprintf("resolution failed: %v", err))
		return
	}

	resp := ResolveResponse{
		Target:     result.Target.String(),
		Transcript: make([]StepEvidenceResponse, len(result.Transcript.Steps)),
	}
	for i, step := range result.Transcript.Steps {
		kind := "unknown"
		switch step.Evidence.Kind() {
		case evidence.EvidenceKindExplicit:
			kind = "explicit"
		case evidence.EvidenceKindImplicit:
			kind = "implicit"
		case evidence.EvidenceKindHAMT:
			kind = "hamt"
		}
		resp.Transcript[i] = StepEvidenceResponse{
			Path:     step.Path,
			Target:   step.Target.String(),
			Evidence: encodeBase64(step.Evidence.Bytes()),
			Kind:     kind,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleGraphResolveWithPath handles GET /graph/{id}/resolve/{path...}
func (s *Server) handleGraphResolveWithPath(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	graphID := r.PathValue("id")
	path := r.PathValue("path")

	meta, g, err := s.openManagedGraph(context.Background(), graphID)
	if err != nil {
		writeManagedGraphError(w, graphID, err)
		return
	}

	root, err := managedGraphHead(meta)
	if err != nil {
		writeNotFound(w, err.Error())
		return
	}

	result, err := g.Resolver().Resolve(root, path)
	if err != nil {
		writeServerError(w, fmt.Sprintf("resolution failed: %v", err))
		return
	}

	resp := ResolveResponse{
		Target:     result.Target.String(),
		Transcript: make([]StepEvidenceResponse, len(result.Transcript.Steps)),
	}
	for i, step := range result.Transcript.Steps {
		kind := "unknown"
		switch step.Evidence.Kind() {
		case evidence.EvidenceKindExplicit:
			kind = "explicit"
		case evidence.EvidenceKindImplicit:
			kind = "implicit"
		case evidence.EvidenceKindHAMT:
			kind = "hamt"
		}
		resp.Transcript[i] = StepEvidenceResponse{
			Path:     step.Path,
			Target:   step.Target.String(),
			Evidence: encodeBase64(step.Evidence.Bytes()),
			Kind:     kind,
		}
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleGraphProof handles GET /graph/{id}/proof/{path...}
func (s *Server) handleGraphProof(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	graphID := r.PathValue("id")
	path := r.PathValue("path")

	meta, g, err := s.openManagedGraph(context.Background(), graphID)
	if err != nil {
		writeManagedGraphError(w, graphID, err)
		return
	}

	root, err := managedGraphHead(meta)
	if err != nil {
		writeNotFound(w, err.Error())
		return
	}

	result, err := g.Resolver().Resolve(root, path)
	if err != nil {
		writeServerError(w, fmt.Sprintf("resolution failed: %v", err))
		return
	}
	if len(result.Transcript.Steps) == 0 {
		writeNotFound(w, "no matching arc found for path")
		return
	}

	lastStep := result.Transcript.Steps[len(result.Transcript.Steps)-1]
	writeJSON(w, http.StatusOK, ProofResponse{
		Target:   lastStep.Target.String(),
		Evidence: encodeBase64(lastStep.Evidence.Bytes()),
	})
}

// handleGraphArc handles GET /graph/{id}/arc/{path...}
func (s *Server) handleGraphArc(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	graphID := r.PathValue("id")
	path := r.PathValue("path")

	meta, g, err := s.openManagedGraph(context.Background(), graphID)
	if err != nil {
		writeManagedGraphError(w, graphID, err)
		return
	}

	root, err := managedGraphHead(meta)
	if err != nil {
		writeNotFound(w, err.Error())
		return
	}

	target, err := g.Writer().GetArc(context.Background(), g.BucketId(), root, path)
	if err != nil {
		writeNotFound(w, fmt.Sprintf("arc %q not found: %v", path, err))
		return
	}

	writeJSON(w, http.StatusOK, ArcResponse{
		Path:   path,
		Target: target.String(),
	})
}

// handleGraphSnapshot handles GET /graph/{id}/snapshot
func (s *Server) handleGraphSnapshot(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	graphID := r.PathValue("id")
	meta, g, err := s.openManagedGraph(context.Background(), graphID)
	if err != nil {
		writeManagedGraphError(w, graphID, err)
		return
	}

	root, err := managedGraphHead(meta)
	if err != nil {
		writeNotFound(w, err.Error())
		return
	}

	snapshot, err := g.Snapshot(context.Background(), root)
	if err != nil {
		writeServerError(w, fmt.Sprintf("failed to get snapshot: %v", err))
		return
	}

	arcs, _, err := snapshotToMap(snapshot)
	if err != nil {
		writeServerError(w, fmt.Sprintf("failed to iterate snapshot: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, SnapshotResponse{
		Root: root.String(),
		Arcs: arcs,
	})
}

// handleGraphUpdateWithPath handles POST /graph/{id}/update/{path}
func (s *Server) handleGraphUpdateWithPath(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	graphID := r.PathValue("id")
	path := r.PathValue("path")

	var req UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	meta, g, err := s.openActiveManagedGraph(context.Background(), graphID)
	if err != nil {
		writeManagedGraphError(w, graphID, err)
		return
	}

	root, err := managedGraphHead(meta)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	var newTarget cid.Cid
	if req.Target != "" {
		newTarget, err = decodeCID(req.Target)
		if err != nil {
			writeBadRequest(w, fmt.Sprintf("invalid target CID: %v", err))
			return
		}
	}

	result, err := g.Writer().UpdateArc(context.Background(), g.BucketId(), root, path, newTarget)
	if err != nil {
		writeServerError(w, fmt.Sprintf("update failed: %v", err))
		return
	}

	if err := s.updateManagedGraphHead(context.Background(), graphID, g, result.NewRoot); err != nil {
		writeServerError(w, fmt.Sprintf("failed to update graph metadata: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, WriteUpdateResponse{
		OldRoot:   result.OldRoot.String(),
		NewRoot:   result.NewRoot.String(),
		Path:      result.Path,
		OldTarget: result.OldTarget.String(),
		NewTarget: result.NewTarget.String(),
		Op:        result.Op.String(),
	})
}

// handleGraphBatchUpdate handles POST /graph/{id}/update/batch
func (s *Server) handleGraphBatchUpdate(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	graphID := r.PathValue("id")

	var req BatchUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	meta, g, err := s.openActiveManagedGraph(context.Background(), graphID)
	if err != nil {
		writeManagedGraphError(w, graphID, err)
		return
	}

	root, err := managedGraphHead(meta)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	updates := make(map[string]cid.Cid, len(req.Updates))
	for path, targetStr := range req.Updates {
		if targetStr == "" {
			updates[path] = cid.Undef
			continue
		}
		target, err := decodeCID(targetStr)
		if err != nil {
			writeBadRequest(w, fmt.Sprintf("invalid target CID for %s: %v", path, err))
			return
		}
		updates[path] = target
	}

	result, err := g.Writer().BatchUpdateArcs(context.Background(), g.BucketId(), root, updates)
	if err != nil {
		writeServerError(w, fmt.Sprintf("batch update failed: %v", err))
		return
	}

	if err := s.updateManagedGraphHead(context.Background(), graphID, g, result.NewRoot); err != nil {
		writeServerError(w, fmt.Sprintf("failed to update graph metadata: %v", err))
		return
	}

	perArc := make(map[string]*WriteUpdateResponse, len(result.PerArc))
	for path, r := range result.PerArc {
		perArc[path] = &WriteUpdateResponse{
			OldRoot:   r.OldRoot.String(),
			NewRoot:   r.NewRoot.String(),
			Path:      r.Path,
			OldTarget: r.OldTarget.String(),
			NewTarget: r.NewTarget.String(),
			Op:        r.Op.String(),
		}
	}

	writeJSON(w, http.StatusOK, WriteBatchResponse{
		OldRoot: result.OldRoot.String(),
		NewRoot: result.NewRoot.String(),
		PerArc:  perArc,
	})
}

// handleGraphCreateStructure handles POST /graph/{id}/structure
func (s *Server) handleGraphCreateStructure(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	graphID := r.PathValue("id")

	var req CreateStructureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, fmt.Sprintf("invalid JSON: %v", err))
		return
	}
	if req.GraphID != "" && req.GraphID != graphID {
		writeBadRequest(w, fmt.Sprintf("graph_id %q does not match route %q", req.GraphID, graphID))
		return
	}
	if len(req.Arcs) == 0 {
		writeBadRequest(w, "arcs is required")
		return
	}

	_, g, err := s.openActiveManagedGraph(context.Background(), graphID)
	if err != nil {
		writeManagedGraphError(w, graphID, err)
		return
	}

	arcsMap := make(map[string]cid.Cid, len(req.Arcs))
	for path, targetStr := range req.Arcs {
		target, err := decodeCID(targetStr)
		if err != nil {
			writeBadRequest(w, fmt.Sprintf("invalid target CID for %s: %v", path, err))
			return
		}
		arcsMap[path] = target
	}

	root, err := g.Writer().CreateStructure(context.Background(), g.BucketId(), arcset.NewMapFrom(arcsMap))
	if err != nil {
		writeServerError(w, fmt.Sprintf("create structure failed: %v", err))
		return
	}

	if _, err := s.gm.UpdateGraph(context.Background(), graphID, root, len(arcsMap)); err != nil {
		writeServerError(w, fmt.Sprintf("failed to update graph metadata: %v", err))
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{
		"root": root.String(),
	})
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

	ctx := context.Background()
	managedGraphID := req.GraphID
	if managedGraphID == "" {
		if _, err := s.gm.GetGraph(ctx, defaultGatewayGraphID); err == nil {
			managedGraphID = defaultGatewayGraphID
		} else if !errors.Is(err, graph.ErrNotFound) {
			writeManagedGraphError(w, defaultGatewayGraphID, err)
			return
		}
	}

	if managedGraphID != "" {
		_, g, err := s.openActiveManagedGraph(ctx, managedGraphID)
		if err != nil {
			writeManagedGraphError(w, managedGraphID, err)
			return
		}

		arcsMap := make(map[string]cid.Cid, len(req.Arcs))
		for path, targetStr := range req.Arcs {
			target, err := decodeCID(targetStr)
			if err != nil {
				writeBadRequest(w, fmt.Sprintf("invalid target CID for %s: %v", path, err))
				return
			}
			arcsMap[path] = target
		}

		root, err := g.Writer().CreateStructure(ctx, g.BucketId(), arcset.NewMapFrom(arcsMap))
		if err != nil {
			writeServerError(w, fmt.Sprintf("create structure failed: %v", err))
			return
		}
		if _, err := s.gm.UpdateGraph(ctx, managedGraphID, root, len(arcsMap)); err != nil {
			writeServerError(w, fmt.Sprintf("failed to update graph metadata: %v", err))
			return
		}

		writeJSON(w, http.StatusCreated, map[string]string{
			"root": root.String(),
		})
		return
	}

	wa, err := s.node.Writer()
	if err != nil {
		writeServerError(w, err.Error())
		return
	}
	rootStr, err := wa.CreateStructure(ctx, defaultGatewayGraphID, req.Arcs)
	if err != nil {
		writeServerError(w, err.Error())
		return
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

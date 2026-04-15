package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/types/evidence"
	cid "github.com/ipfs/go-cid"
)

// ===== Health =====

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ===== Graph Management =====

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

	_, err := s.gm.GetGraph(context.Background(), req.ID)
	if err == nil {
		writeError(w, http.StatusConflict, fmt.Sprintf("graph %q already exists", req.ID))
		return
	}
	if err != graph.ErrNotFound {
		writeServerError(w, fmt.Sprintf("failed to check graph: %v", err))
		return
	}

	g, err := s.node.CreateManagedGraph(context.Background(), req.ID, "")
	if err != nil {
		writeServerError(w, fmt.Sprintf("failed to create graph: %v", err))
		return
	}
	writeJSON(w, http.StatusCreated, GraphResponse{Graph: graphResponseFromGraph(g)})
}

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

	g, err := s.getGraph(ctx, graphID)
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

	g, err := s.getGraph(ctx, graphID)
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

// ===== Graph-scoped handlers =====

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

	writeJSON(w, http.StatusOK, ResolveResponse{
		Target:     result.Target.String(),
		Transcript: stepsToResponse(result.Transcript.Steps),
	})
}

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

	writeJSON(w, http.StatusOK, ResolveResponse{
		Target:     result.Target.String(),
		Transcript: stepsToResponse(result.Transcript.Steps),
	})
}

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

	writeJSON(w, http.StatusCreated, map[string]string{"root": root.String()})
}

// ===== Non-managed resolution =====

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	root := r.PathValue("root")
	if root == "" {
		writeBadRequest(w, "root CID is required")
		return
	}

	g, err := s.getGraph(context.Background(), defaultGatewayGraphID)
	if err != nil {
		writeServerError(w, err.Error())
		return
	}

	rootCid, err := decodeCID(root)
	if err != nil {
		writeBadRequest(w, fmt.Sprintf("invalid root CID: %v", err))
		return
	}

	result, err := g.Resolver().Resolve(rootCid, "")
	if err != nil {
		writeServerError(w, fmt.Sprintf("resolution failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, ResolveResponse{
		Target:     result.Target.String(),
		Transcript: stepsToResponse(result.Transcript.Steps),
	})
}

func (s *Server) handleResolveWithPath(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	root := r.PathValue("root")
	path := r.PathValue("path")

	g, err := s.getGraph(context.Background(), defaultGatewayGraphID)
	if err != nil {
		writeServerError(w, err.Error())
		return
	}

	rootCid, err := decodeCID(root)
	if err != nil {
		writeBadRequest(w, fmt.Sprintf("invalid root CID: %v", err))
		return
	}

	result, err := g.Resolver().Resolve(rootCid, path)
	if err != nil {
		writeServerError(w, fmt.Sprintf("resolution failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, ResolveResponse{
		Target:     result.Target.String(),
		Transcript: stepsToResponse(result.Transcript.Steps),
	})
}

func (s *Server) handleResolvePOST(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	var req ResolveRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	g, err := s.getGraph(context.Background(), defaultGatewayGraphID)
	if err != nil {
		writeServerError(w, err.Error())
		return
	}

	rootCid, err := decodeCID(req.Root)
	if err != nil {
		writeBadRequest(w, fmt.Sprintf("invalid root CID: %v", err))
		return
	}

	result, err := g.Resolver().Resolve(rootCid, req.Path)
	if err != nil {
		writeServerError(w, fmt.Sprintf("resolution failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, ResolveResponse{
		Target:     result.Target.String(),
		Transcript: stepsToResponse(result.Transcript.Steps),
	})
}

// ===== Proof and Arc Queries =====

func (s *Server) handleProof(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	rootStr := r.PathValue("root")
	path := r.PathValue("path")

	g, err := s.getGraph(context.Background(), defaultGatewayGraphID)
	if err != nil {
		writeServerError(w, err.Error())
		return
	}

	rootCid, err := decodeCID(rootStr)
	if err != nil {
		writeBadRequest(w, fmt.Sprintf("invalid root CID: %v", err))
		return
	}

	result, err := g.Resolver().Resolve(rootCid, path)
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

func (s *Server) handleArc(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	rootStr := r.PathValue("root")
	path := r.PathValue("path")

	g, err := s.getGraph(context.Background(), defaultGatewayGraphID)
	if err != nil {
		writeServerError(w, err.Error())
		return
	}

	rootCid, err := decodeCID(rootStr)
	if err != nil {
		writeBadRequest(w, fmt.Sprintf("invalid root CID: %v", err))
		return
	}

	target, err := g.Writer().GetArc(context.Background(), g.BucketId(), rootCid, path)
	if err != nil {
		writeNotFound(w, fmt.Sprintf("arc %q not found: %v", path, err))
		return
	}

	writeJSON(w, http.StatusOK, ArcResponse{
		Path:   path,
		Target: target.String(),
	})
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	rootStr := r.PathValue("root")

	g, err := s.getGraph(context.Background(), defaultGatewayGraphID)
	if err != nil {
		writeServerError(w, err.Error())
		return
	}

	rootCid, err := decodeCID(rootStr)
	if err != nil {
		writeBadRequest(w, fmt.Sprintf("invalid root CID: %v", err))
		return
	}

	snapshot, err := g.Snapshot(context.Background(), rootCid)
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
		Root: rootStr,
		Arcs: arcs,
	})
}

func (s *Server) handleContent(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	cidStr := r.PathValue("cid")
	content, err := s.node.CAS().Get(context.Background(), decodeCIDOr(cidStr))
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

	g, err := s.getGraph(context.Background(), defaultGatewayGraphID)
	if err != nil {
		writeServerError(w, err.Error())
		return
	}

	rootCid, err := decodeCID(rootStr)
	if err != nil {
		writeBadRequest(w, fmt.Sprintf("invalid root CID: %v", err))
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

	result, err := g.Writer().UpdateArc(context.Background(), g.BucketId(), rootCid, path, newTarget)
	if err != nil {
		writeServerError(w, fmt.Sprintf("update failed: %v", err))
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

	g, err := s.getGraph(context.Background(), defaultGatewayGraphID)
	if err != nil {
		writeServerError(w, err.Error())
		return
	}

	rootCid, err := decodeCID(rootStr)
	if err != nil {
		writeBadRequest(w, fmt.Sprintf("invalid root CID: %v", err))
		return
	}

	updates := make(map[string]cid.Cid, len(req.Updates))
	for path, targetStr := range req.Updates {
		if targetStr == "" {
			updates[path] = cid.Undef
		} else {
			target, err := decodeCID(targetStr)
			if err != nil {
				writeBadRequest(w, fmt.Sprintf("invalid target CID for %s: %v", path, err))
				return
			}
			updates[path] = target
		}
	}

	result, err := g.Writer().BatchUpdateArcs(context.Background(), g.BucketId(), rootCid, updates)
	if err != nil {
		writeServerError(w, fmt.Sprintf("batch update failed: %v", err))
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

		writeJSON(w, http.StatusCreated, map[string]string{"root": root.String()})
		return
	}

	g, err := s.getGraph(ctx, defaultGatewayGraphID)
	if err != nil {
		writeServerError(w, err.Error())
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

	writeJSON(w, http.StatusCreated, map[string]string{"root": root.String()})
}

// ===== Verification =====

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	if enableCORS(w, r) {
		return
	}

	var req VerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBadRequest(w, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	g, err := s.getGraph(context.Background(), defaultGatewayGraphID)
	if err != nil {
		writeServerError(w, err.Error())
		return
	}

	rootCid, err := decodeCID(req.Root)
	if err != nil {
		writeBadRequest(w, fmt.Sprintf("invalid root CID: %v", err))
		return
	}

	steps := make([]resolver.StepEvidence, len(req.Transcript))
	for i, step := range req.Transcript {
		evBytes, err := decodeBase64(step.Evidence)
		if err != nil {
			writeBadRequest(w, fmt.Sprintf("invalid evidence at step %d: %v", i, err))
			return
		}

		targetCid, err := decodeCID(step.Target)
		if err != nil {
			writeBadRequest(w, fmt.Sprintf("invalid target CID at step %d: %v", i, err))
			return
		}

		var ev evidence.Evidence
		switch step.Kind {
		case "explicit":
			ev = evidence.NewExplicitEvidence(evBytes)
		case "implicit":
			ev = evidence.NewImplicitEvidence(evBytes)
		case "hamt":
			ev = evidence.NewHAMTEvidence(evBytes)
		default:
			writeBadRequest(w, fmt.Sprintf("unknown evidence kind at step %d: %s", i, step.Kind))
			return
		}

		steps[i] = resolver.StepEvidence{
			Path:     step.Path,
			Target:   targetCid,
			Evidence: ev,
		}
	}

	valid, err := g.Resolver().VerifyTranscript(rootCid, &resolver.Transcript{Steps: steps})
	if err != nil {
		writeServerError(w, fmt.Sprintf("verification failed: %v", err))
		return
	}

	writeJSON(w, http.StatusOK, VerifyResponse{Valid: valid})
}

// ===== Graph conversion helpers =====

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

func graphToGateway(g *graph.GraphMeta) *Graph {
	return graphResponseFromGraph(g)
}

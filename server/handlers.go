package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/lineage"
	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/types/evidence"
	"github.com/dewebprotocol/malt/core/writer"
	"github.com/dewebprotocol/malt/httpapi"
	cid "github.com/ipfs/go-cid"
)

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, &httpapi.HealthResponse{Status: "ok"})
}

func (s *Server) handleGraphCreate(w http.ResponseWriter, r *http.Request) {
	var req httpapi.GraphCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "graph id is required")
		return
	}

	meta, err := s.node.CreateManagedGraph(r.Context(), req.ID, req.Backend)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, graph.ErrAlreadyExists) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, &httpapi.GraphResponse{Graph: graphToResponse(meta)})
}

func (s *Server) handleGraphList(w http.ResponseWriter, r *http.Request) {
	graphs, err := s.gm.ListGraphs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := &httpapi.GraphListResponse{Graphs: make([]*httpapi.Graph, len(graphs))}
	for i, meta := range graphs {
		resp.Graphs[i] = graphToResponse(meta)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleGraphGet(w http.ResponseWriter, r *http.Request) {
	meta, err := s.gm.GetGraph(r.Context(), r.PathValue("id"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, graph.ErrNotFound) || errors.Is(err, graph.ErrDeleted) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, &httpapi.GraphResponse{Graph: graphToResponse(meta)})
}

func (s *Server) handleGraphDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.gm.DeleteGraph(r.Context(), r.PathValue("id")); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, graph.ErrNotFound) || errors.Is(err, graph.ErrDeleted) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleGraphFreeze(w http.ResponseWriter, r *http.Request) {
	if err := s.gm.FreezeGraph(r.Context(), r.PathValue("id")); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, graph.ErrNotFound) || errors.Is(err, graph.ErrDeleted) {
			status = http.StatusNotFound
		}
		if errors.Is(err, graph.ErrInvalidState) {
			status = http.StatusConflict
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "frozen"})
}

func (s *Server) handleGraphCreateStructure(w http.ResponseWriter, r *http.Request) {
	graphID := r.PathValue("id")
	_, g, err := s.openManagedGraph(r.Context(), graphID, true)
	if err != nil {
		writeManagedGraphError(w, err)
		return
	}

	var req httpapi.CreateStructureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}
	if len(req.Arcs) == 0 {
		writeError(w, http.StatusBadRequest, "arcs is required")
		return
	}

	parsedArcs, err := parseArcMap(req.Arcs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	snapshot, arcCount, err := buildCreateSnapshot(parsedArcs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	root, err := g.Writer().CreateStructure(r.Context(), g.BucketId(), snapshot)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.updateManagedGraphHead(r.Context(), graphID, root, arcCount); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, &httpapi.CreateStructureResponse{Root: root.String()})
}

func (s *Server) handleGraphResolve(w http.ResponseWriter, r *http.Request) {
	meta, g, err := s.openManagedGraph(r.Context(), r.PathValue("id"), false)
	if err != nil {
		writeManagedGraphError(w, err)
		return
	}
	root, err := graphHead(meta)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.resolveAndWrite(w, g, root, r.URL.Query().Get("path"))
}

func (s *Server) handleGraphProof(w http.ResponseWriter, r *http.Request) {
	s.handleGraphResolve(w, r)
}

func (s *Server) handleGraphSnapshot(w http.ResponseWriter, r *http.Request) {
	meta, g, err := s.openManagedGraph(r.Context(), r.PathValue("id"), false)
	if err != nil {
		writeManagedGraphError(w, err)
		return
	}
	root, err := graphHead(meta)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	s.writeSnapshot(w, r.Context(), g, root)
}

func (s *Server) handleGraphUpdate(w http.ResponseWriter, r *http.Request) {
	graphID := r.PathValue("id")
	meta, g, err := s.openManagedGraph(r.Context(), graphID, true)
	if err != nil {
		writeManagedGraphError(w, err)
		return
	}
	root, err := graphHead(meta)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	var req httpapi.UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}
	req.Path = nonEmpty(req.Path, r.URL.Query().Get("path"))
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	target, err := parseOptionalCID(req.Target)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := g.Writer().UpdateArc(r.Context(), g.BucketId(), root, req.Path, target)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.updateManagedGraphHead(r.Context(), graphID, result.NewRoot, applyArcDelta(meta.ArcCount, result.Op)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updateResponse(result))
}

func (s *Server) handleGraphBatchUpdate(w http.ResponseWriter, r *http.Request) {
	graphID := r.PathValue("id")
	meta, g, err := s.openManagedGraph(r.Context(), graphID, true)
	if err != nil {
		writeManagedGraphError(w, err)
		return
	}
	root, err := graphHead(meta)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	var req httpapi.BatchUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	parsedUpdates, err := parseArcMap(req.Updates)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := g.Writer().BatchUpdateArcs(r.Context(), g.BucketId(), root, parsedUpdates)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.updateManagedGraphHead(r.Context(), graphID, result.NewRoot, applyBatchArcDelta(meta.ArcCount, result)); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, batchResponse(result))
}

func (s *Server) handleRootCreateStructure(w http.ResponseWriter, r *http.Request) {
	g, err := s.getGraph(r.Context(), defaultRootGraphID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var req httpapi.CreateStructureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}
	if len(req.Arcs) == 0 {
		writeError(w, http.StatusBadRequest, "arcs is required")
		return
	}

	parsedArcs, err := parseArcMap(req.Arcs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	snapshot, _, err := buildCreateSnapshot(parsedArcs)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	root, err := g.Writer().CreateStructure(r.Context(), g.BucketId(), snapshot)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, &httpapi.CreateStructureResponse{Root: root.String()})
}

func (s *Server) handleRootResolve(w http.ResponseWriter, r *http.Request) {
	g, err := s.getGraph(r.Context(), defaultRootGraphID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	root, err := decodeCID(r.PathValue("root"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.resolveAndWrite(w, g, root, r.URL.Query().Get("path"))
}

func (s *Server) handleRootProof(w http.ResponseWriter, r *http.Request) {
	s.handleRootResolve(w, r)
}

func (s *Server) handleRootSnapshot(w http.ResponseWriter, r *http.Request) {
	g, err := s.getGraph(r.Context(), defaultRootGraphID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	root, err := decodeCID(r.PathValue("root"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.writeSnapshot(w, r.Context(), g, root)
}

func (s *Server) handleRootUpdate(w http.ResponseWriter, r *http.Request) {
	g, err := s.getGraph(r.Context(), defaultRootGraphID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	root, err := decodeCID(r.PathValue("root"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var req httpapi.UpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}
	req.Path = nonEmpty(req.Path, r.URL.Query().Get("path"))
	if req.Path == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	target, err := parseOptionalCID(req.Target)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := g.Writer().UpdateArc(r.Context(), g.BucketId(), root, req.Path, target)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, updateResponse(result))
}

func (s *Server) handleRootBatchUpdate(w http.ResponseWriter, r *http.Request) {
	g, err := s.getGraph(r.Context(), defaultRootGraphID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	root, err := decodeCID(r.PathValue("root"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var req httpapi.BatchUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	parsedUpdates, err := parseArcMap(req.Updates)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	result, err := g.Writer().BatchUpdateArcs(r.Context(), g.BucketId(), root, parsedUpdates)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, batchResponse(result))
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	g, err := s.getGraph(r.Context(), defaultRootGraphID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var req httpapi.VerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}
	root, err := decodeCID(req.Root)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	steps := make([]resolver.StepEvidence, len(req.Transcript))
	for i, step := range req.Transcript {
		target, err := decodeCID(step.Target)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid target CID at step %d: %v", i, err))
			return
		}
		evBytes, err := base64.StdEncoding.DecodeString(step.Evidence)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid evidence at step %d: %v", i, err))
			return
		}
		ev, err := decodeEvidence(step.Kind, evBytes)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid evidence kind at step %d: %v", i, err))
			return
		}
		steps[i] = resolver.StepEvidence{
			Path:     arcset.CanonicalizePath(step.Path),
			Target:   target,
			Evidence: ev,
		}
	}

	valid, err := g.Resolver().VerifyTranscript(root, &resolver.Transcript{Steps: steps})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, &httpapi.VerifyResponse{Valid: valid})
}

func (s *Server) handleLineageGet(w http.ResponseWriter, r *http.Request) {
	root, err := decodeCID(r.PathValue("root"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rec, err := s.node.LineageManager().Get(r.Context(), root)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, lineageRecord(rec))
}

func (s *Server) handleLineageAncestors(w http.ResponseWriter, r *http.Request) {
	root, err := decodeCID(r.PathValue("root"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	maxDepth, _ := strconv.Atoi(r.URL.Query().Get("max_depth"))
	items, err := s.node.LineageManager().Ancestors(r.Context(), root, maxDepth)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, &httpapi.CIDListResponse{Items: cidsToStrings(items)})
}

func (s *Server) handleLineageDescendants(w http.ResponseWriter, r *http.Request) {
	root, err := decodeCID(r.PathValue("root"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	items, err := s.node.LineageManager().Descendants(r.Context(), root)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, &httpapi.CIDListResponse{Items: cidsToStrings(items)})
}

func (s *Server) handleLineageList(w http.ResponseWriter, r *http.Request) {
	records, err := s.node.LineageManager().List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	resp := &httpapi.LineageListResponse{Records: make([]httpapi.LineageRecordResponse, len(records))}
	for i, rec := range records {
		resp.Records[i] = lineageRecord(rec)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleLineageCount(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, &httpapi.CountResponse{Count: s.node.LineageManager().Count(r.Context())})
}

func (s *Server) resolveAndWrite(w http.ResponseWriter, g *graph.Graph, root cid.Cid, path string) {
	result, err := g.Resolver().Resolve(root, path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, &httpapi.ResolveResponse{
		Target:     result.Target.String(),
		Transcript: encodeTranscript(result.Transcript),
	})
}

func (s *Server) writeSnapshot(w http.ResponseWriter, ctx context.Context, g *graph.Graph, root cid.Cid) {
	snapshot, err := g.Snapshot(ctx, root)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	arcs, _, err := snapshotToMap(snapshot)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, &httpapi.SnapshotResponse{
		Root: root.String(),
		Arcs: arcs,
	})
}

func writeManagedGraphError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, graph.ErrNotFound), errors.Is(err, graph.ErrDeleted):
		status = http.StatusNotFound
	case errors.Is(err, graph.ErrFrozen), errors.Is(err, graph.ErrInvalidState):
		status = http.StatusConflict
	}
	writeError(w, status, err.Error())
}

func parseArcMap(raw map[string]string) (map[string]cid.Cid, error) {
	out := make(map[string]cid.Cid, len(raw))
	for path, target := range raw {
		parsed, err := parseOptionalCID(target)
		if err != nil {
			return nil, fmt.Errorf("invalid target for %q: %w", path, err)
		}
		out[path] = parsed
	}
	return out, nil
}

func buildCreateSnapshot(arcs map[string]cid.Cid) (arcset.ArcSet, int, error) {
	canonical := make(map[string]cid.Cid, len(arcs))
	for rawPath, target := range arcs {
		path := arcset.CanonicalizePath(rawPath)
		if path.IsEmpty() {
			return nil, 0, fmt.Errorf("path must not be empty")
		}

		if existing, ok := canonical[path.String()]; ok && !existing.Equals(target) {
			return nil, 0, fmt.Errorf("duplicate canonical path %q in arcs", path.String())
		}
		canonical[path.String()] = target
	}

	arcCount := 0
	for _, target := range canonical {
		if target.Defined() {
			arcCount++
		}
	}

	return arcset.NewSetFrom(canonical), arcCount, nil
}

func decodeTargetCID(raw string) cid.Cid {
	if raw == "" {
		return cid.Undef
	}
	c, err := cid.Decode(raw)
	if err != nil {
		return cid.Undef
	}
	return c
}

func parseOptionalCID(raw string) (cid.Cid, error) {
	if raw == "" {
		return cid.Undef, nil
	}
	return cid.Decode(raw)
}

func updateResponse(result *writer.UpdateResult) *httpapi.WriteUpdateResponse {
	return &httpapi.WriteUpdateResponse{
		OldRoot:   result.OldRoot.String(),
		NewRoot:   result.NewRoot.String(),
		Path:      result.Path.String(),
		OldTarget: result.OldTarget.String(),
		NewTarget: result.NewTarget.String(),
		Op:        result.Op.String(),
	}
}

func batchResponse(result *writer.BatchUpdateResult) *httpapi.WriteBatchResponse {
	resp := &httpapi.WriteBatchResponse{
		OldRoot: result.OldRoot.String(),
		NewRoot: result.NewRoot.String(),
		PerArc:  make(map[string]*httpapi.WriteUpdateResponse, len(result.PerArc)),
	}
	for path, r := range result.PerArc {
		resp.PerArc[path.String()] = &httpapi.WriteUpdateResponse{
			OldRoot:   r.OldRoot.String(),
			NewRoot:   r.NewRoot.String(),
			Path:      r.Path.String(),
			OldTarget: r.OldTarget.String(),
			NewTarget: r.NewTarget.String(),
			Op:        r.Op.String(),
		}
	}
	return resp
}

func decodeEvidence(kind string, payload []byte) (evidence.Evidence, error) {
	switch kind {
	case "explicit":
		return evidence.NewExplicitEvidence(payload), nil
	case "implicit":
		return evidence.NewImplicitEvidence(payload), nil
	case "hamt":
		return evidence.NewHAMTEvidence(payload), nil
	default:
		return nil, fmt.Errorf("unknown evidence kind %q", kind)
	}
}

func lineageRecord(rec *lineage.LineageRecord) httpapi.LineageRecordResponse {
	parent := ""
	if rec.Parent.Defined() {
		parent = rec.Parent.String()
	}
	return httpapi.LineageRecordResponse{
		Root:      rec.Root.String(),
		Parent:    parent,
		Timestamp: rec.Timestamp.Format(time.RFC3339),
		Depth:     rec.Depth,
		ArcCount:  rec.ArcCount,
	}
}

func cidsToStrings(items []cid.Cid) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.String()
	}
	return out
}

func nonEmpty(primary string, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

func applyArcDelta(current int, op writer.ArcOp) int {
	switch op {
	case writer.ArcInsert:
		return current + 1
	case writer.ArcDelete:
		if current > 0 {
			return current - 1
		}
		return 0
	default:
		return current
	}
}

func applyBatchArcDelta(current int, result *writer.BatchUpdateResult) int {
	next := current
	if result == nil {
		return next
	}
	for _, perArc := range result.PerArc {
		next = applyArcDelta(next, perArc.Op)
	}
	return next
}

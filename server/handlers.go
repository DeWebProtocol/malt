package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/dewebprotocol/malt/core/bucketpath"
	"github.com/dewebprotocol/malt/core/cas"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/gateway"
	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/layout/malt/unixfs"
	"github.com/dewebprotocol/malt/core/lineage"
	"github.com/dewebprotocol/malt/core/manifest"
	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/resolver/step/explicit"
	"github.com/dewebprotocol/malt/core/structure/list"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/types/evidence"
	"github.com/dewebprotocol/malt/core/types/prooflist"
	"github.com/dewebprotocol/malt/core/writer"
	"github.com/dewebprotocol/malt/httpapi"
	cid "github.com/ipfs/go-cid"
)

const fixedBucketListChunkSize = 262144

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, &httpapi.HealthResponse{Status: "ok"})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, &httpapi.MetricsResponse{Snapshot: s.node.MetricsSnapshot()})
}

func (s *Server) handleMetricsReset(w http.ResponseWriter, r *http.Request) {
	s.node.ResetMetrics()
	writeJSON(w, http.StatusOK, &httpapi.MetricsResponse{Snapshot: s.node.MetricsSnapshot()})
}

func (s *Server) handleBucketCreate(w http.ResponseWriter, r *http.Request) {
	var req httpapi.BucketCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "bucket id is required")
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

	writeJSON(w, http.StatusCreated, &httpapi.BucketResponse{Bucket: bucketToResponse(meta)})
}

func (s *Server) handleBucketList(w http.ResponseWriter, r *http.Request) {
	graphs, err := s.gm.ListGraphs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := &httpapi.BucketListResponse{Buckets: make([]*httpapi.Bucket, len(graphs))}
	for i, meta := range graphs {
		resp.Buckets[i] = bucketToResponse(meta)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleBucketGet(w http.ResponseWriter, r *http.Request) {
	meta, err := s.gm.GetGraph(r.Context(), r.PathValue("id"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, graph.ErrNotFound) || errors.Is(err, graph.ErrDeleted) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, &httpapi.BucketResponse{Bucket: bucketToResponse(meta)})
}

func (s *Server) handleBucketDelete(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handleBucketFreeze(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handleBucketCreateStructure(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handleBucketHeadSet(w http.ResponseWriter, r *http.Request) {
	bucketID := r.PathValue("id")

	// Require active bucket.
	meta, g, err := s.openManagedGraph(r.Context(), bucketID, true)
	if err != nil {
		writeManagedGraphError(w, err)
		return
	}

	var req httpapi.BucketHeadSetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}
	if req.NewRoot == "" {
		writeError(w, http.StatusBadRequest, "new_root is required")
		return
	}
	if req.ArcCount < 0 {
		writeError(w, http.StatusBadRequest, "arc_count must be non-negative")
		return
	}

	newRoot, err := decodeCID(req.NewRoot)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if codec.SemanticKindOf(newRoot) != codec.SemanticKindMap {
		writeError(w, http.StatusBadRequest, "new_root must be a map root")
		return
	}
	if _, err := mandatoryMapPayload(r.Context(), g, newRoot); err != nil {
		if !s.validUnixFSRoot(r.Context(), g, newRoot) {
			if errors.Is(err, writer.ErrArcNotFound) {
				writeError(w, http.StatusBadRequest, "new_root must resolve to a map root in this bucket with a mandatory @payload binding")
				return
			}
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	if req.ExpectedOldRoot != "" {
		expected, err := decodeCID(req.ExpectedOldRoot)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// If current head is undefined, this must still match.
		if meta.Root != expected {
			writeError(w, http.StatusConflict, "stale expected_old_root")
			return
		}
	}

	oldRoot := meta.Root
	if err := s.updateManagedGraphHead(r.Context(), bucketID, newRoot, req.ArcCount); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Record lineage for this head transition (best effort).
	if lm := s.node.LineageManager(); lm != nil {
		_ = lm.Record(r.Context(), newRoot, oldRoot, req.ArcCount)
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleBucketSemanticMutation(w http.ResponseWriter, r *http.Request) {
	bucketID := r.PathValue("id")
	meta, g, err := s.openManagedGraph(r.Context(), bucketID, true)
	if err != nil {
		writeManagedGraphError(w, err)
		return
	}

	var req httpapi.BucketSemanticMutationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	baseRoot := meta.Root
	if req.BaseRoot != "" {
		baseRoot, err = decodeCID(req.BaseRoot)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		if meta.Root != baseRoot {
			writeError(w, http.StatusConflict, "stale base_root")
			return
		}
	}

	mut, err := semanticMutationFromRequest(bucketID, baseRoot, &req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(mut.Puts) == 0 || mut.Puts[len(mut.Puts)-1].Kind != arcset.KindMap {
		writeError(w, http.StatusBadRequest, "semantic mutation result must be a map bucket head")
		return
	}

	exec := gateway.Executor{
		Maps:     g.Semantic(),
		Lists:    g.ListSemantic(),
		ArcTable: s.node.ArcTable(),
	}
	receipt, err := exec.Apply(r.Context(), mut)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if codec.SemanticKindOf(receipt.NewRoot) != codec.SemanticKindMap {
		writeError(w, http.StatusBadRequest, "semantic mutation result must be a map bucket head")
		return
	}
	if _, err := mandatoryMapPayload(r.Context(), g, receipt.NewRoot); err != nil {
		if errors.Is(err, writer.ErrArcNotFound) {
			writeError(w, http.StatusBadRequest, "semantic mutation result must include a mandatory @payload binding")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if err := s.updateManagedGraphHead(r.Context(), bucketID, receipt.NewRoot, receipt.ArcCount); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if lm := s.node.LineageManager(); lm != nil {
		_ = lm.Record(r.Context(), receipt.NewRoot, receipt.BaseRoot, receipt.ArcCount)
	}

	writeJSON(w, http.StatusCreated, &httpapi.BucketSemanticMutationResponse{
		Bucket:   receipt.BucketID,
		BaseRoot: receipt.BaseRoot.String(),
		NewRoot:  receipt.NewRoot.String(),
		PutCount: receipt.PutCount,
		ArcCount: receipt.ArcCount,
	})
}

func (s *Server) handleBucketMapsCreate(w http.ResponseWriter, r *http.Request) {
	_, g, err := s.openManagedGraph(r.Context(), r.PathValue("id"), true)
	if err != nil {
		writeManagedGraphError(w, err)
		return
	}

	var req httpapi.BucketMapCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}
	if len(req.Bindings) == 0 {
		writeError(w, http.StatusBadRequest, "bindings is required")
		return
	}

	parsed, err := parseArcMap(req.Bindings)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	snapshot, _, err := buildCreateSnapshot(parsed)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	root, err := g.Writer().CreateStructure(r.Context(), g.BucketId(), snapshot)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, &httpapi.BucketMapCreateResponse{Root: root.String()})
}

func (s *Server) handleBucketMapsSnapshot(w http.ResponseWriter, r *http.Request) {
	_, g, err := s.openManagedGraph(r.Context(), r.PathValue("id"), false)
	if err != nil {
		writeManagedGraphError(w, err)
		return
	}
	root, err := decodeCID(r.PathValue("root"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if codec.SemanticKindOf(root) != codec.SemanticKindMap {
		writeError(w, http.StatusBadRequest, "root must be a map root")
		return
	}

	snapshot, err := g.Snapshot(r.Context(), root)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	bindings, _, err := snapshotToMap(snapshot)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, &httpapi.BucketMapSnapshotResponse{
		Root:     root.String(),
		Bindings: bindings,
	})
}

func (s *Server) handleBucketMapsResolve(w http.ResponseWriter, r *http.Request) {
	_, g, err := s.openManagedGraph(r.Context(), r.PathValue("id"), false)
	if err != nil {
		writeManagedGraphError(w, err)
		return
	}
	root, err := decodeCID(r.PathValue("root"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if codec.SemanticKindOf(root) != codec.SemanticKindMap {
		writeError(w, http.StatusBadRequest, "root must be a map root")
		return
	}

	path := bucketpath.CanonicalizeQueryPath(r.URL.Query().Get("path"))
	result, err := g.Resolver().ResolveKey(root, path)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, resolver.ErrResolutionFailed) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	if resolveMiss(root, path, result) {
		writeError(w, http.StatusNotFound, errPathNotFound.Error())
		return
	}
	writeJSON(w, http.StatusOK, &httpapi.BucketMapResolveResponse{Key: result.Target.String()})
}

func (s *Server) handleBucketMapsUpdate(w http.ResponseWriter, r *http.Request) {
	_, g, err := s.openManagedGraph(r.Context(), r.PathValue("id"), true)
	if err != nil {
		writeManagedGraphError(w, err)
		return
	}
	root, err := decodeCID(r.PathValue("root"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if codec.SemanticKindOf(root) != codec.SemanticKindMap {
		writeError(w, http.StatusBadRequest, "root must be a map root")
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
	if err := validatePayloadUpdate(req.Path, target); err != nil {
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

func (s *Server) handleBucketMapsBatchUpdate(w http.ResponseWriter, r *http.Request) {
	_, g, err := s.openManagedGraph(r.Context(), r.PathValue("id"), true)
	if err != nil {
		writeManagedGraphError(w, err)
		return
	}
	root, err := decodeCID(r.PathValue("root"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if codec.SemanticKindOf(root) != codec.SemanticKindMap {
		writeError(w, http.StatusBadRequest, "root must be a map root")
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
	if err := validatePayloadBatchUpdates(parsedUpdates); err != nil {
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

func (s *Server) handleBucketListsCreate(w http.ResponseWriter, r *http.Request) {
	_, g, err := s.openManagedGraph(r.Context(), r.PathValue("id"), true)
	if err != nil {
		writeManagedGraphError(w, err)
		return
	}

	var req httpapi.BucketListCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}
	if req.ChunkSize != fixedBucketListChunkSize {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("chunk_size must be %d", fixedBucketListChunkSize))
		return
	}

	chunks := make([]cid.Cid, len(req.Chunks))
	for i, raw := range req.Chunks {
		c, err := decodeCID(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid chunk cid at index %d: %v", i, err))
			return
		}
		chunks[i] = c
	}

	root, err := g.ListSemantic().Commit(r.Context(), g.BucketId(), list.NewViewFromSlice(chunks))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, &httpapi.BucketListStatResponse{
		Root:       root.String(),
		ChunkCount: len(chunks),
		ChunkSize:  fixedBucketListChunkSize,
	})
}

func (s *Server) handleBucketListsGet(w http.ResponseWriter, r *http.Request) {
	_, g, err := s.openManagedGraph(r.Context(), r.PathValue("id"), false)
	if err != nil {
		writeManagedGraphError(w, err)
		return
	}
	root, err := decodeCID(r.PathValue("root"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if codec.SemanticKindOf(root) != codec.SemanticKindList {
		writeError(w, http.StatusBadRequest, "root must be a list root")
		return
	}

	// Query out-of-range to get authenticated length.
	q, _, err := g.ListSemantic().Prove(r.Context(), g.BucketId(), root, ^uint64(0))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, &httpapi.BucketListStatResponse{
		Root:       root.String(),
		ChunkCount: int(q.Length),
		ChunkSize:  fixedBucketListChunkSize,
	})
}

func (s *Server) handleBucketUnixFSFile(w http.ResponseWriter, r *http.Request) {
	graphID := r.PathValue("id")
	meta, g, err := s.openManagedGraph(r.Context(), graphID, true)
	if err != nil {
		writeManagedGraphError(w, err)
		return
	}

	p := bucketpath.CanonicalizeQueryPath(r.URL.Query().Get("path"))
	if p == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("read request body: %v", err))
		return
	}

	layout, err := s.unixFSLayout(g)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	oldRoot := cid.Undef
	if meta != nil {
		oldRoot = meta.Root
	}
	baseRoot, err := s.prepareUnixFSRoot(r.Context(), g, layout, oldRoot)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	newRoot, err := layout.AddFile(r.Context(), baseRoot, p, data)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	receipt, err := s.applyUnixFSGatewayMutation(r.Context(), graphID, g, layout, oldRoot, newRoot)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.publishUnixFSReceipt(r.Context(), graphID, oldRoot, receipt); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := &httpapi.BucketUnixFSWriteResponse{
		Bucket:   graphID,
		Path:     p,
		Kind:     "file",
		NewRoot:  receipt.NewRoot.String(),
		ArcCount: receipt.ArcCount,
	}
	if oldRoot.Defined() {
		resp.OldRoot = oldRoot.String()
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleBucketUnixFSDirectory(w http.ResponseWriter, r *http.Request) {
	graphID := r.PathValue("id")
	meta, g, err := s.openManagedGraph(r.Context(), graphID, true)
	if err != nil {
		writeManagedGraphError(w, err)
		return
	}

	p := bucketpath.CanonicalizeQueryPath(r.URL.Query().Get("path"))
	layout, err := s.unixFSLayout(g)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	oldRoot := cid.Undef
	if meta != nil {
		oldRoot = meta.Root
	}
	baseRoot, err := s.prepareUnixFSRoot(r.Context(), g, layout, oldRoot)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	newRoot, err := layout.AddDirectory(r.Context(), baseRoot, p)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	receipt, err := s.applyUnixFSGatewayMutation(r.Context(), graphID, g, layout, oldRoot, newRoot)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.publishUnixFSReceipt(r.Context(), graphID, oldRoot, receipt); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	resp := &httpapi.BucketUnixFSWriteResponse{
		Bucket:   graphID,
		Path:     p,
		Kind:     "dir",
		NewRoot:  receipt.NewRoot.String(),
		ArcCount: receipt.ArcCount,
	}
	if oldRoot.Defined() {
		resp.OldRoot = oldRoot.String()
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleBucketStat(w http.ResponseWriter, r *http.Request) {
	meta, g, err := s.openManagedGraph(r.Context(), r.PathValue("id"), false)
	if err != nil {
		writeManagedGraphError(w, err)
		return
	}
	if meta == nil || !meta.Root.Defined() {
		writeError(w, http.StatusNotFound, "path not found")
		return
	}

	path := bucketpath.CanonicalizeQueryPath(r.URL.Query().Get("path"))
	stat, err := s.bucketStat(r.Context(), g, meta.Root, path)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errPathNotFound) || errors.Is(err, resolver.ErrResolutionFailed) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, stat)
}

func (s *Server) handleBucketContent(w http.ResponseWriter, r *http.Request) {
	meta, g, err := s.openManagedGraph(r.Context(), r.PathValue("id"), false)
	if err != nil {
		writeManagedGraphError(w, err)
		return
	}
	if meta == nil || !meta.Root.Defined() {
		writeError(w, http.StatusNotFound, "path not found")
		return
	}

	path := bucketpath.CanonicalizeQueryPath(r.URL.Query().Get("path"))
	stat, err := s.bucketStat(r.Context(), g, meta.Root, path)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errPathNotFound) || errors.Is(err, resolver.ErrResolutionFailed) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	if stat.Kind != "file" {
		writeError(w, httpapi.StatusBucketContentIsDirectory, "content is only valid for file targets")
		return
	}
	key, err := decodeCID(stat.Key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	totalSize := int64(0)
	if stat.Size != nil {
		totalSize = *stat.Size
	}

	start, endExclusive, partial, err := parseRangeHeader(r.Header.Get("Range"), totalSize)
	if err != nil {
		writeError(w, httpapi.StatusBucketRangeNotSatisfiable, err.Error())
		return
	}

	var payload []byte
	payload, err = s.readBucketContentPayload(r.Context(), g, stat, key, start, endExclusive)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	w.Header().Set("Accept-Ranges", "bytes")
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, endExclusive-1, totalSize))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	_, _ = io.Copy(w, bytes.NewReader(payload))
}

func (s *Server) handleBucketContentProof(w http.ResponseWriter, r *http.Request) {
	meta, g, err := s.openManagedGraph(r.Context(), r.PathValue("id"), false)
	if err != nil {
		writeManagedGraphError(w, err)
		return
	}
	if meta == nil || !meta.Root.Defined() {
		writeError(w, http.StatusNotFound, "path not found")
		return
	}

	queryPath := bucketpath.CanonicalizeQueryPath(r.URL.Query().Get("path"))
	stat, err := s.bucketStat(r.Context(), g, meta.Root, queryPath)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errPathNotFound) || errors.Is(err, resolver.ErrResolutionFailed) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	if stat.Kind != "file" {
		writeError(w, httpapi.StatusBucketContentIsDirectory, "content proof is only valid for file targets")
		return
	}
	key, err := decodeCID(stat.Key)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	totalSize := int64(0)
	if stat.Size != nil {
		totalSize = *stat.Size
	}

	start, endExclusive, partial, err := parseRangeHeader(r.Header.Get("Range"), totalSize)
	if err != nil {
		writeError(w, httpapi.StatusBucketRangeNotSatisfiable, err.Error())
		return
	}
	payload, err := s.readBucketContentPayload(r.Context(), g, stat, key, start, endExclusive)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pl, err := s.contentProofList(r.Context(), g, meta.Root, queryPath, stat, start, endExclusive)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errPathNotFound) || errors.Is(err, resolver.ErrResolutionFailed) || errors.Is(err, unixfs.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}

	w.Header().Set("Accept-Ranges", "bytes")
	writeJSON(w, http.StatusOK, &httpapi.BucketContentProofResponse{
		Path:        queryPath,
		StorageKind: stat.StorageKind,
		Key:         stat.Key,
		Content:     payload,
		Range:       contentRangeMetadata(start, endExclusive, totalSize, partial),
		ProofList:   *pl,
	})
}

func (s *Server) handleBucketResolve(w http.ResponseWriter, r *http.Request) {
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
	s.resolveAndWrite(w, r.Context(), g, root, r.URL.Query().Get("path"))
}

func (s *Server) handleBucketProof(w http.ResponseWriter, r *http.Request) {
	s.handleBucketResolve(w, r)
}

func (s *Server) handleBucketProofList(w http.ResponseWriter, r *http.Request) {
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
	s.proofListAndWrite(w, r.Context(), g, root, r.URL.Query().Get("path"))
}

func (s *Server) handleBucketSnapshot(w http.ResponseWriter, r *http.Request) {
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

func (s *Server) handleBucketUpdate(w http.ResponseWriter, r *http.Request) {
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
	if err := validatePayloadUpdate(req.Path, target); err != nil {
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

func (s *Server) handleBucketBatchUpdate(w http.ResponseWriter, r *http.Request) {
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
	if err := validatePayloadBatchUpdates(parsedUpdates); err != nil {
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
	s.resolveAndWrite(w, r.Context(), g, root, r.URL.Query().Get("path"))
}

func (s *Server) handleRootProof(w http.ResponseWriter, r *http.Request) {
	s.handleRootResolve(w, r)
}

func (s *Server) handleRootProofList(w http.ResponseWriter, r *http.Request) {
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
	s.proofListAndWrite(w, r.Context(), g, root, r.URL.Query().Get("path"))
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
	if err := validatePayloadUpdate(req.Path, target); err != nil {
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
	if err := validatePayloadBatchUpdates(parsedUpdates); err != nil {
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

func (s *Server) resolveAndWrite(w http.ResponseWriter, ctx context.Context, g *graph.Graph, root cid.Cid, rawPath string) {
	if resp, err := s.unixFSResolve(ctx, g, root, rawPath); err == nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}

	result, err := g.Resolver().Resolve(root, rawPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, &httpapi.ResolveResponse{
		Target:     result.Target.String(),
		Transcript: encodeTranscript(result.Transcript),
	})
}

func (s *Server) proofListAndWrite(w http.ResponseWriter, ctx context.Context, g *graph.Graph, root cid.Cid, rawPath string) {
	result, err := g.Resolver().Resolve(root, rawPath)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pl, err := resolver.ProofListFromTranscript(root, result.Transcript)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	pl.Query = bucketpath.CanonicalizeQueryPath(rawPath)
	s.node.RecordProofList(*pl)
	writeJSON(w, http.StatusOK, &httpapi.ProofListResponse{
		Target:    result.Target.String(),
		ProofList: *pl,
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

var errPathNotFound = errors.New("path not found")

func (s *Server) bucketStat(ctx context.Context, g *graph.Graph, root cid.Cid, path string) (*httpapi.BucketStatResponse, error) {
	if stat, err := s.unixFSBucketStat(ctx, g, root, path); err == nil {
		return stat, nil
	}
	return s.legacyBucketStat(ctx, g, root, path)
}

func (s *Server) legacyBucketStat(ctx context.Context, g *graph.Graph, root cid.Cid, path string) (*httpapi.BucketStatResponse, error) {
	keyResult, err := g.Resolver().ResolveKey(root, path)
	if err != nil {
		return nil, err
	}
	key := keyResult.Target

	// Treat "no movement from root with non-empty path" as not found.
	if path != "" && key.Equals(root) && len(keyResult.Transcript.Steps) == 0 {
		return nil, errPathNotFound
	}
	if !key.Defined() {
		return nil, errPathNotFound
	}

	switch codec.SemanticKindOf(key) {
	case codec.SemanticKindMap:
		payload, err := mandatoryMapPayload(ctx, g, key)
		if err != nil {
			return nil, err
		}
		resp := &httpapi.BucketStatResponse{
			Kind:        "dir",
			StorageKind: "map",
			Key:         key.String(),
			Payload:     payload.String(),
		}
		return resp, nil
	case codec.SemanticKindList:
		size, _, err := s.listFileSize(ctx, g, key)
		if err != nil {
			return nil, err
		}
		return &httpapi.BucketStatResponse{
			Kind:        "file",
			StorageKind: "list",
			Key:         key.String(),
			Size:        &size,
		}, nil
	default:
		// Non-MALT key is treated as raw file content.
		data, err := s.node.CAS().Get(ctx, key)
		if err != nil {
			return nil, errPathNotFound
		}
		size := int64(len(data))
		return &httpapi.BucketStatResponse{
			Kind:        "file",
			StorageKind: "raw",
			Key:         key.String(),
			Size:        &size,
		}, nil
	}
}

func (s *Server) unixFSLayout(g *graph.Graph) (*unixfs.Layout, error) {
	blocks, ok := s.node.CAS().(cas.Client)
	if !ok {
		return nil, fmt.Errorf("configured CAS does not support writes")
	}
	return unixfs.New(unixfs.Options{
		BucketID:  g.BucketId(),
		ChunkSize: fixedBucketListChunkSize,
		Map:       g.Semantic(),
		List:      g.ListSemantic(),
		Blocks:    blocks,
	})
}

func (s *Server) prepareUnixFSRoot(ctx context.Context, g *graph.Graph, layout *unixfs.Layout, root cid.Cid) (cid.Cid, error) {
	if !root.Defined() {
		return cid.Undef, nil
	}
	if stat, err := layout.Stat(ctx, root, ""); err == nil && stat.Kind == "directory" {
		return root, nil
	}
	return s.migrateLegacyTreeToUnixFS(ctx, g, layout, root)
}

func (s *Server) applyUnixFSGatewayMutation(ctx context.Context, bucketID string, g *graph.Graph, layout *unixfs.Layout, oldRoot cid.Cid, newRoot cid.Cid) (gateway.WriteReceipt, error) {
	baseRoot := oldRoot
	if !baseRoot.Defined() {
		baseRoot = newRoot
	}

	plan, err := layout.MutationPlanForRoot(ctx, oldRoot, newRoot)
	if err != nil {
		return gateway.WriteReceipt{}, err
	}
	mut := gateway.SemanticMutation{
		BucketID: bucketID,
		BaseRoot: baseRoot,
		Puts:     make([]gateway.ArcSetPut, 0, len(plan.Puts)),
	}
	for _, put := range plan.Puts {
		// UnixFS replay rematerializes deterministic roots; do not treat the
		// already-materialized object as a versioned ArcTable parent.
		mut.Puts = append(mut.Puts, gateway.ArcSetPut{
			Object: cid.Undef,
			Kind:   put.Kind,
			ArcSet: put.ArcSet,
		})
	}

	exec := gateway.Executor{
		Maps:     g.Semantic(),
		Lists:    g.ListSemantic(),
		ArcTable: s.node.ArcTable(),
	}
	receipt, err := exec.Apply(ctx, mut)
	if err != nil {
		return gateway.WriteReceipt{}, err
	}
	if codec.SemanticKindOf(receipt.NewRoot) != codec.SemanticKindMap {
		return gateway.WriteReceipt{}, fmt.Errorf("unixfs mutation result must be a map bucket head")
	}
	return receipt, nil
}

func (s *Server) publishUnixFSReceipt(ctx context.Context, graphID string, oldRoot cid.Cid, receipt gateway.WriteReceipt) error {
	if err := s.updateManagedGraphHead(ctx, graphID, receipt.NewRoot, receipt.ArcCount); err != nil {
		return err
	}
	if lm := s.node.LineageManager(); lm != nil {
		_ = lm.Record(ctx, receipt.NewRoot, oldRoot, receipt.ArcCount)
	}
	return nil
}

func (s *Server) migrateLegacyTreeToUnixFS(ctx context.Context, g *graph.Graph, layout *unixfs.Layout, legacyRoot cid.Cid) (cid.Cid, error) {
	return s.copyLegacyPathToUnixFS(ctx, g, layout, legacyRoot, "", cid.Undef, "")
}

func (s *Server) copyLegacyPathToUnixFS(ctx context.Context, g *graph.Graph, layout *unixfs.Layout, legacyRoot cid.Cid, legacyPath string, unixRoot cid.Cid, unixPath string) (cid.Cid, error) {
	stat, err := s.legacyBucketStat(ctx, g, legacyRoot, legacyPath)
	if err != nil {
		return cid.Undef, fmt.Errorf("migrate legacy path %q: %w", legacyPath, err)
	}

	switch stat.Kind {
	case "dir":
		unixRoot, err = layout.AddDirectory(ctx, unixRoot, unixPath)
		if err != nil {
			return cid.Undef, err
		}
		entries, err := s.directoryEntriesFromStat(ctx, stat)
		if err != nil {
			return cid.Undef, fmt.Errorf("read legacy directory %q: %w", legacyPath, err)
		}
		for _, entry := range entries {
			childLegacyPath := entry
			if legacyPath != "" {
				childLegacyPath = path.Join(legacyPath, entry)
			}
			childUnixPath := entry
			if unixPath != "" {
				childUnixPath = path.Join(unixPath, entry)
			}
			unixRoot, err = s.copyLegacyPathToUnixFS(ctx, g, layout, legacyRoot, childLegacyPath, unixRoot, childUnixPath)
			if err != nil {
				return cid.Undef, err
			}
		}
		return unixRoot, nil
	case "file":
		if unixPath == "" {
			return cid.Undef, fmt.Errorf("legacy root file cannot be migrated into a UnixFS bucket root")
		}
		data, err := s.readStatFile(ctx, g, stat)
		if err != nil {
			return cid.Undef, fmt.Errorf("read legacy file %q: %w", legacyPath, err)
		}
		return layout.AddFile(ctx, unixRoot, unixPath, data)
	default:
		return cid.Undef, fmt.Errorf("unsupported legacy path kind %q", stat.Kind)
	}
}

func (s *Server) directoryEntriesFromStat(ctx context.Context, stat *httpapi.BucketStatResponse) ([]string, error) {
	if stat == nil || stat.Kind != "dir" {
		return nil, fmt.Errorf("directory stat is required")
	}
	if stat.Entries != nil {
		return stat.Entries, nil
	}
	if stat.Payload == "" {
		return nil, nil
	}
	payloadCID, err := cid.Decode(stat.Payload)
	if err != nil {
		return nil, err
	}
	data, err := s.node.CAS().Get(ctx, payloadCID)
	if err != nil {
		return nil, err
	}
	m, err := manifest.ParseDirectoryJSON(data)
	if err != nil {
		return nil, err
	}
	return m.Entries, nil
}

func (s *Server) readStatFile(ctx context.Context, g *graph.Graph, stat *httpapi.BucketStatResponse) ([]byte, error) {
	if stat == nil || stat.Kind != "file" {
		return nil, fmt.Errorf("file stat is required")
	}
	key, err := decodeCID(stat.Key)
	if err != nil {
		return nil, err
	}
	switch stat.StorageKind {
	case "raw":
		return s.node.CAS().Get(ctx, key)
	case "list":
		size := int64(0)
		if stat.Size != nil {
			size = *stat.Size
		} else {
			size, _, err = s.listFileSize(ctx, g, key)
			if err != nil {
				return nil, err
			}
		}
		return s.readListRange(ctx, g, key, 0, size)
	default:
		return nil, fmt.Errorf("unsupported file storage kind %q", stat.StorageKind)
	}
}

func (s *Server) readBucketContentPayload(ctx context.Context, g *graph.Graph, stat *httpapi.BucketStatResponse, key cid.Cid, start, endExclusive int64) ([]byte, error) {
	switch stat.StorageKind {
	case "raw":
		raw, err := s.node.CAS().Get(ctx, key)
		if err != nil {
			return nil, err
		}
		return raw[start:endExclusive], nil
	case "list":
		return s.readListRange(ctx, g, key, start, endExclusive)
	default:
		return nil, fmt.Errorf("unsupported storage kind for content")
	}
}

func (s *Server) contentProofList(ctx context.Context, g *graph.Graph, root cid.Cid, queryPath string, stat *httpapi.BucketStatResponse, start, endExclusive int64) (*prooflist.ProofList, error) {
	if layout, err := s.unixFSLayout(g); err == nil {
		if unixStat, statErr := layout.Stat(ctx, root, queryPath); statErr == nil && unixStat.Kind == "file" {
			return s.unixFSContentProofList(ctx, layout, root, queryPath, stat, start, endExclusive)
		}
	}
	return s.legacyContentProofList(ctx, g, root, queryPath, stat, start, endExclusive)
}

func (s *Server) unixFSContentProofList(ctx context.Context, layout *unixfs.Layout, root cid.Cid, queryPath string, stat *httpapi.BucketStatResponse, start, endExclusive int64) (*prooflist.ProofList, error) {
	resolution, err := layout.Resolve(ctx, root, queryPath)
	if err != nil {
		return nil, err
	}
	pl, err := unixfs.ProofListFromSteps(root, queryPath, resolution.Steps)
	if err != nil {
		return nil, err
	}
	if stat.StorageKind == "list" && endExclusive > start {
		steps, err := layout.ListIndexStepsForFileRange(ctx, root, queryPath, uint64(start), uint64(endExclusive-start))
		if err != nil {
			return nil, err
		}
		if err := unixfs.AppendListIndexSteps(pl, queryPath, steps); err != nil {
			return nil, err
		}
	}
	return pl, nil
}

func (s *Server) legacyContentProofList(ctx context.Context, g *graph.Graph, root cid.Cid, queryPath string, stat *httpapi.BucketStatResponse, start, endExclusive int64) (*prooflist.ProofList, error) {
	result, err := g.Resolver().Resolve(root, queryPath)
	if err != nil {
		return nil, err
	}
	if resolveMiss(root, queryPath, result) {
		return nil, errPathNotFound
	}
	pl, err := resolver.ProofListFromTranscript(root, result.Transcript)
	if err != nil {
		return nil, err
	}
	pl.Query = queryPath
	if stat.StorageKind == "list" && endExclusive > start {
		listRoot, err := decodeCID(stat.Key)
		if err != nil {
			return nil, err
		}
		steps, err := s.listIndexStepsForRange(ctx, g, listRoot, start, endExclusive)
		if err != nil {
			return nil, err
		}
		if err := unixfs.AppendListIndexSteps(pl, queryPath, steps); err != nil {
			return nil, err
		}
	}
	return pl, nil
}

func (s *Server) unixFSBucketStat(ctx context.Context, g *graph.Graph, root cid.Cid, p string) (*httpapi.BucketStatResponse, error) {
	layout, err := s.unixFSLayout(g)
	if err != nil {
		return nil, err
	}
	stat, err := layout.Stat(ctx, root, p)
	if err != nil {
		return nil, err
	}

	switch stat.Kind {
	case "directory":
		return &httpapi.BucketStatResponse{
			Kind:        "dir",
			StorageKind: "map",
			Key:         stat.NodeRoot.String(),
			Payload:     stat.Payload.String(),
			Entries:     stat.Entries,
		}, nil
	case "file":
		size := int64(stat.Size)
		return &httpapi.BucketStatResponse{
			Kind:        "file",
			StorageKind: stat.StorageKind,
			Key:         stat.Payload.String(),
			Payload:     stat.Payload.String(),
			Size:        &size,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported unixfs node kind %q", stat.Kind)
	}
}

func (s *Server) unixFSResolve(ctx context.Context, g *graph.Graph, root cid.Cid, rawPath string) (*httpapi.ResolveResponse, error) {
	layout, err := s.unixFSLayout(g)
	if err != nil {
		return nil, err
	}
	resolution, err := layout.Resolve(ctx, root, bucketpath.CanonicalizeQueryPath(rawPath))
	if err != nil {
		return nil, err
	}

	steps := make([]httpapi.StepEvidence, len(resolution.Steps))
	for i, step := range resolution.Steps {
		steps[i] = httpapi.StepEvidence{
			Path:     step.Path.String(),
			Target:   step.Target.String(),
			Evidence: base64.StdEncoding.EncodeToString([]byte(step.Proof)),
			Kind:     "explicit",
		}
	}
	return &httpapi.ResolveResponse{
		Target:     resolution.Payload.String(),
		Transcript: steps,
	}, nil
}

func (s *Server) validUnixFSRoot(ctx context.Context, g *graph.Graph, root cid.Cid) bool {
	layout, err := s.unixFSLayout(g)
	if err != nil {
		return false
	}
	stat, err := layout.Stat(ctx, root, "")
	return err == nil && stat.Kind == "directory"
}

func (s *Server) unixFSArcCount(ctx context.Context, layout *unixfs.Layout, root cid.Cid) int {
	count, err := unixFSArcCountAt(ctx, layout, root, "")
	if err != nil {
		return 0
	}
	return count
}

func unixFSArcCountAt(ctx context.Context, layout *unixfs.Layout, root cid.Cid, p string) (int, error) {
	stat, err := layout.Stat(ctx, root, p)
	if err != nil {
		return 0, err
	}
	switch stat.Kind {
	case "file":
		return 4, nil
	case "directory":
		count := 2 + len(stat.Entries)
		for _, entry := range stat.Entries {
			childPath := entry
			if p != "" {
				childPath = path.Join(p, entry)
			}
			childCount, err := unixFSArcCountAt(ctx, layout, root, childPath)
			if err != nil {
				return 0, err
			}
			count += childCount
		}
		return count, nil
	default:
		return 0, fmt.Errorf("unsupported unixfs node kind %q", stat.Kind)
	}
}

func (s *Server) listFileSize(ctx context.Context, g *graph.Graph, listRoot cid.Cid) (int64, uint64, error) {
	q, _, err := g.ListSemantic().Prove(ctx, g.BucketId(), listRoot, ^uint64(0))
	if err != nil {
		return 0, 0, err
	}
	count := q.Length
	if count == 0 {
		return 0, 0, nil
	}
	last, _, err := g.ListSemantic().Prove(ctx, g.BucketId(), listRoot, count-1)
	if err != nil {
		return 0, 0, err
	}
	lastChunk, err := s.node.CAS().Get(ctx, last.Key)
	if err != nil {
		return 0, 0, err
	}
	size := int64((count-1)*uint64(fixedBucketListChunkSize) + uint64(len(lastChunk)))
	return size, count, nil
}

func (s *Server) readListRange(ctx context.Context, g *graph.Graph, listRoot cid.Cid, start, endExclusive int64) ([]byte, error) {
	if endExclusive <= start {
		return []byte{}, nil
	}
	first := uint64(start / fixedBucketListChunkSize)
	last := uint64((endExclusive - 1) / fixedBucketListChunkSize)
	var out bytes.Buffer
	for i := first; i <= last; i++ {
		q, _, err := g.ListSemantic().Prove(ctx, g.BucketId(), listRoot, i)
		if err != nil {
			return nil, err
		}
		chunk, err := s.node.CAS().Get(ctx, q.Key)
		if err != nil {
			return nil, err
		}
		chunkStart := int64(i) * fixedBucketListChunkSize
		localStart := int64(0)
		if start > chunkStart {
			localStart = start - chunkStart
		}
		localEnd := int64(len(chunk))
		if endExclusive < chunkStart+localEnd {
			localEnd = endExclusive - chunkStart
		}
		if localStart < 0 {
			localStart = 0
		}
		if localEnd < localStart {
			localEnd = localStart
		}
		out.Write(chunk[localStart:localEnd])
	}
	return out.Bytes(), nil
}

func (s *Server) listIndexStepsForRange(ctx context.Context, g *graph.Graph, listRoot cid.Cid, start, endExclusive int64) ([]unixfs.ListIndexStep, error) {
	if endExclusive <= start {
		return nil, nil
	}
	first := uint64(start / fixedBucketListChunkSize)
	last := uint64((endExclusive - 1) / fixedBucketListChunkSize)
	steps := make([]unixfs.ListIndexStep, 0, last-first+1)
	for index := first; index <= last; index++ {
		query, proof, err := g.ListSemantic().Prove(ctx, g.BucketId(), listRoot, index)
		if err != nil {
			return nil, err
		}
		ok, err := g.ListSemantic().Verify(listRoot, index, query, proof)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("list proof failed at index %d", index)
		}
		if !query.Key.Defined() {
			return nil, fmt.Errorf("missing chunk %d", index)
		}
		steps = append(steps, unixfs.ListIndexStep{
			Root:   listRoot,
			Index:  index,
			Target: query.Key,
			Proof:  proof,
		})
	}
	return steps, nil
}

func contentRangeMetadata(start, endExclusive, totalSize int64, partial bool) httpapi.BucketContentRange {
	statusCode := http.StatusOK
	contentRange := ""
	if partial {
		statusCode = http.StatusPartialContent
		contentRange = fmt.Sprintf("bytes %d-%d/%d", start, endExclusive-1, totalSize)
	}
	return httpapi.BucketContentRange{
		Start:         start,
		EndExclusive:  endExclusive,
		ContentLength: endExclusive - start,
		TotalSize:     totalSize,
		Partial:       partial,
		StatusCode:    statusCode,
		AcceptRanges:  "bytes",
		ContentRange:  contentRange,
	}
}

func parseRangeHeader(raw string, size int64) (start int64, endExclusive int64, partial bool, err error) {
	if size < 0 {
		return 0, 0, false, fmt.Errorf("invalid size")
	}
	if raw == "" {
		return 0, size, false, nil
	}
	if !strings.HasPrefix(raw, "bytes=") {
		return 0, 0, false, fmt.Errorf("invalid range")
	}
	spec := strings.TrimPrefix(raw, "bytes=")
	if strings.Contains(spec, ",") {
		return 0, 0, false, fmt.Errorf("multiple ranges are not supported")
	}
	parts := strings.SplitN(spec, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false, fmt.Errorf("invalid range")
	}
	switch {
	case parts[0] == "":
		// suffix bytes: bytes=-N
		suffix, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || suffix <= 0 {
			return 0, 0, false, fmt.Errorf("invalid range")
		}
		if size == 0 {
			return 0, 0, false, fmt.Errorf("unsatisfiable range")
		}
		if suffix > size {
			suffix = size
		}
		return size - suffix, size, true, nil
	default:
		start, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || start < 0 {
			return 0, 0, false, fmt.Errorf("invalid range")
		}
		if start >= size {
			return 0, 0, false, fmt.Errorf("unsatisfiable range")
		}
		if parts[1] == "" {
			return start, size, true, nil
		}
		end, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || end < start {
			return 0, 0, false, fmt.Errorf("invalid range")
		}
		if end >= size {
			end = size - 1
		}
		return start, end + 1, true, nil
	}
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

func semanticMutationFromRequest(bucketID string, baseRoot cid.Cid, req *httpapi.BucketSemanticMutationRequest) (gateway.SemanticMutation, error) {
	if req == nil {
		return gateway.SemanticMutation{}, fmt.Errorf("request is required")
	}

	puts := make([]gateway.ArcSetPut, 0, len(req.Puts))
	for i, putReq := range req.Puts {
		put, err := semanticPutFromRequest(putReq)
		if err != nil {
			return gateway.SemanticMutation{}, fmt.Errorf("put %d: %w", i, err)
		}
		puts = append(puts, put)
	}

	return gateway.SemanticMutation{
		BucketID: bucketID,
		BaseRoot: baseRoot,
		Puts:     puts,
	}, nil
}

func semanticPutFromRequest(req httpapi.SemanticMutationPut) (gateway.ArcSetPut, error) {
	object := cid.Undef
	if req.Object != "" {
		parsed, err := decodeCID(req.Object)
		if err != nil {
			return gateway.ArcSetPut{}, fmt.Errorf("invalid object: %w", err)
		}
		object = parsed
	}

	kind := arcset.Kind(req.Kind)
	entries := make([]arcset.ArcEntry, 0, len(req.Entries))
	for i, entryReq := range req.Entries {
		entry, err := semanticEntryFromRequest(kind, entryReq)
		if err != nil {
			return gateway.ArcSetPut{}, fmt.Errorf("entry %d: %w", i, err)
		}
		entries = append(entries, entry)
	}

	set, err := arcset.NewCanonicalArcSet(kind, entries)
	if err != nil {
		return gateway.ArcSetPut{}, err
	}
	return gateway.ArcSetPut{
		Object: object,
		Kind:   kind,
		ArcSet: set,
	}, nil
}

func semanticEntryFromRequest(kind arcset.Kind, req httpapi.SemanticMutationEntry) (arcset.ArcEntry, error) {
	target, err := decodeCID(req.Target)
	if err != nil {
		return arcset.ArcEntry{}, fmt.Errorf("invalid target: %w", err)
	}

	targetRef, err := semanticTargetRef(req.TargetKind, target)
	if err != nil {
		return arcset.ArcEntry{}, err
	}

	var coord arcset.CanonicalCoordinate
	switch kind {
	case arcset.KindMap:
		if req.Path == "" {
			return arcset.ArcEntry{}, fmt.Errorf("path is required for map entries")
		}
		coord, err = arcset.NewMapCoordinate(req.Path)
	case arcset.KindList:
		if req.Index == nil {
			return arcset.ArcEntry{}, fmt.Errorf("index is required for list entries")
		}
		if *req.Index > uint64(1<<63-1) {
			return arcset.ArcEntry{}, fmt.Errorf("index is too large")
		}
		coord, err = arcset.NewListCoordinate(int64(*req.Index))
	default:
		return arcset.ArcEntry{}, fmt.Errorf("%w: %q", arcset.ErrInvalidKind, kind)
	}
	if err != nil {
		return arcset.ArcEntry{}, err
	}

	return arcset.ArcEntry{
		Coordinate: coord,
		Target:     targetRef,
	}, nil
}

func semanticTargetRef(kind string, target cid.Cid) (arcset.TargetRef, error) {
	switch arcset.TargetKind(kind) {
	case "":
		return arcset.NewCIDTarget(target), nil
	case arcset.TargetKindUnknown:
		return arcset.NewUnknownTarget(target), nil
	case arcset.TargetKindCAS:
		return arcset.NewCASTarget(target), nil
	case arcset.TargetKindMap:
		return arcset.NewMapTarget(target), nil
	case arcset.TargetKindList:
		return arcset.NewListTarget(target), nil
	default:
		return arcset.TargetRef{}, fmt.Errorf("%w: %q", arcset.ErrInvalidTargetKind, kind)
	}
}

func buildCreateSnapshot(arcs map[string]cid.Cid) (arcset.ArcSet, int, error) {
	snapshot, err := arcset.NewArcSet(arcs)
	if err != nil {
		return nil, 0, err
	}

	payload, ok := snapshot.Get(explicit.PayloadArc)
	if !ok || !payload.Defined() {
		return nil, 0, fmt.Errorf("@payload binding is required")
	}

	canonical, err := arcset.ToPathMap(snapshot)
	if err != nil {
		return nil, 0, err
	}

	arcCount := 0
	for _, target := range canonical {
		if target.Defined() {
			arcCount++
		}
	}

	return snapshot, arcCount, nil
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

func validatePayloadUpdate(path string, target cid.Cid) error {
	if arcset.CanonicalizePath(path) == explicit.PayloadArc && !target.Defined() {
		return fmt.Errorf("@payload binding is mandatory and cannot be deleted")
	}
	return nil
}

func validatePayloadBatchUpdates(updates map[string]cid.Cid) error {
	for rawPath, target := range updates {
		if err := validatePayloadUpdate(rawPath, target); err != nil {
			return err
		}
	}
	return nil
}

func resolveMiss(root cid.Cid, requestedPath string, result *resolver.ResolveResult) bool {
	if result == nil {
		return false
	}
	path := arcset.CanonicalizePath(requestedPath)
	return !path.IsEmpty() && result.Target.Equals(root) && len(result.Transcript.Steps) == 0
}

func mandatoryMapPayload(ctx context.Context, g *graph.Graph, root cid.Cid) (cid.Cid, error) {
	payload, err := g.Writer().GetArc(ctx, g.BucketId(), root, explicit.PayloadArc.String())
	if err != nil {
		return cid.Undef, err
	}
	if !payload.Defined() {
		return cid.Undef, fmt.Errorf("map root %s is missing mandatory @payload binding", root.String())
	}
	return payload, nil
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

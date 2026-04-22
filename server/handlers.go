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
	"strconv"
	"strings"
	"time"

	"github.com/dewebprotocol/malt/core/bucketpath"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/lineage"
	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/resolver/step/explicit"
	"github.com/dewebprotocol/malt/core/structure/list"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/types/evidence"
	"github.com/dewebprotocol/malt/core/writer"
	"github.com/dewebprotocol/malt/httpapi"
	cid "github.com/ipfs/go-cid"
)

const fixedBucketListChunkSize = 262144

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, &httpapi.HealthResponse{Status: "ok"})
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
		if errors.Is(err, writer.ErrArcNotFound) {
			writeError(w, http.StatusBadRequest, "new_root must resolve to a map root in this bucket with a mandatory @payload binding")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
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
	switch stat.StorageKind {
	case "raw":
		raw, err := s.node.CAS().Get(r.Context(), key)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		payload = raw[start:endExclusive]
	case "list":
		payload, err = s.readListRange(r.Context(), g, key, start, endExclusive)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	default:
		writeError(w, http.StatusInternalServerError, "unsupported storage kind for content")
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
	s.resolveAndWrite(w, g, root, r.URL.Query().Get("path"))
}

func (s *Server) handleBucketProof(w http.ResponseWriter, r *http.Request) {
	s.handleBucketResolve(w, r)
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

var errPathNotFound = errors.New("path not found")

func (s *Server) bucketStat(ctx context.Context, g *graph.Graph, root cid.Cid, path string) (*httpapi.BucketStatResponse, error) {
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

func buildCreateSnapshot(arcs map[string]cid.Cid) (arcset.ArcSet, int, error) {
	canonical := make(map[string]cid.Cid, len(arcs))
	payloadPresent := false
	for rawPath, target := range arcs {
		path := arcset.CanonicalizePath(rawPath)
		if path.IsEmpty() {
			return nil, 0, fmt.Errorf("path must not be empty")
		}

		if existing, ok := canonical[path.String()]; ok && !existing.Equals(target) {
			return nil, 0, fmt.Errorf("duplicate canonical path %q in arcs", path.String())
		}
		canonical[path.String()] = target
		if path == explicit.PayloadArc && target.Defined() {
			payloadPresent = true
		}
	}
	if !payloadPresent {
		return nil, 0, fmt.Errorf("@payload binding is required")
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

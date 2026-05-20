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

	"github.com/dewebprotocol/malt/core/cas"
	"github.com/dewebprotocol/malt/core/codec"
	"github.com/dewebprotocol/malt/core/gateway"
	"github.com/dewebprotocol/malt/core/graph"
	"github.com/dewebprotocol/malt/core/layout/malt/unixfs"
	"github.com/dewebprotocol/malt/core/manifest"
	"github.com/dewebprotocol/malt/core/querypath"
	"github.com/dewebprotocol/malt/core/resolver"
	"github.com/dewebprotocol/malt/core/resolver/step/explicit"
	"github.com/dewebprotocol/malt/core/structure"
	"github.com/dewebprotocol/malt/core/structure/list"
	"github.com/dewebprotocol/malt/core/structure/mapping"
	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/types/evidence"
	"github.com/dewebprotocol/malt/core/types/prooflist"
	"github.com/dewebprotocol/malt/httpapi"
	cid "github.com/ipfs/go-cid"
)

const fixedListChunkSize = 262144

type pathResolution struct {
	queryPath string
	target    cid.Cid
	stat      *httpapi.PathStatResponse
	proofList *prooflist.ProofList
}

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

func (s *Server) handleResolve(w http.ResponseWriter, r *http.Request) {
	g, err := s.getOrCreateGraph(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	root, err := decodeCID(r.PathValue("root"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid root CID: "+err.Error())
		return
	}
	s.serveResolve(w, r, g, root, r.PathValue("path"))
}

func (s *Server) handleContent(w http.ResponseWriter, r *http.Request) {
	g, err := s.getOrCreateGraph(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	root, err := decodeCID(r.PathValue("root"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid root CID: "+err.Error())
		return
	}
	path := r.PathValue("path")

	if _, ok := r.URL.Query()["format"]; ok {
		writeError(w, http.StatusBadRequest, "format query is not supported; use /resolve/{root}/{path} for path resolution or GET /{root}/{path} for content reads")
		return
	}

	wantProof := r.Method != http.MethodHead && !shouldOmitDefaultProof(r)
	resolved, err := s.resolvePath(r.Context(), g, root, path, wantProof)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errPathNotFound) || errors.Is(err, resolver.ErrResolutionFailed) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	stat := resolved.stat

	// HEAD request — return stat headers without body
	if r.Method == http.MethodHead {
		w.Header().Set("X-Malt-Kind", stat.Kind)
		w.Header().Set("X-Malt-Storage-Kind", stat.StorageKind)
		w.Header().Set("X-Malt-Key", stat.Key)
		if stat.Payload != "" {
			w.Header().Set("X-Malt-Payload", stat.Payload)
		}
		if stat.Size != nil {
			w.Header().Set("Content-Length", strconv.FormatInt(*stat.Size, 10))
		}
		w.WriteHeader(http.StatusOK)
		return
	}

	if stat.Kind == "dir" {
		// Return JSON directory listing
		if resolved.proofList != nil {
			if err := writeProofListHeader(w, *resolved.proofList); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			s.node.RecordProofList(*resolved.proofList)
		}
		addVaryHeader(w, "X-Malt-Proof")
		writeJSON(w, http.StatusOK, stat)
		return
	}

	// Serve file content
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
		writeError(w, httpapi.StatusRangeNotSatisfiable, err.Error())
		return
	}
	payload, err := s.readContentPayload(r.Context(), g, stat, key, start, endExclusive)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	// Generate and write proof headers before response headers
	if resolved.proofList != nil {
		pl, err := s.readProofList(r.Context(), g, resolved, start, endExclusive)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if err := writeProofListHeader(w, *pl); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.node.RecordProofList(*pl)
	}

	// Advertise X-Malt-Proof as a variance header since proof generation depends on it
	addVaryHeader(w, "X-Malt-Proof")
	w.Header().Set("Accept-Ranges", "bytes")
	if partial {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, endExclusive-1, totalSize))
		w.WriteHeader(http.StatusPartialContent)
	} else {
		w.WriteHeader(http.StatusOK)
	}
	_, _ = io.Copy(w, bytes.NewReader(payload))
}

func (s *Server) serveResolve(w http.ResponseWriter, r *http.Request, g *graph.Graph, root cid.Cid, queryPath string) {
	resolved, err := s.resolvePath(r.Context(), g, root, queryPath, !shouldOmitDefaultProof(r))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, errPathNotFound) || errors.Is(err, resolver.ErrResolutionFailed) {
			status = http.StatusNotFound
		}
		writeError(w, status, err.Error())
		return
	}
	addVaryHeader(w, "X-Malt-Proof")
	resp := &httpapi.ResolveResponse{Target: resolved.target.String()}
	if resolved.proofList != nil {
		resp.ProofList = resolved.proofList
		s.node.RecordProofList(*resolved.proofList)
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleSemanticMutation(w http.ResponseWriter, r *http.Request) {
	g, err := s.getOrCreateGraph(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	baseRoot, err := decodeCID(r.PathValue("root"))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	var req httpapi.SemanticMutationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}

	mut, err := semanticMutationFromRequest(baseRoot, req.Puts)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	receipt, err := s.applyGatewaySemanticMutation(r.Context(), g, mut)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, &httpapi.SemanticMutationResponse{
		BaseRoot:        receipt.BaseRoot.String(),
		NewRoot:         receipt.NewRoot.String(),
		ResultRoot:      receipt.NewRoot.String(),
		PutCount:        receipt.PutCount,
		ArcCount:        receipt.ArcCount,
		MALTObjectCount: receipt.PutCount,
		MapCount:        countSemanticPuts(mut.Puts, arcset.KindMap),
		ListCount:       countSemanticPuts(mut.Puts, arcset.KindList),
	})
}

func (s *Server) handleWrite(w http.ResponseWriter, r *http.Request) {
	g, err := s.getOrCreateGraph(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	root, err := decodeCID(r.PathValue("root"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid root CID: "+err.Error())
		return
	}

	p := r.PathValue("path")
	if p == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	layout, err := s.unixFSLayout(g)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	baseRoot, err := s.prepareUnixFSRoot(r.Context(), g, layout, root)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}

	if r.URL.Query().Get("type") == "dir" {
		newRoot, err := layout.AddDirectory(r.Context(), baseRoot, p)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		receipt, err := s.applyUnixFSLayoutMutation(r.Context(), g, layout, root, newRoot)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		resp := &httpapi.UnixFSWriteResponse{
			Path:     p,
			Kind:     "dir",
			NewRoot:  receipt.NewRoot.String(),
			ArcCount: receipt.ArcCount,
		}
		if root.Defined() {
			resp.OldRoot = root.String()
		}
		writeJSON(w, http.StatusCreated, resp)
		return
	}

	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("read request body: %v", err))
		return
	}
	newRoot, err := layout.AddFile(r.Context(), baseRoot, p, data)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	receipt, err := s.applyUnixFSLayoutMutation(r.Context(), g, layout, root, newRoot)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	resp := &httpapi.UnixFSWriteResponse{
		Path:     p,
		Kind:     "file",
		NewRoot:  receipt.NewRoot.String(),
		ArcCount: receipt.ArcCount,
	}
	if root.Defined() {
		resp.OldRoot = root.String()
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleCreateStructure(w http.ResponseWriter, r *http.Request) {
	g, err := s.getOrCreateGraph(r.Context())
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
	root, err := g.Writer().CreateStructure(r.Context(), g.Namespace(), snapshot)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, &httpapi.CreateStructureResponse{Root: root.String()})
}

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	g, err := s.getOrCreateGraph(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var req httpapi.VerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}
	valid, err := s.verifyProofList(g, req.ProofList)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, &httpapi.VerifyResponse{Valid: valid})
}

func (s *Server) verifyProofList(g *graph.Graph, pl prooflist.ProofList) (bool, error) {
	if err := pl.ValidateShape(prooflist.RequireSteps()); err != nil {
		return false, err
	}
	var verifiedPath proofListVerifiedPath
	for i, step := range pl.Steps {
		ok, err := s.verifyProofListStep(g, i, step)
		if err != nil || !ok {
			return ok, err
		}
		if err := verifiedPath.addStep(step); err != nil {
			return false, err
		}
	}
	if err := validateProofListQuery(pl, verifiedPath); err != nil {
		return false, err
	}
	return true, nil
}

func validateProofListQuery(pl prooflist.ProofList, verifiedPath proofListVerifiedPath) error {
	want := arcset.CanonicalizePath(querypath.CanonicalizeQueryPath(pl.Query)).String()
	if want == "" {
		return nil
	}
	got := verifiedPath.logicalQueryPath()
	if got == want {
		return nil
	}
	if verifiedPath.hasPayloadBinding {
		payloadQuery := "@payload"
		if got != "" {
			payloadQuery = got + "/@payload"
		}
		if want == payloadQuery {
			return nil
		}
	}
	return fmt.Errorf("prooflist query %q does not match ordered traversal path %q", want, got)
}

type proofListVerifiedPath struct {
	parts             []string
	hasPayloadBinding bool
}

func (p *proofListVerifiedPath) addStep(step prooflist.Step) error {
	path := arcset.CanonicalizePath(step.Path).String()
	if path == "" || step.EvidenceKind == "structure" && (step.EvidenceBackend == "list" || step.EvidenceBackend == "measured_list") {
		return nil
	}
	if p.hasPayloadBinding {
		return fmt.Errorf("prooflist traversal step %q appears after terminal @payload binding", path)
	}
	if path == "@payload" {
		p.hasPayloadBinding = true
		return nil
	}
	p.parts = append(p.parts, path)
	return nil
}

func (p proofListVerifiedPath) logicalQueryPath() string {
	return strings.Join(p.parts, "/")
}

func (s *Server) verifyProofListStep(g *graph.Graph, index int, step prooflist.Step) (bool, error) {
	switch step.EvidenceKind {
	case "explicit", "implicit", "hamt":
		ev, err := decodeEvidence(step.EvidenceKind, step.Evidence)
		if err != nil {
			return false, fmt.Errorf("invalid evidence at step %d: %w", index, err)
		}
		return g.Resolver().VerifyTranscript(step.From, &resolver.Transcript{Steps: []resolver.StepEvidence{{
			Path:     arcset.CanonicalizePath(step.Path),
			Target:   step.Target,
			Evidence: ev,
		}}})
	case "structure":
		switch step.EvidenceBackend {
		case "map":
			key := arcset.CanonicalizePath(step.Path)
			return g.Semantic().Verify(step.From, key, mapping.Binding{Value: step.Target, Present: true}, structure.Proof(step.Proof))
		case "list":
			if step.Index == nil {
				return false, fmt.Errorf("prooflist step %d list index is missing", index)
			}
			if step.Length == nil {
				return false, fmt.Errorf("prooflist step %d list length is missing", index)
			}
			return g.ListSemantic().Verify(step.From, *step.Index, list.Query{Key: step.Target, Length: *step.Length}, structure.Proof(step.Proof))
		case "measured_list":
			if step.Start == nil {
				return false, fmt.Errorf("prooflist step %d list range start is missing", index)
			}
			if step.End == nil {
				return false, fmt.Errorf("prooflist step %d list range end is missing", index)
			}
			if step.ChildCount == nil {
				return false, fmt.Errorf("prooflist step %d list range child count is missing", index)
			}
			if step.TotalSize == nil {
				return false, fmt.Errorf("prooflist step %d list range total size is missing", index)
			}
			if step.ChunkSize == nil {
				return false, fmt.Errorf("prooflist step %d list range chunk size is missing", index)
			}
			measured, ok := g.ListSemantic().(list.MeasuredSemantics)
			if !ok {
				return false, fmt.Errorf("prooflist step %d has measured list evidence but graph list semantic does not support measured ranges", index)
			}
			return measured.VerifyRange(step.From, *step.Start, step.End, list.RangeResult{
				Metadata: list.RangeMetadata{
					ChildCount: *step.ChildCount,
					TotalSize:  *step.TotalSize,
					ChunkSize:  *step.ChunkSize,
				},
				Segments: append([]cid.Cid(nil), step.Segments...),
			}, structure.Proof(step.Proof))
		default:
			return false, fmt.Errorf("prooflist step %d has unsupported structure evidence backend %q", index, step.EvidenceBackend)
		}
	default:
		return false, fmt.Errorf("prooflist step %d has unsupported evidence labels %q/%q", index, step.EvidenceKind, step.EvidenceBackend)
	}
}

var (
	errPathNotFound = errors.New("path not found")
)

func (s *Server) resolvePath(ctx context.Context, g *graph.Graph, root cid.Cid, rawPath string, wantProof bool) (*pathResolution, error) {
	cleanPath := querypath.CanonicalizeQueryPath(rawPath)
	keyResult, err := g.Resolver().ResolveKey(root, cleanPath)
	if err != nil {
		return nil, err
	}
	if resolveMiss(root, cleanPath, keyResult) {
		return nil, errPathNotFound
	}
	key := keyResult.Target
	if !key.Defined() {
		return nil, errPathNotFound
	}

	stat, target, err := s.statForResolvedKey(ctx, g, key)
	if err != nil {
		return nil, err
	}

	resolved := &pathResolution{
		queryPath: cleanPath,
		target:    target,
		stat:      stat,
	}
	if !wantProof {
		return resolved, nil
	}

	transcript := keyResult.Transcript
	if codec.SemanticKindOf(key) == codec.SemanticKindMap {
		payloadResult, err := g.Resolver().ResolveKey(key, explicit.PayloadArc.String())
		if err != nil {
			return nil, err
		}
		if resolveMiss(key, explicit.PayloadArc.String(), payloadResult) {
			return nil, errPathNotFound
		}
		if !payloadResult.Target.Equals(target) {
			return nil, fmt.Errorf("%w: resolved target %s does not match projected target %s", resolver.ErrResolutionFailed, payloadResult.Target, target)
		}
		transcript.Steps = append(transcript.Steps, payloadResult.Transcript.Steps...)
	}

	pl, err := resolver.ProofListFromTranscript(root, transcript)
	if err != nil {
		return nil, err
	}
	pl.Query = cleanPath
	resolved.proofList = pl
	return resolved, nil
}

func (s *Server) statForResolvedKey(ctx context.Context, g *graph.Graph, key cid.Cid) (*httpapi.PathStatResponse, cid.Cid, error) {
	switch codec.SemanticKindOf(key) {
	case codec.SemanticKindManifest:
		stat, err := s.statFromFlatTarget(ctx, g, key)
		return stat, key, err
	case codec.SemanticKindMap:
		if stat, err := s.unixFSPathStat(ctx, g, key, ""); err == nil {
			target, err := statReadTarget(stat)
			if err != nil {
				return nil, cid.Undef, err
			}
			return stat, target, nil
		}
		payload, err := mandatoryMapPayload(ctx, g, key)
		if err != nil {
			return nil, cid.Undef, err
		}
		return &httpapi.PathStatResponse{
			Kind:        "dir",
			StorageKind: "map",
			Key:         key.String(),
			Payload:     payload.String(),
		}, payload, nil
	case codec.SemanticKindList:
		size, _, err := s.listFileSize(ctx, g, key)
		if err != nil {
			return nil, cid.Undef, err
		}
		return &httpapi.PathStatResponse{
			Kind:        "file",
			StorageKind: "list",
			Key:         key.String(),
			Size:        &size,
		}, key, nil
	default:
		resp := &httpapi.PathStatResponse{
			Kind:        "file",
			StorageKind: "raw",
			Key:         key.String(),
		}
		if data, err := s.node.CAS().Get(ctx, key); err == nil {
			size := int64(len(data))
			resp.Size = &size
		}
		return resp, key, nil
	}
}

func statReadTarget(stat *httpapi.PathStatResponse) (cid.Cid, error) {
	if stat == nil {
		return cid.Undef, fmt.Errorf("resolved stat is nil")
	}
	target := stat.Key
	if stat.Payload != "" {
		target = stat.Payload
	}
	if target == "" {
		return cid.Undef, errPathNotFound
	}
	return decodeCID(target)
}

func (s *Server) readProofList(ctx context.Context, g *graph.Graph, resolved *pathResolution, start, endExclusive int64) (*prooflist.ProofList, error) {
	if resolved == nil || resolved.proofList == nil {
		return nil, fmt.Errorf("resolution prooflist is missing")
	}
	pl := *resolved.proofList
	pl.Steps = append([]prooflist.Step(nil), resolved.proofList.Steps...)
	if resolved.stat.StorageKind == "list" && endExclusive > start {
		listRoot, err := decodeCID(resolved.stat.Key)
		if err != nil {
			return nil, err
		}
		if measured, ok := g.ListSemantic().(list.MeasuredSemantics); ok {
			rangeStart := uint64(start)
			rangeEnd := uint64(endExclusive)
			result, proof, err := measured.ProveRange(ctx, g.Namespace(), listRoot, rangeStart, &rangeEnd)
			if err == nil {
				if err := unixfs.AppendListRangeStep(&pl, resolved.queryPath, listRoot, rangeStart, rangeEnd, result, proof); err != nil {
					return nil, err
				}
				return &pl, nil
			}
		}
		steps, err := s.listIndexStepsForRange(ctx, g, listRoot, start, endExclusive)
		if err != nil {
			return nil, err
		}
		if err := unixfs.AppendListIndexSteps(&pl, resolved.queryPath, steps); err != nil {
			return nil, err
		}
	}
	return &pl, nil
}

func (s *Server) statFromFlatTarget(ctx context.Context, g *graph.Graph, target cid.Cid) (*httpapi.PathStatResponse, error) {
	switch codec.SemanticKindOf(target) {
	case codec.SemanticKindManifest:
		entries, err := s.readDirectoryManifest(ctx, target)
		if err != nil {
			return nil, err
		}
		return &httpapi.PathStatResponse{
			Kind:        "dir",
			StorageKind: "manifest",
			Key:         target.String(),
			Payload:     target.String(),
			Entries:     entries,
		}, nil
	case codec.SemanticKindMap:
		if stat, err := s.unixFSPathStat(ctx, g, target, ""); err == nil {
			return stat, nil
		}
		payload, err := mandatoryMapPayload(ctx, g, target)
		if err != nil {
			return nil, err
		}
		return &httpapi.PathStatResponse{
			Kind:        "dir",
			StorageKind: "map",
			Key:         target.String(),
			Payload:     payload.String(),
		}, nil
	case codec.SemanticKindList:
		size, _, err := s.listFileSize(ctx, g, target)
		if err != nil {
			return nil, err
		}
		return &httpapi.PathStatResponse{
			Kind:        "file",
			StorageKind: "list",
			Key:         target.String(),
			Size:        &size,
		}, nil
	default:
		data, err := s.node.CAS().Get(ctx, target)
		if err != nil {
			return nil, errPathNotFound
		}
		size := int64(len(data))
		return &httpapi.PathStatResponse{
			Kind:        "file",
			StorageKind: "raw",
			Key:         target.String(),
			Size:        &size,
		}, nil
	}
}

func (s *Server) legacyPathStat(ctx context.Context, g *graph.Graph, root cid.Cid, path string) (*httpapi.PathStatResponse, error) {
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
	case codec.SemanticKindManifest:
		return s.statFromFlatTarget(ctx, g, key)
	case codec.SemanticKindMap:
		if stat, err := s.unixFSPathStat(ctx, g, key, ""); err == nil {
			return stat, nil
		}
		payload, err := mandatoryMapPayload(ctx, g, key)
		if err != nil {
			return nil, err
		}
		resp := &httpapi.PathStatResponse{
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
		return &httpapi.PathStatResponse{
			Kind:        "file",
			StorageKind: "list",
			Key:         key.String(),
			Size:        &size,
		}, nil
	default:
		// Non-MALT key is treated as a raw file. If the content is available in
		// CAS we include the size; otherwise we still return the resolved key.
		resp := &httpapi.PathStatResponse{
			Kind:        "file",
			StorageKind: "raw",
			Key:         key.String(),
		}
		if data, err := s.node.CAS().Get(ctx, key); err == nil {
			s := int64(len(data))
			resp.Size = &s
		}
		return resp, nil
	}
}

func (s *Server) unixFSLayout(g *graph.Graph) (*unixfs.Layout, error) {
	blocks, ok := s.node.CAS().(cas.Client)
	if !ok {
		return nil, fmt.Errorf("configured CAS does not support writes")
	}
	return unixfs.New(unixfs.Options{
		Namespace: g.Namespace(),
		ChunkSize: fixedListChunkSize,
		Map:       g.Semantic(),
		List:      g.ListSemantic(),
		Blocks:    blocks,
	})
}

func (s *Server) readDirectoryManifest(ctx context.Context, manifestCID cid.Cid) ([]string, error) {
	data, err := s.node.CAS().Get(ctx, manifestCID)
	if err != nil {
		return nil, err
	}
	m, err := manifest.ParseDirectoryJSON(data)
	if err != nil {
		return nil, err
	}
	return m.Entries, nil
}

func (s *Server) prepareUnixFSRoot(ctx context.Context, g *graph.Graph, layout *unixfs.Layout, root cid.Cid) (cid.Cid, error) {
	if !root.Defined() {
		return cid.Undef, nil
	}
	if stat, err := layout.Stat(ctx, root, ""); err == nil && stat.Kind == "directory" {
		return root, nil
	}
	// Try legacy migration; if that fails, treat as fresh root.
	if migrated, err := s.migrateLegacyTreeToUnixFS(ctx, g, layout, root); err == nil {
		return migrated, nil
	}
	return cid.Undef, nil
}

func (s *Server) applyUnixFSLayoutMutation(ctx context.Context, g *graph.Graph, layout *unixfs.Layout, oldRoot cid.Cid, newRoot cid.Cid) (gateway.WriteReceipt, error) {
	plan, err := layout.MutationPlanForRoot(ctx, oldRoot, newRoot)
	if err != nil {
		return gateway.WriteReceipt{}, err
	}
	mut := semanticMutationFromUnixFSPlan(plan, newRoot)
	receipt, err := s.applyGatewaySemanticMutation(ctx, g, mut)
	if err != nil {
		return gateway.WriteReceipt{}, err
	}
	if codec.SemanticKindOf(receipt.NewRoot) != codec.SemanticKindMap {
		return gateway.WriteReceipt{}, fmt.Errorf("unixfs mutation result must be a map current root")
	}
	return receipt, nil
}

func (s *Server) applyGatewaySemanticMutation(ctx context.Context, g *graph.Graph, mut gateway.SemanticMutation) (gateway.WriteReceipt, error) {
	exec := gateway.Executor{
		Namespace: g.Namespace(),
		Maps:      g.Semantic(),
		Lists:     g.ListSemantic(),
		ArcTable:  s.node.ArcTable(),
	}
	return exec.Apply(ctx, mut)
}

func semanticMutationFromUnixFSPlan(plan *unixfs.MutationPlan, fallbackRoot cid.Cid) gateway.SemanticMutation {
	baseRoot := plan.BaseRoot
	if !baseRoot.Defined() {
		baseRoot = fallbackRoot
	}

	mut := gateway.SemanticMutation{
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
	return mut
}

func (s *Server) migrateLegacyTreeToUnixFS(ctx context.Context, g *graph.Graph, layout *unixfs.Layout, legacyRoot cid.Cid) (cid.Cid, error) {
	return s.copyLegacyPathToUnixFS(ctx, g, layout, legacyRoot, "", cid.Undef, "")
}

func (s *Server) copyLegacyPathToUnixFS(ctx context.Context, g *graph.Graph, layout *unixfs.Layout, legacyRoot cid.Cid, legacyPath string, unixRoot cid.Cid, unixPath string) (cid.Cid, error) {
	stat, err := s.legacyPathStat(ctx, g, legacyRoot, legacyPath)
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
			return cid.Undef, fmt.Errorf("legacy root file cannot be migrated into a UnixFS root")
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

func (s *Server) directoryEntriesFromStat(ctx context.Context, stat *httpapi.PathStatResponse) ([]string, error) {
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

func (s *Server) readStatFile(ctx context.Context, g *graph.Graph, stat *httpapi.PathStatResponse) ([]byte, error) {
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

func (s *Server) readContentPayload(ctx context.Context, g *graph.Graph, stat *httpapi.PathStatResponse, key cid.Cid, start, endExclusive int64) ([]byte, error) {
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

func (s *Server) unixFSPathStat(ctx context.Context, g *graph.Graph, root cid.Cid, p string) (*httpapi.PathStatResponse, error) {
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
		return &httpapi.PathStatResponse{
			Kind:        "dir",
			StorageKind: "map",
			Key:         stat.NodeRoot.String(),
			Payload:     stat.Payload.String(),
			Entries:     stat.Entries,
		}, nil
	case "file":
		size := int64(stat.Size)
		return &httpapi.PathStatResponse{
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
	q, _, err := g.ListSemantic().Prove(ctx, g.Namespace(), listRoot, ^uint64(0))
	if err != nil {
		return 0, 0, err
	}
	count := q.Length
	if count == 0 {
		return 0, 0, nil
	}
	last, _, err := g.ListSemantic().Prove(ctx, g.Namespace(), listRoot, count-1)
	if err != nil {
		return 0, 0, err
	}
	lastChunk, err := s.node.CAS().Get(ctx, last.Key)
	if err != nil {
		return 0, 0, err
	}
	size := int64((count-1)*uint64(fixedListChunkSize) + uint64(len(lastChunk)))
	return size, count, nil
}

func (s *Server) readListRange(ctx context.Context, g *graph.Graph, listRoot cid.Cid, start, endExclusive int64) ([]byte, error) {
	if endExclusive <= start {
		return []byte{}, nil
	}
	first := uint64(start / fixedListChunkSize)
	last := uint64((endExclusive - 1) / fixedListChunkSize)
	var out bytes.Buffer
	for i := first; i <= last; i++ {
		q, _, err := g.ListSemantic().Prove(ctx, g.Namespace(), listRoot, i)
		if err != nil {
			return nil, err
		}
		chunk, err := s.node.CAS().Get(ctx, q.Key)
		if err != nil {
			return nil, err
		}
		chunkStart := int64(i) * fixedListChunkSize
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
	first := uint64(start / fixedListChunkSize)
	last := uint64((endExclusive - 1) / fixedListChunkSize)
	steps := make([]unixfs.ListIndexStep, 0, last-first+1)
	for index := first; index <= last; index++ {
		query, proof, err := g.ListSemantic().Prove(ctx, g.Namespace(), listRoot, index)
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
			Length: query.Length,
			Target: query.Key,
			Proof:  proof,
		})
	}
	return steps, nil
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

func semanticMutationFromRequest(baseRoot cid.Cid, putRequests []httpapi.SemanticMutationPut) (gateway.SemanticMutation, error) {
	puts := make([]gateway.ArcSetPut, 0, len(putRequests))
	for i, putReq := range putRequests {
		put, err := semanticPutFromRequest(putReq)
		if err != nil {
			return gateway.SemanticMutation{}, fmt.Errorf("put %d: %w", i, err)
		}
		puts = append(puts, put)
	}

	return gateway.SemanticMutation{
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

func countSemanticPuts(puts []gateway.ArcSetPut, kind arcset.Kind) int {
	count := 0
	for _, put := range puts {
		if put.Kind == kind {
			count++
		}
	}
	return count
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

func resolveMiss(_ cid.Cid, _ string, result *resolver.ResolveResult) bool {
	if result == nil {
		return false
	}
	return !result.RemainingPath.IsEmpty()
}

func mandatoryMapPayload(ctx context.Context, g *graph.Graph, root cid.Cid) (cid.Cid, error) {
	payload, err := g.Writer().GetArc(ctx, g.Namespace(), root, explicit.PayloadArc.String())
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

func nonEmpty(primary string, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

// shouldOmitDefaultProof returns true when the request opts out of default proof
// generation via query parameter or request header.
func shouldOmitDefaultProof(r *http.Request) bool {
	if r.URL.Query().Get("proof") == "false" {
		return true
	}
	if r.Header.Get("X-Malt-Proof") == "omit" {
		return true
	}
	return false
}

// writeProofListHeader encodes and writes the ProofList as base64url-json headers.
func writeProofListHeader(w http.ResponseWriter, pl prooflist.ProofList) error {
	data, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	w.Header().Set("X-Malt-ProofList", encoded)
	w.Header().Set("X-Malt-ProofList-Encoding", "base64url-json")
	return nil
}

// addVaryHeader adds a Vary header value if not already present, preserving existing values.
func addVaryHeader(w http.ResponseWriter, value string) {
	values := w.Header().Values("Vary")
	for _, existing := range values {
		for _, part := range strings.Split(existing, ",") {
			if strings.EqualFold(strings.TrimSpace(part), value) {
				return
			}
		}
	}
	w.Header().Add("Vary", value)
}

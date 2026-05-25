package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/dewebprotocol/malt/api/http"
	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/auth/proof/evidence"
	"github.com/dewebprotocol/malt/auth/proof/prooflist"
	"github.com/dewebprotocol/malt/auth/semantic/list"
	"github.com/dewebprotocol/malt/graph"
	"github.com/dewebprotocol/malt/graph/resolver"
	"github.com/dewebprotocol/malt/graph/resolver/step/explicit"
	"github.com/dewebprotocol/malt/graph/writer"
	"github.com/dewebprotocol/malt/layout/unixfs"
	"github.com/dewebprotocol/malt/layout/unixfs/manifest"
	unixfswire "github.com/dewebprotocol/malt/layout/unixfs/wire"
	"github.com/dewebprotocol/malt/storage/cas"
	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

const fixedListChunkSize = 262144

type pathResolution struct {
	queryPath string
	target    cid.Cid
	stat      *httpapi.PathStatResponse
	proofList *prooflist.ProofList
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

var (
	errPathNotFound = errors.New("path not found")
)

func (s *Server) statForResolvedKey(ctx context.Context, g graph.Runtime, key cid.Cid) (*httpapi.PathStatResponse, cid.Cid, error) {
	if unixfswire.IsManifestCID(key) {
		stat, err := s.statFromFlatTarget(ctx, g, key)
		return stat, key, err
	}
	switch maltcid.SemanticKindOf(key) {
	case maltcid.SemanticKindMap:
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
	case maltcid.SemanticKindList:
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

func (s *Server) readProofList(ctx context.Context, g graph.Runtime, resolved *pathResolution, start, endExclusive int64) (*prooflist.ProofList, error) {
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

func (s *Server) statFromFlatTarget(ctx context.Context, g graph.Runtime, target cid.Cid) (*httpapi.PathStatResponse, error) {
	if unixfswire.IsManifestCID(target) {
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
	}
	switch maltcid.SemanticKindOf(target) {
	case maltcid.SemanticKindMap:
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
	case maltcid.SemanticKindList:
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

func (s *Server) legacyPathStat(ctx context.Context, g graph.Runtime, root cid.Cid, path string) (*httpapi.PathStatResponse, error) {
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

	if unixfswire.IsManifestCID(key) {
		return s.statFromFlatTarget(ctx, g, key)
	}
	switch maltcid.SemanticKindOf(key) {
	case maltcid.SemanticKindMap:
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
	case maltcid.SemanticKindList:
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

func (s *Server) unixFSLayout(g graph.Runtime) (*unixfs.Layout, error) {
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

func (s *Server) prepareUnixFSRoot(ctx context.Context, g graph.Runtime, layout *unixfs.Layout, root cid.Cid) (cid.Cid, error) {
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

func (s *Server) applyUnixFSLayoutMutation(ctx context.Context, g graph.Runtime, layout *unixfs.Layout, oldRoot cid.Cid, newRoot cid.Cid) (writer.WriteReceipt, error) {
	if oldRoot.Defined() && oldRoot.Equals(newRoot) {
		if maltcid.SemanticKindOf(newRoot) != maltcid.SemanticKindMap {
			return writer.WriteReceipt{}, fmt.Errorf("unixfs mutation result must be a map current root")
		}
		return writer.WriteReceipt{
			BaseRoot: oldRoot,
			NewRoot:  newRoot,
		}, nil
	}

	plan, err := layout.MutationPlanForRoot(ctx, oldRoot, newRoot)
	if err != nil {
		return writer.WriteReceipt{}, err
	}
	mut := plan.WriterMutation(newRoot)
	receipt, err := s.applyWriterMutation(ctx, g, mut)
	if err != nil {
		return writer.WriteReceipt{}, err
	}
	if maltcid.SemanticKindOf(receipt.NewRoot) != maltcid.SemanticKindMap {
		return writer.WriteReceipt{}, fmt.Errorf("unixfs mutation result must be a map current root")
	}
	return receipt, nil
}

func (s *Server) applyWriterMutation(ctx context.Context, g graph.Runtime, mut writer.SemanticMutation) (writer.WriteReceipt, error) {
	return graphService{runtime: g}.ApplyMutation(ctx, mut)
}

func (s *Server) migrateLegacyTreeToUnixFS(ctx context.Context, g graph.Runtime, layout *unixfs.Layout, legacyRoot cid.Cid) (cid.Cid, error) {
	return s.copyLegacyPathToUnixFS(ctx, g, layout, legacyRoot, "", cid.Undef, "")
}

func (s *Server) copyLegacyPathToUnixFS(ctx context.Context, g graph.Runtime, layout *unixfs.Layout, legacyRoot cid.Cid, legacyPath string, unixRoot cid.Cid, unixPath string) (cid.Cid, error) {
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

func (s *Server) readStatFile(ctx context.Context, g graph.Runtime, stat *httpapi.PathStatResponse) ([]byte, error) {
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

func (s *Server) unixFSPathStat(ctx context.Context, g graph.Runtime, root cid.Cid, p string) (*httpapi.PathStatResponse, error) {
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

func (s *Server) listFileSize(ctx context.Context, g graph.Runtime, listRoot cid.Cid) (int64, uint64, error) {
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

func (s *Server) readListRange(ctx context.Context, g graph.Runtime, listRoot cid.Cid, start, endExclusive int64) ([]byte, error) {
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

func (s *Server) listIndexStepsForRange(ctx context.Context, g graph.Runtime, listRoot cid.Cid, start, endExclusive int64) ([]unixfs.ListIndexStep, error) {
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

func semanticMutationFromRequest(baseRoot cid.Cid, deltaRequests []httpapi.SemanticMutationDelta) (writer.SemanticMutation, error) {
	deltas := make([]writer.ArcSetDelta, 0, len(deltaRequests))
	for i, deltaReq := range deltaRequests {
		delta, err := semanticDeltaFromRequest(deltaReq)
		if err != nil {
			return writer.SemanticMutation{}, fmt.Errorf("delta %d: %w", i, err)
		}
		deltas = append(deltas, delta)
	}

	return writer.SemanticMutation{
		BaseRoot: baseRoot,
		Deltas:   deltas,
	}, nil
}

func semanticDeltaFromRequest(req httpapi.SemanticMutationDelta) (writer.ArcSetDelta, error) {
	object := cid.Undef
	if req.Object != "" {
		parsed, err := decodeCID(req.Object)
		if err != nil {
			return writer.ArcSetDelta{}, fmt.Errorf("invalid object: %w", err)
		}
		object = parsed
	}
	expectedRoot := cid.Undef
	if req.ExpectedRoot != "" {
		parsed, err := decodeCID(req.ExpectedRoot)
		if err != nil {
			return writer.ArcSetDelta{}, fmt.Errorf("invalid expected root: %w", err)
		}
		expectedRoot = parsed
	}

	kind := arcset.Kind(req.Kind)
	changes := make([]arcset.ArcChange, 0, len(req.Changes))
	for i, changeReq := range req.Changes {
		change, err := semanticChangeFromRequest(kind, changeReq)
		if err != nil {
			return writer.ArcSetDelta{}, fmt.Errorf("change %d: %w", i, err)
		}
		changes = append(changes, change)
	}

	delta, err := arcset.NewCanonicalArcDelta(kind, changes)
	if err != nil {
		return writer.ArcSetDelta{}, err
	}
	out := writer.ArcSetDelta{
		Object:       object,
		ExpectedRoot: expectedRoot,
		Kind:         kind,
		Changes:      delta,
	}
	if req.Commit != nil && req.Commit.FixedList != nil {
		out.Commit.FixedList = &writer.FixedListCommit{
			TotalSize: req.Commit.FixedList.TotalSize,
			ChunkSize: req.Commit.FixedList.ChunkSize,
		}
	}
	return out, nil
}

func semanticChangeFromRequest(kind arcset.Kind, req httpapi.SemanticMutationChange) (arcset.ArcChange, error) {
	var coord arcset.CanonicalCoordinate
	var err error
	switch kind {
	case arcset.KindMap:
		if req.Path == "" {
			return arcset.ArcChange{}, fmt.Errorf("path is required for map changes")
		}
		coord, err = arcset.NewMapCoordinate(req.Path)
	case arcset.KindList:
		if req.Index == nil {
			return arcset.ArcChange{}, fmt.Errorf("index is required for list changes")
		}
		if *req.Index > uint64(1<<63-1) {
			return arcset.ArcChange{}, fmt.Errorf("index is too large")
		}
		coord, err = arcset.NewListCoordinate(int64(*req.Index))
	default:
		return arcset.ArcChange{}, fmt.Errorf("%w: %q", arcset.ErrInvalidKind, kind)
	}
	if err != nil {
		return arcset.ArcChange{}, err
	}

	change := arcset.ArcChange{Coordinate: coord}
	if req.Before != nil {
		before, err := semanticTargetFromRequest(*req.Before)
		if err != nil {
			return arcset.ArcChange{}, fmt.Errorf("before: %w", err)
		}
		change.Before = &before
	}
	if req.After != nil {
		after, err := semanticTargetFromRequest(*req.After)
		if err != nil {
			return arcset.ArcChange{}, fmt.Errorf("after: %w", err)
		}
		change.After = &after
	}
	return change, nil
}

func semanticTargetFromRequest(req httpapi.SemanticMutationTarget) (arcset.TargetRef, error) {
	target, err := decodeCID(req.Target)
	if err != nil {
		return arcset.TargetRef{}, fmt.Errorf("invalid target: %w", err)
	}
	return semanticTargetRef(req.TargetKind, target)
}

func countSemanticDeltas(deltas []writer.ArcSetDelta, kind arcset.Kind) int {
	count := 0
	for _, delta := range deltas {
		if delta.Kind == kind {
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

func mandatoryMapPayload(ctx context.Context, g graph.Runtime, root cid.Cid) (cid.Cid, error) {
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

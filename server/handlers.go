package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/dewebprotocol/malt/api/http"
	"github.com/dewebprotocol/malt/auth/proof/prooflist"
	"github.com/dewebprotocol/malt/graph"
	"github.com/dewebprotocol/malt/graph/resolver"
	"github.com/dewebprotocol/malt/graph/resolver/step/explicit"
	"github.com/dewebprotocol/malt/graph/writer"
	"github.com/dewebprotocol/malt/layout/unixfs"
	"github.com/dewebprotocol/malt/storage/cas"
	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

type pathResolution struct {
	queryPath string
	target    cid.Cid
	stat      *httpapi.PathStatResponse
	proofList *prooflist.ProofList
}

var (
	errPathNotFound                     = errors.New("path not found")
	errLegacyRootRequiresMigrationOptIn = errors.New("root is not a UnixFS root; pass migrate=1 to opt into legacy tree migration")
)

func (s *Server) statForResolvedKey(ctx context.Context, g graph.Runtime, key cid.Cid) (*httpapi.PathStatResponse, cid.Cid, error) {
	stat, err := s.pathStatForTarget(ctx, g, key, false)
	if err != nil {
		return nil, cid.Undef, err
	}
	target, err := statReadTarget(stat)
	if err != nil {
		return nil, cid.Undef, err
	}
	return stat, target, nil
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

func (s *Server) pathStatForTarget(ctx context.Context, g graph.Runtime, target cid.Cid, rawMustExist bool) (*httpapi.PathStatResponse, error) {
	if entries, ok, err := unixfs.ManifestDirectoryEntries(ctx, s.node.CAS(), target); ok || err != nil {
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
		resp := &httpapi.PathStatResponse{
			Kind:        "file",
			StorageKind: "raw",
			Key:         target.String(),
		}
		data, err := s.node.CAS().Get(ctx, target)
		if err != nil {
			if rawMustExist {
				return nil, errPathNotFound
			}
			return resp, nil
		}
		size := int64(len(data))
		resp.Size = &size
		return resp, nil
	}
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
		if start < 0 {
			return nil, fmt.Errorf("range start is negative")
		}
		layout, err := s.unixFSLayout(g)
		if err != nil {
			return nil, err
		}
		if err := layout.AppendListPayloadRangeProof(ctx, &pl, resolved.queryPath, listRoot, uint64(start), uint64(endExclusive-start)); err != nil {
			return nil, err
		}
	}
	return &pl, nil
}

func (s *Server) statFromFlatTarget(ctx context.Context, g graph.Runtime, target cid.Cid) (*httpapi.PathStatResponse, error) {
	return s.pathStatForTarget(ctx, g, target, true)
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

	return s.pathStatForTarget(ctx, g, key, false)
}

func (s *Server) unixFSLayout(g graph.Runtime) (*unixfs.Layout, error) {
	blocks, ok := s.node.CAS().(cas.Client)
	if !ok {
		return nil, fmt.Errorf("configured CAS does not support writes")
	}
	return unixfs.New(unixfs.Options{
		Namespace: g.Namespace(),
		Map:       g.Semantic(),
		List:      g.ListSemantic(),
		Blocks:    blocks,
	})
}

func (s *Server) prepareUnixFSRoot(ctx context.Context, g graph.Runtime, layout *unixfs.Layout, root cid.Cid, allowLegacyMigration bool) (cid.Cid, error) {
	if !root.Defined() {
		return cid.Undef, nil
	}
	if stat, err := layout.Stat(ctx, root, ""); err == nil && stat.Kind == "directory" {
		return root, nil
	}
	if !allowLegacyMigration {
		return cid.Undef, errLegacyRootRequiresMigrationOptIn
	}
	// Preserve the explicit legacy-compatibility behavior: when a caller opts
	// in but the source cannot be migrated, the write starts a fresh UnixFS root.
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
	return unixfs.DirectoryManifestPayloadEntries(ctx, s.node.CAS(), payloadCID)
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
		if stat.Size > math.MaxInt64 {
			return nil, fmt.Errorf("unixfs file size %d exceeds max int64", stat.Size)
		}
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
	layout, err := s.unixFSLayout(g)
	if err != nil {
		return 0, 0, err
	}
	size, count, err := layout.ListPayloadSize(ctx, listRoot)
	return int64(size), count, err
}

func (s *Server) readListRange(ctx context.Context, g graph.Runtime, listRoot cid.Cid, start, endExclusive int64) ([]byte, error) {
	if endExclusive <= start {
		return []byte{}, nil
	}
	if start < 0 {
		return nil, fmt.Errorf("range start is negative")
	}
	layout, err := s.unixFSLayout(g)
	if err != nil {
		return nil, err
	}
	return layout.ReadListPayloadRange(ctx, listRoot, uint64(start), uint64(endExclusive-start))
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

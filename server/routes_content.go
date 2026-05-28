package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"github.com/dewebprotocol/malt/api/http"
	"github.com/dewebprotocol/malt/graph"
	"github.com/dewebprotocol/malt/graph/resolver"
	"github.com/dewebprotocol/malt/storage/cas"
	cid "github.com/ipfs/go-cid"
)

func (s *Server) handleContent(w http.ResponseWriter, r *http.Request) {
	svc, err := s.graphService(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	g := svc.runtime
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
	resolved, err := s.resolvePath(r.Context(), svc, root, path, wantProof)
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
		if resolved.proofList != nil {
			if err := writeProofListHeader(w, *resolved.proofList); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			s.node.RecordProofList(*resolved.proofList)
		}
		payload, err := readDirectoryContentPayload(r.Context(), s.node.CAS(), stat)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		addVaryHeader(w, "X-Malt-Proof")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
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

func (s *Server) readContentPayload(ctx context.Context, g graph.Runtime, stat *httpapi.PathStatResponse, key cid.Cid, start, endExclusive int64) ([]byte, error) {
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

func readDirectoryContentPayload(ctx context.Context, blocks cas.Reader, stat *httpapi.PathStatResponse) ([]byte, error) {
	if stat == nil || stat.Kind != "dir" {
		return nil, fmt.Errorf("directory stat is required")
	}
	if stat.Payload == "" {
		return nil, fmt.Errorf("directory payload is missing")
	}
	payloadCID, err := decodeCID(stat.Payload)
	if err != nil {
		return nil, err
	}
	return blocks.Get(ctx, payloadCID)
}

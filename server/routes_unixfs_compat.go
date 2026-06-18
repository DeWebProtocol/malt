package server

import (
	"net/http"
	"strings"

	"github.com/dewebprotocol/malt/api/http"
	cid "github.com/ipfs/go-cid"
)

func (s *Server) handleWriteNewUnixFSRoot(w http.ResponseWriter, r *http.Request) {
	s.handleUnixFSWrite(w, r, cid.Undef, r.URL.Query().Get("path"))
}

func (s *Server) handleWrite(w http.ResponseWriter, r *http.Request) {
	root, err := decodeCID(r.PathValue("root"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid root CID: "+err.Error())
		return
	}
	s.handleUnixFSWrite(w, r, root, r.PathValue("path"))
}

func (s *Server) handleUnixFSWrite(w http.ResponseWriter, r *http.Request, root cid.Cid, p string) {
	g, err := s.getOrCreateGraph(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if p == "" {
		writeError(w, http.StatusBadRequest, "path is required")
		return
	}

	layout, err := s.unixFSLayout(g)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	baseRoot, err := s.prepareUnixFSRoot(r.Context(), g, layout, root, unixFSMigrationRequested(r))
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

	// File uploads are buffered before chunking, so cap them with the
	// configured upload limit. Anything beyond the limit returns 413 instead
	// of OOM-ing the daemon.
	s.limitUnixFSUpload(w, r)
	newRoot, err := layout.AddFileStream(r.Context(), baseRoot, p, r.Body)
	if err != nil {
		if isMaxBytesError(err) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
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

func unixFSMigrationRequested(r *http.Request) bool {
	value := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("migrate")))
	return value == "1" || value == "true"
}

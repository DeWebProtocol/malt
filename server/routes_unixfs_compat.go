package server

import (
	"fmt"
	"io"
	"net/http"

	"github.com/dewebprotocol/malt/api/http"
)

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

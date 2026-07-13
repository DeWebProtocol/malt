package server

import (
	"net/http"

	"github.com/dewebprotocol/malt/api/http"
	authverifier "github.com/dewebprotocol/malt/auth/verifier"
)

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Malt-Verification-Role", "diagnostic")
	portable, err := authverifier.NewDefault()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var req httpapi.VerifyRequest
	if err := s.decodeJSONBody(w, r, &req); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	valid, err := portable.VerifyProofList(r.Context(), req.ProofList)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, &httpapi.VerifyResponse{Valid: valid})
}

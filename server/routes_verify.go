package server

import (
	"encoding/json"
	"net/http"

	"github.com/dewebprotocol/malt/api/http"
)

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	svc, err := s.graphService(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.limitJSONBody(w, r)
	var req httpapi.VerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	valid, err := (proofVerifier{runtime: svc.runtime}).VerifyProofList(req.ProofList)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, &httpapi.VerifyResponse{Valid: valid})
}

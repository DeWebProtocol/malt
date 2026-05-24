package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/dewebprotocol/malt/httpapi"
)

func (s *Server) handleVerify(w http.ResponseWriter, r *http.Request) {
	svc, err := s.graphService(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	var req httpapi.VerifyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
		return
	}
	valid, err := (proofVerifier{runtime: svc.runtime}).VerifyProofList(req.ProofList)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, &httpapi.VerifyResponse{Valid: valid})
}

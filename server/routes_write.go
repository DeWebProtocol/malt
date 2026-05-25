package server

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/httpapi"
)

func (s *Server) handleSemanticMutation(w http.ResponseWriter, r *http.Request) {
	svc, err := s.graphService(r.Context())
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

	mut, err := semanticMutationFromRequest(baseRoot, req.Deltas)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	receipt, err := svc.ApplyMutation(r.Context(), mut)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	writeJSON(w, http.StatusCreated, &httpapi.SemanticMutationResponse{
		BaseRoot:        receipt.BaseRoot.String(),
		NewRoot:         receipt.NewRoot.String(),
		ResultRoot:      receipt.NewRoot.String(),
		DeltaCount:      receipt.DeltaCount,
		ArcCount:        receipt.ArcCount,
		MALTObjectCount: receipt.DeltaCount,
		MapCount:        countSemanticDeltas(mut.Deltas, arcset.KindMap),
		ListCount:       countSemanticDeltas(mut.Deltas, arcset.KindList),
	})
}

func (s *Server) handleCreateStructure(w http.ResponseWriter, r *http.Request) {
	svc, err := s.graphService(r.Context())
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
	root, err := svc.CreateStructure(r.Context(), snapshot)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, &httpapi.CreateStructureResponse{Root: root.String()})
}

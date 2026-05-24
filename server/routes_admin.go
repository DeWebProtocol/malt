package server

import (
	"net/http"

	"github.com/dewebprotocol/malt/httpapi"
)

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

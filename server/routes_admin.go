package server

import (
	"net/http"

	"github.com/dewebprotocol/malt/api/http"
	"github.com/dewebprotocol/malt/runtime/metrics"
)

const lifecycleIdentityTokenHeader = "X-Malt-Lifecycle-Token"

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, &httpapi.HealthResponse{
		Status: "ok",
	})
}

func (s *Server) handleLifecycleIdentity(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != "" {
		http.Error(w, "browser origin is not allowed", http.StatusForbidden)
		return
	}
	if s.lifecycleToken == "" || r.Header.Get(lifecycleIdentityTokenHeader) != s.lifecycleToken {
		http.Error(w, "lifecycle identity token mismatch", http.StatusUnauthorized)
		return
	}
	writeJSON(w, http.StatusOK, &httpapi.LifecycleIdentityResponse{
		Status: "ok",
	})
}

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, metricsResponse(s.node.MetricsSnapshot()))
}

func (s *Server) handleMetricsReset(w http.ResponseWriter, r *http.Request) {
	s.node.ResetMetrics()
	writeJSON(w, http.StatusOK, metricsResponse(s.node.MetricsSnapshot()))
}

func metricsResponse(snapshot metrics.Snapshot) *httpapi.MetricsResponse {
	return &httpapi.MetricsResponse{
		Snapshot: httpapi.MetricsSnapshot{
			CAS: httpapi.CASStats{
				PutCount: snapshot.CAS.PutCount,
				GetCount: snapshot.CAS.GetCount,
				HasCount: snapshot.CAS.HasCount,
				BytesPut: snapshot.CAS.BytesPut,
				BytesGet: snapshot.CAS.BytesGet,
			},
			ArcTable: httpapi.ArcTableStats{
				GetCount:          snapshot.ArcTable.GetCount,
				BatchGetCount:     snapshot.ArcTable.BatchGetCount,
				BatchGetPathCount: snapshot.ArcTable.BatchGetPathCount,
				UpdateCount:       snapshot.ArcTable.UpdateCount,
				UpdateArcCount:    snapshot.ArcTable.UpdateArcCount,
				SnapshotCount:     snapshot.ArcTable.SnapshotCount,
				SnapshotArcCount:  snapshot.ArcTable.SnapshotArcCount,
				IterateCount:      snapshot.ArcTable.IterateCount,
			},
			Proof: httpapi.ProofStats{
				ProofListCount: snapshot.Proof.ProofListCount,
				StepCount:      snapshot.Proof.StepCount,
				EvidenceBytes:  snapshot.Proof.EvidenceBytes,
				ProofBytes:     snapshot.Proof.ProofBytes,
				TotalBytes:     snapshot.Proof.TotalBytes,
			},
		},
	}
}

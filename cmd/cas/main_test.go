package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDaemonShutdownHandlerRequiresToken(t *testing.T) {
	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusNoContent)
	})
	shutdownCh := make(chan struct{}, 1)
	handler := daemonShutdownHandler(next, "secret", shutdownCh)

	req := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	resp := httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusForbidden {
		t.Fatalf("status without token = %d, want %d", resp.Code, http.StatusForbidden)
	}

	req = httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	req.Header.Set("X-MALT-CAS-Shutdown-Token", "secret")
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if resp.Code != http.StatusAccepted {
		t.Fatalf("status with token = %d, want %d", resp.Code, http.StatusAccepted)
	}
	select {
	case <-shutdownCh:
	default:
		t.Fatal("shutdown signal was not sent")
	}

	req = httptest.NewRequest(http.MethodGet, "/health", nil)
	resp = httptest.NewRecorder()
	handler.ServeHTTP(resp, req)
	if !nextCalled || resp.Code != http.StatusNoContent {
		t.Fatalf("non-shutdown request was not delegated: called=%v status=%d", nextCalled, resp.Code)
	}
}

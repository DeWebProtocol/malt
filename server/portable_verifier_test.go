package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	malt "github.com/dewebprotocol/malt"
	authverifier "github.com/dewebprotocol/malt/auth/verifier"
)

func TestVerifyHandlerCachesPortableVerifierAcrossConcurrentRequests(t *testing.T) {
	srv := New(nil, "127.0.0.1:0")
	var initializations atomic.Int32
	srv.verifierCache.factory = func() (malt.ProofVerifier, error) {
		initializations.Add(1)
		return authverifier.New(nil, nil), nil
	}
	handler := srv.Handler()

	const requests = 32
	var wg sync.WaitGroup
	statuses := make(chan int, requests)
	for range requests {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodPost, "/verify", strings.NewReader(`{"prooflist":{}}`))
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			statuses <- rec.Code
		}()
	}
	wg.Wait()
	close(statuses)

	for status := range statuses {
		if status != http.StatusBadRequest {
			t.Fatalf("verify status = %d, want %d", status, http.StatusBadRequest)
		}
	}
	if got := initializations.Load(); got != 1 {
		t.Fatalf("portable verifier initializations = %d, want 1", got)
	}
}

func TestVerifyHandlerRejectsOversizeBodyBeforePortableVerifierInitialization(t *testing.T) {
	srv := New(nil, "127.0.0.1:0", WithBodyLimits(BodyLimits{JSONBytes: 16}))
	var initializations atomic.Int32
	srv.verifierCache.factory = func() (malt.ProofVerifier, error) {
		initializations.Add(1)
		return authverifier.New(nil, nil), nil
	}

	req := httptest.NewRequest(http.MethodPost, "/verify", strings.NewReader(`{"prooflist":{"steps":[]}}`))
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("verify status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
	if got := initializations.Load(); got != 0 {
		t.Fatalf("portable verifier initialized %d times before oversized body rejection", got)
	}
}

package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserCORSAllowsConfiguredResolveAndVerifyRoutes(t *testing.T) {
	s := New(nil, "127.0.0.1:0", WithBrowserOrigins([]string{"https://docs.example"}))
	handler := s.Handler()

	t.Run("resolve GET preflight exposes proof headers", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/resolve/bafkqaaa/docs/readme", nil)
		req.Header.Set("Origin", "https://docs.example")
		req.Header.Set("Access-Control-Request-Method", http.MethodGet)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Fatalf("preflight status = %d, want %d", rec.Code, http.StatusNoContent)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://docs.example" {
			t.Fatalf("Access-Control-Allow-Origin = %q, want configured origin", got)
		}
		if got := rec.Header().Values("Vary"); !containsHeaderValue(got, "Origin") {
			t.Fatalf("Vary headers = %v, want Origin", got)
		}
		expose := rec.Header().Get("Access-Control-Expose-Headers")
		for _, want := range []string{"X-Malt-ProofList", "X-Malt-ProofList-Encoding", "Content-Range", "X-Malt-Key"} {
			if !strings.Contains(expose, want) {
				t.Fatalf("Access-Control-Expose-Headers = %q, want %s", expose, want)
			}
		}
	})

	t.Run("verify POST preflight", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/verify", nil)
		req.Header.Set("Origin", "https://docs.example")
		req.Header.Set("Access-Control-Request-Method", http.MethodPost)
		req.Header.Set("Access-Control-Request-Headers", "content-type")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusNoContent {
			t.Fatalf("preflight status = %d, want %d", rec.Code, http.StatusNoContent)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "https://docs.example" {
			t.Fatalf("Access-Control-Allow-Origin = %q, want configured origin", got)
		}
		if methods := rec.Header().Get("Access-Control-Allow-Methods"); !strings.Contains(methods, http.MethodPost) {
			t.Fatalf("Access-Control-Allow-Methods = %q, want POST", methods)
		}
		if headers := strings.ToLower(rec.Header().Get("Access-Control-Allow-Headers")); !strings.Contains(headers, "content-type") {
			t.Fatalf("Access-Control-Allow-Headers = %q, want content-type", headers)
		}
	})
}

func TestBrowserCORSDeniesUnconfiguredOriginsAndWriteRoutes(t *testing.T) {
	s := New(nil, "127.0.0.1:0", WithBrowserOrigins([]string{"https://docs.example"}))
	handler := s.Handler()

	t.Run("unconfigured origin", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/health", nil)
		req.Header.Set("Origin", "https://elsewhere.example")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("Access-Control-Allow-Origin = %q, want empty", got)
		}
	})

	t.Run("write route preflight", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodOptions, "/bafkqaaa/file.txt", nil)
		req.Header.Set("Origin", "https://docs.example")
		req.Header.Set("Access-Control-Request-Method", http.MethodPost)
		req.Header.Set("Access-Control-Request-Headers", "content-type")
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Fatalf("write preflight status = %d, want %d", rec.Code, http.StatusForbidden)
		}
		if got := rec.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Fatalf("Access-Control-Allow-Origin = %q, want empty", got)
		}
	})
}

func containsHeaderValue(values []string, want string) bool {
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), want) {
				return true
			}
		}
	}
	return false
}

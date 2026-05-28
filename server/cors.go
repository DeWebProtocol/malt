package server

import (
	"net/http"
	"strings"
)

const browserCORSAllowHeaders = "Content-Type, Range, X-Malt-Proof"
const browserCORSExposeHeaders = "X-Malt-ProofList, X-Malt-ProofList-Encoding, Content-Range, X-Malt-Kind, X-Malt-Storage-Kind, X-Malt-Key, X-Malt-Payload"
const browserCORSAllowMethods = "GET, HEAD, POST, OPTIONS"

func browserOriginSet(origins []string) map[string]struct{} {
	if len(origins) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(origins))
	for _, origin := range origins {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		set[origin] = struct{}{}
	}
	if len(set) == 0 {
		return nil
	}
	return set
}

func (s *Server) browserCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		if _, ok := s.browserOrigins[origin]; !ok {
			http.Error(w, "origin is not allowed", http.StatusForbidden)
			return
		}

		method := r.Method
		if method == http.MethodOptions {
			method = r.Header.Get("Access-Control-Request-Method")
		}
		if !browserCORSRouteAllowed(method, r.URL.Path) {
			http.Error(w, "route is not allowed for browser access", http.StatusForbidden)
			return
		}

		writeBrowserCORSHeaders(w, origin)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeBrowserCORSHeaders(w http.ResponseWriter, origin string) {
	w.Header().Set("Access-Control-Allow-Origin", origin)
	w.Header().Set("Access-Control-Allow-Methods", browserCORSAllowMethods)
	w.Header().Set("Access-Control-Allow-Headers", browserCORSAllowHeaders)
	w.Header().Set("Access-Control-Expose-Headers", browserCORSExposeHeaders)
	w.Header().Set("Access-Control-Max-Age", "600")
	addVaryHeader(w, "Origin")
}

func browserCORSRouteAllowed(method, rawPath string) bool {
	switch method {
	case http.MethodGet, http.MethodHead:
		return browserCORSReadPathAllowed(rawPath)
	case http.MethodPost:
		return rawPath == "/verify" || browserCORSUnixFSWritePathAllowed(rawPath)
	default:
		return false
	}
}

func browserCORSUnixFSWritePathAllowed(rawPath string) bool {
	if rawPath == "/_unixfs" {
		return true
	}

	trimmed := strings.Trim(rawPath, "/")
	if trimmed == "" {
		return false
	}
	root, rest, ok := strings.Cut(trimmed, "/")
	if !ok || rest == "" {
		return false
	}
	switch root {
	case "health", "metrics", "metrics:reset", "resolve", "verify", "lineage", "_", "_unixfs":
		return false
	}
	return rest != "_mutate"
}

func browserCORSReadPathAllowed(rawPath string) bool {
	if rawPath == "/health" {
		return true
	}
	if strings.HasPrefix(rawPath, "/resolve/") {
		return true
	}
	trimmed := strings.Trim(rawPath, "/")
	if trimmed == "" {
		return false
	}
	first, _, _ := strings.Cut(trimmed, "/")
	switch first {
	case "health", "metrics", "metrics:reset", "resolve", "verify", "lineage", "_", "_unixfs":
		return false
	default:
		return true
	}
}

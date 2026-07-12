package server

import (
	"net/http"
	"net/url"
	"strings"
)

const browserCORSAllowHeaders = "Content-Type, Range, X-Malt-Proof"
const browserCORSExposeHeaders = "X-Malt-ProofList, X-Malt-ProofList-Encoding, Content-Range, X-Malt-Kind, X-Malt-Storage-Kind, X-Malt-Key, X-Malt-Payload"
const browserCORSAllowMethods = "GET, HEAD, POST, OPTIONS"

type browserOriginPolicy struct {
	exact            map[string]struct{}
	loopbackWildcard []browserLoopbackWildcard
}

type browserLoopbackWildcard struct {
	scheme string
	host   string
}

func browserOriginSet(origins []string) *browserOriginPolicy {
	if len(origins) == 0 {
		return nil
	}
	policy := &browserOriginPolicy{
		exact: make(map[string]struct{}, len(origins)),
	}
	for _, origin := range origins {
		origin = strings.TrimSpace(origin)
		if origin == "" {
			continue
		}
		if wildcard, ok := parseLoopbackPortWildcardOrigin(origin); ok {
			policy.loopbackWildcard = append(policy.loopbackWildcard, wildcard)
			continue
		}
		policy.exact[origin] = struct{}{}
	}
	if len(policy.exact) == 0 && len(policy.loopbackWildcard) == 0 {
		return nil
	}
	return policy
}

func (p *browserOriginPolicy) Allows(origin string) bool {
	if p == nil {
		return false
	}
	if _, ok := p.exact[origin]; ok {
		return true
	}
	if len(p.loopbackWildcard) == 0 {
		return false
	}
	scheme, host, ok := parseBrowserOrigin(origin)
	if !ok {
		return false
	}
	for _, wildcard := range p.loopbackWildcard {
		if wildcard.scheme == scheme && wildcard.host == host {
			return true
		}
	}
	return false
}

func parseLoopbackPortWildcardOrigin(origin string) (browserLoopbackWildcard, bool) {
	base, ok := strings.CutSuffix(origin, ":*")
	if !ok {
		return browserLoopbackWildcard{}, false
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return browserLoopbackWildcard{}, false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return browserLoopbackWildcard{}, false
	}
	host, ok := normalizeLoopbackCORSHost(u.Hostname())
	if !ok {
		return browserLoopbackWildcard{}, false
	}
	return browserLoopbackWildcard{scheme: u.Scheme, host: host}, true
}

func parseBrowserOrigin(origin string) (string, string, bool) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return "", "", false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", "", false
	}
	host, ok := normalizeLoopbackCORSHost(u.Hostname())
	if !ok {
		return "", "", false
	}
	return u.Scheme, host, true
}

func normalizeLoopbackCORSHost(host string) (string, bool) {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	switch host {
	case "localhost", "127.0.0.1", "::1":
		return host, true
	default:
		return "", false
	}
}

func (s *Server) browserCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		if !s.browserOrigins.Allows(origin) {
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
		return rawPath == "/verify" || browserCORSArtifactRouteAllowed(rawPath) || browserCORSUnixFSWritePathAllowed(rawPath)
	default:
		return false
	}
}

func browserCORSArtifactRouteAllowed(rawPath string) bool {
	switch rawPath {
	case "/v1/artifacts/resolve", "/v1/artifacts/prove", "/v1/artifacts/verify":
		return true
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
	case "health", "metrics", "metrics:reset", "resolve", "verify", "lineage", "_", "_unixfs", "_lifecycle":
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
	case "health", "metrics", "metrics:reset", "resolve", "verify", "lineage", "_", "_unixfs", "_lifecycle":
		return false
	default:
		return true
	}
}

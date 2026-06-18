package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Body size defaults for write-side routes. These are sized for the MALT
// research prototype, where structured JSON requests are typically tens of KB
// and file uploads are bounded by the host's available memory (AddFileStream
// currently buffers the whole upload before chunking).
//
// Operators with different needs can override these via WithBodyLimits; tests
// in particular install much smaller numbers so they can exercise the limit
// path without allocating real megabytes of memory.
const (
	// DefaultJSONBodyBytes caps any JSON-decoded request body. ProofList and
	// semantic mutation payloads stay well below this in practice.
	DefaultJSONBodyBytes int64 = 8 * 1024 * 1024 // 8 MiB

	// DefaultUnixFSUploadBytes caps a single UnixFS file upload that the
	// daemon buffers in memory before chunking.
	DefaultUnixFSUploadBytes int64 = 1 * 1024 * 1024 * 1024 // 1 GiB
)

// BodyLimits configures upper bounds on accepted request bodies for write
// routes. Zero fields fall back to the package defaults.
type BodyLimits struct {
	JSONBytes         int64
	UnixFSUploadBytes int64
}

func (b BodyLimits) withDefaults() BodyLimits {
	if b.JSONBytes <= 0 {
		b.JSONBytes = DefaultJSONBodyBytes
	}
	if b.UnixFSUploadBytes <= 0 {
		b.UnixFSUploadBytes = DefaultUnixFSUploadBytes
	}
	return b
}

// WithBodyLimits overrides the default request-body bounds.
func WithBodyLimits(limits BodyLimits) Option {
	return func(s *Server) {
		s.bodyLimits = limits
	}
}

// limitJSONBody installs a MaxBytesReader on the request body sized for JSON
// payloads. Callers should still defer r.Body.Close().
func (s *Server) limitJSONBody(w http.ResponseWriter, r *http.Request) {
	if r == nil || r.Body == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.bodyLimits.JSONBytes)
}

// limitUnixFSUpload installs a MaxBytesReader on the request body sized for
// UnixFS file uploads.
func (s *Server) limitUnixFSUpload(w http.ResponseWriter, r *http.Request) {
	if r == nil || r.Body == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.bodyLimits.UnixFSUploadBytes)
}

// isMaxBytesError reports whether err originates from http.MaxBytesReader.
//
// Go 1.19 introduced *http.MaxBytesError; we accept either the typed error or
// the historical "http: request body too large" string so we keep working
// against vendored stdlibs.
func isMaxBytesError(err error) bool {
	if err == nil {
		return false
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return true
	}
	return strings.Contains(err.Error(), "request body too large")
}

// writeBodyDecodeError translates a JSON decode error into either a 413 (body
// too large) or 400 (malformed JSON). Returning a single helper keeps the
// error format consistent across all JSON-decoding write handlers.
func writeBodyDecodeError(w http.ResponseWriter, err error) {
	if isMaxBytesError(err) {
		writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	writeError(w, http.StatusBadRequest, fmt.Sprintf("invalid JSON: %v", err))
}

// decodeJSONBody reads exactly one JSON value from r.Body subject to the
// configured JSONBytes limit. It is the single entry point write handlers
// should use when they want a "small structured request body" semantics.
//
// MaxBytesReader on its own only counts bytes a handler actually reads, so
// json.Decoder.Decode can return after the first value while a megabyte-sized
// suffix sits unread. We close that gap two ways:
//
//   - Content-Length fast reject: when the client advertises a size larger
//     than the limit we refuse to read anything at all.
//   - Drain after Decode: io.Copy(io.Discard, r.Body) keeps reading through
//     the same MaxBytesReader so an oversized suffix (chunked or otherwise)
//     surfaces as a MaxBytesError and the handler returns 413.
//
// dst must be a pointer suitable for json.Decoder.Decode.
func (s *Server) decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) error {
	limit := s.bodyLimits.JSONBytes
	if r != nil && r.ContentLength > limit {
		return &http.MaxBytesError{Limit: limit}
	}
	s.limitJSONBody(w, r)
	if r == nil || r.Body == nil {
		return errors.New("request has no body")
	}
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		return err
	}
	// Drain whatever the decoder left behind. If it pushes the cumulative
	// read past the configured limit, MaxBytesReader trips here and the
	// handler routes the request to 413 via writeBodyDecodeError.
	if _, err := io.Copy(io.Discard, r.Body); err != nil {
		return err
	}
	return nil
}

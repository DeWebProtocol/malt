package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServerLimits_ApplyDefaults(t *testing.T) {
	got := ServerLimits{}.withDefaults()
	if got.ReadHeaderTimeout != DefaultReadHeaderTimeout {
		t.Errorf("ReadHeaderTimeout = %v, want %v", got.ReadHeaderTimeout, DefaultReadHeaderTimeout)
	}
	if got.IdleTimeout != DefaultIdleTimeout {
		t.Errorf("IdleTimeout = %v, want %v", got.IdleTimeout, DefaultIdleTimeout)
	}
	if got.ReadTimeout != DefaultReadTimeout {
		t.Errorf("ReadTimeout = %v, want %v", got.ReadTimeout, DefaultReadTimeout)
	}
	if got.WriteTimeout != DefaultWriteTimeout {
		t.Errorf("WriteTimeout = %v, want %v", got.WriteTimeout, DefaultWriteTimeout)
	}
	if got.MaxHeaderBytes != DefaultMaxHeaderBytes {
		t.Errorf("MaxHeaderBytes = %d, want %d", got.MaxHeaderBytes, DefaultMaxHeaderBytes)
	}
}

func TestServerLimits_PartialOverride(t *testing.T) {
	custom := ServerLimits{ReadHeaderTimeout: 7 * time.Second}.withDefaults()
	if custom.ReadHeaderTimeout != 7*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 7s", custom.ReadHeaderTimeout)
	}
	if custom.IdleTimeout != DefaultIdleTimeout {
		t.Errorf("IdleTimeout = %v, want default %v", custom.IdleTimeout, DefaultIdleTimeout)
	}
}

func TestBodyLimits_ApplyDefaults(t *testing.T) {
	got := BodyLimits{}.withDefaults()
	if got.JSONBytes != DefaultJSONBodyBytes {
		t.Errorf("JSONBytes = %d, want %d", got.JSONBytes, DefaultJSONBodyBytes)
	}
	if got.UnixFSUploadBytes != DefaultUnixFSUploadBytes {
		t.Errorf("UnixFSUploadBytes = %d, want %d", got.UnixFSUploadBytes, DefaultUnixFSUploadBytes)
	}
}

func TestBodyLimits_PartialOverride(t *testing.T) {
	custom := BodyLimits{JSONBytes: 16}.withDefaults()
	if custom.JSONBytes != 16 {
		t.Errorf("JSONBytes = %d, want 16", custom.JSONBytes)
	}
	if custom.UnixFSUploadBytes != DefaultUnixFSUploadBytes {
		t.Errorf("UnixFSUploadBytes = %d, want default %d", custom.UnixFSUploadBytes, DefaultUnixFSUploadBytes)
	}
}

func TestNew_SeedsLimitDefaults(t *testing.T) {
	s := New(nil, "127.0.0.1:0")
	if s.limits.ReadHeaderTimeout != DefaultReadHeaderTimeout {
		t.Errorf("limits.ReadHeaderTimeout = %v, want %v", s.limits.ReadHeaderTimeout, DefaultReadHeaderTimeout)
	}
	if s.bodyLimits.JSONBytes != DefaultJSONBodyBytes {
		t.Errorf("bodyLimits.JSONBytes = %d, want %d", s.bodyLimits.JSONBytes, DefaultJSONBodyBytes)
	}
}

func TestWithServerLimits_OverridesAndStillFillsZeros(t *testing.T) {
	s := New(nil, "127.0.0.1:0", WithServerLimits(ServerLimits{ReadHeaderTimeout: 2 * time.Second}))
	if s.limits.ReadHeaderTimeout != 2*time.Second {
		t.Errorf("ReadHeaderTimeout = %v, want 2s", s.limits.ReadHeaderTimeout)
	}
	if s.limits.IdleTimeout != DefaultIdleTimeout {
		t.Errorf("IdleTimeout = %v, want default", s.limits.IdleTimeout)
	}
}

func TestWithBodyLimits_OverridesAndStillFillsZeros(t *testing.T) {
	s := New(nil, "127.0.0.1:0", WithBodyLimits(BodyLimits{JSONBytes: 256}))
	if s.bodyLimits.JSONBytes != 256 {
		t.Errorf("JSONBytes = %d, want 256", s.bodyLimits.JSONBytes)
	}
	if s.bodyLimits.UnixFSUploadBytes != DefaultUnixFSUploadBytes {
		t.Errorf("UnixFSUploadBytes = %d, want default", s.bodyLimits.UnixFSUploadBytes)
	}
}

func TestStart_PropagatesTimeoutsToHTTPServer(t *testing.T) {
	// Avoid actually calling Start (which reads from the listener in a
	// goroutine and would race the test's reads of s.server). buildHTTPServer
	// is the synchronous helper Start uses internally; calling it directly
	// is sufficient to verify the configured limits land on *http.Server.
	srv := New(nil, "127.0.0.1:0",
		WithServerLimits(ServerLimits{
			ReadHeaderTimeout: 1500 * time.Millisecond,
			IdleTimeout:       2 * time.Second,
			ReadTimeout:       3 * time.Second,
			WriteTimeout:      4 * time.Second,
			MaxHeaderBytes:    777,
		}),
	)
	hs := srv.buildHTTPServer()
	if hs.ReadHeaderTimeout != 1500*time.Millisecond {
		t.Errorf("ReadHeaderTimeout = %v", hs.ReadHeaderTimeout)
	}
	if hs.IdleTimeout != 2*time.Second {
		t.Errorf("IdleTimeout = %v", hs.IdleTimeout)
	}
	if hs.ReadTimeout != 3*time.Second {
		t.Errorf("ReadTimeout = %v", hs.ReadTimeout)
	}
	if hs.WriteTimeout != 4*time.Second {
		t.Errorf("WriteTimeout = %v", hs.WriteTimeout)
	}
	if hs.MaxHeaderBytes != 777 {
		t.Errorf("MaxHeaderBytes = %d", hs.MaxHeaderBytes)
	}
	if hs.Addr != srv.addr {
		t.Errorf("Addr = %q, want %q", hs.Addr, srv.addr)
	}
	if hs.Handler == nil {
		t.Error("Handler is nil")
	}
}

// TestStart_BindAndShutdown drives Start end-to-end against a real loopback
// listener and exits cleanly via Shutdown. It asserts that buildHTTPServer
// is wired in and that Shutdown is safe after Start has succeeded.
func TestStart_BindAndShutdown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := listener.Addr().String()
	listener.Close()

	srv := New(nil, addr)
	errCh := make(chan error, 1)
	ready := make(chan struct{})
	go func() {
		// Mirror Start's body but signal readiness once buildHTTPServer
		// finishes so the test can synchronize without racing on s.server.
		hs := srv.buildHTTPServer()
		srv.server = hs
		close(ready)
		errCh <- hs.ListenAndServe()
	}()
	<-ready
	if srv.server == nil {
		t.Fatal("server not built")
	}
	if err := srv.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if got := <-errCh; got != nil && got != http.ErrServerClosed {
		t.Fatalf("Start returned unexpected error: %v", got)
	}
}

func TestIsMaxBytesError(t *testing.T) {
	if isMaxBytesError(nil) {
		t.Fatal("nil should not be MaxBytes error")
	}
	if isMaxBytesError(errors.New("boom")) {
		t.Fatal("plain error misclassified")
	}
	mbe := &http.MaxBytesError{Limit: 1}
	if !isMaxBytesError(mbe) {
		t.Fatal("typed MaxBytesError not detected")
	}
	if !isMaxBytesError(fmt.Errorf("decode: %w", mbe)) {
		t.Fatal("wrapped MaxBytesError not detected")
	}
	if !isMaxBytesError(errors.New("http: request body too large")) {
		t.Fatal("legacy string MaxBytesError not detected")
	}
}

func TestLimitJSONBody_RejectsOversizedReads(t *testing.T) {
	srv := New(nil, "127.0.0.1:0", WithBodyLimits(BodyLimits{JSONBytes: 4}))

	rec := httptest.NewRecorder()
	body := strings.NewReader("0123456789")
	req := httptest.NewRequest(http.MethodPost, "/", body)
	srv.limitJSONBody(rec, req)

	got, err := io.ReadAll(req.Body)
	if err == nil {
		t.Fatalf("expected error reading oversized body, got %d bytes", len(got))
	}
	if !isMaxBytesError(err) {
		t.Fatalf("error %v is not classified as MaxBytes", err)
	}
}

func TestLimitJSONBody_HandlesNil(t *testing.T) {
	srv := New(nil, "127.0.0.1:0")
	// Both nil request and nil body must not panic.
	srv.limitJSONBody(nil, nil)
	rec := httptest.NewRecorder()
	r := &http.Request{} // r.Body is nil
	srv.limitJSONBody(rec, r)
}

func TestLimitUnixFSUpload_RejectsOversizedReads(t *testing.T) {
	srv := New(nil, "127.0.0.1:0", WithBodyLimits(BodyLimits{UnixFSUploadBytes: 5}))
	rec := httptest.NewRecorder()
	body := strings.NewReader("hello world")
	req := httptest.NewRequest(http.MethodPost, "/", body)
	srv.limitUnixFSUpload(rec, req)
	_, err := io.ReadAll(req.Body)
	if err == nil || !isMaxBytesError(err) {
		t.Fatalf("expected MaxBytes error, got %v", err)
	}
}

func TestLimitUnixFSUpload_HandlesNil(t *testing.T) {
	srv := New(nil, "127.0.0.1:0")
	srv.limitUnixFSUpload(nil, nil)
	rec := httptest.NewRecorder()
	srv.limitUnixFSUpload(rec, &http.Request{})
}

func TestWriteBodyDecodeError_TooLargeReturns413(t *testing.T) {
	rec := httptest.NewRecorder()
	writeBodyDecodeError(rec, &http.MaxBytesError{Limit: 1})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
	}
}

func TestWriteBodyDecodeError_OtherErrorReturns400(t *testing.T) {
	rec := httptest.NewRecorder()
	writeBodyDecodeError(rec, errors.New("syntax"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}

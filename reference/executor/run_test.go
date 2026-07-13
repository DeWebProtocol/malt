package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	httpapi "github.com/dewebprotocol/malt/api/http"
	"github.com/dewebprotocol/malt/config"
	casmock "github.com/dewebprotocol/malt/storage/cas/mock"
)

func TestStartUsesExternalCASConfig(t *testing.T) {
	mockCAS := casmock.NewCAS()
	casServer := httptest.NewServer(casmock.NewHTTPServer("", mockCAS).Handler())
	defer casServer.Close()

	listen := "127.0.0.1:" + freePort(t)
	cfg := config.DefaultConfig()
	cfg.State.RootDir = t.TempDir()
	cfg.State.KVStore.Type = "memory"
	cfg.RPC.Listen = listen
	cfg.CAS.Mode = "external"
	cfg.CAS.BaseURL = casServer.URL

	handle, err := Start(cfg, Options{LifecycleToken: "managed-token"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := handle.Shutdown(ctx); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	}()

	baseURL := "http://" + listen
	var health httpapi.HealthResponse
	waitForTestHealth(t, baseURL, &health)
	if health.Status != "ok" {
		t.Fatalf("health status = %q, want ok", health.Status)
	}
	if err := fetchTestLifecycleIdentity(context.Background(), baseURL, "managed-token"); err != nil {
		t.Fatalf("fetchTestLifecycleIdentity: %v", err)
	}
}

func fetchTestLifecycleIdentity(ctx context.Context, baseURL, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/_lifecycle/identity", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Malt-Lifecycle-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("lifecycle identity status %d", resp.StatusCode)
	}
	return nil
}

func waitForTestHealth(t *testing.T, baseURL string, out *httpapi.HealthResponse) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(baseURL + "/health")
		if err == nil {
			func() {
				defer resp.Body.Close()
				if resp.StatusCode != http.StatusOK {
					lastErr = nil
					return
				}
				lastErr = json.NewDecoder(resp.Body).Decode(out)
			}()
			if lastErr == nil {
				return
			}
		} else {
			lastErr = err
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("reference executor did not become healthy at %s: %v", baseURL, lastErr)
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen free port: %v", err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

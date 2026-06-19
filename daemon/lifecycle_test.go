package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/dewebprotocol/malt/config"
)

func TestLifecycleStartWritesManagedState(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.RPC.Listen = "127.0.0.1:54321"
	statePath := t.TempDir() + "/daemon.json"
	logPath := t.TempDir() + "/daemon.log"
	configPath := t.TempDir() + "/malt.json"
	startedAt := time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC)
	var started BackgroundProcessSpec
	processStarted := false

	manager := NewLifecycleManager(LifecycleOptions{
		ConfigPath:    configPath,
		StatePath:     statePath,
		LogPath:       logPath,
		Executable:    "/usr/local/bin/malt",
		ProcessArgs:   []string{"--internal-daemon-test"},
		Env:           []string{LifecycleTokenEnv + "=old-token", "PATH=/bin"},
		Now:           func() time.Time { return startedAt },
		GenerateToken: func() (string, error) { return "managed-token", nil },
		StartProcess: func(spec BackgroundProcessSpec) (int, error) {
			started = spec
			processStarted = true
			return 4242, nil
		},
		HealthCheck: func(context.Context, string) error {
			if processStarted {
				return nil
			}
			return errors.New("daemon not running")
		},
		IdentityCheck: func(_ context.Context, _ string, token string) error {
			if token != "managed-token" {
				t.Fatalf("identity token = %q, want managed-token", token)
			}
			if processStarted {
				return nil
			}
			return errors.New("daemon not running")
		},
	})

	status, err := manager.Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !status.Running || !status.Managed || status.PID != 4242 {
		t.Fatalf("status = %+v, want managed running pid 4242", status)
	}
	if started.Executable != "/usr/local/bin/malt" {
		t.Fatalf("started executable = %q", started.Executable)
	}
	if !slices.Equal(started.Args, []string{"--internal-daemon-test"}) {
		t.Fatalf("started args = %v", started.Args)
	}
	if started.LogPath != logPath {
		t.Fatalf("started log path = %q, want %q", started.LogPath, logPath)
	}
	if !slices.Contains(started.Env, LifecycleTokenEnv+"=managed-token") {
		t.Fatalf("started env missing lifecycle token: %v", started.Env)
	}
	if slices.Contains(started.Env, LifecycleTokenEnv+"=old-token") {
		t.Fatalf("started env retained stale lifecycle token: %v", started.Env)
	}

	state, err := LoadDaemonState(statePath)
	if err != nil {
		t.Fatalf("LoadDaemonState: %v", err)
	}
	if state.PID != 4242 || state.Listen != cfg.RPC.Listen || state.BaseURL != cfg.APIBaseURL() {
		t.Fatalf("state = %+v", state)
	}
	if state.ConfigPath != configPath || state.StartedAt != startedAt {
		t.Fatalf("state metadata = %+v", state)
	}
	if state.LifecycleToken != "managed-token" {
		t.Fatalf("state lifecycle token = %q, want managed-token", state.LifecycleToken)
	}
	assertDaemonStateMode(t, statePath)
}

func TestWriteDaemonStateSecuresTokenFileMode(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "daemon.json")

	if err := WriteDaemonState(statePath, &DaemonState{
		PID:            4242,
		Listen:         "127.0.0.1:54321",
		BaseURL:        "http://127.0.0.1:54321",
		ConfigPath:     "config.json",
		LifecycleToken: "managed-token",
		StartedAt:      time.Now(),
	}); err != nil {
		t.Fatalf("WriteDaemonState: %v", err)
	}
	assertDaemonStateMode(t, statePath)
}

func TestWriteDaemonStateTightensExistingFileMode(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "daemon.json")
	if err := os.WriteFile(statePath, []byte("{}\n"), 0o644); err != nil {
		t.Fatalf("seed daemon state: %v", err)
	}

	if err := WriteDaemonState(statePath, &DaemonState{
		PID:            4242,
		Listen:         "127.0.0.1:54321",
		BaseURL:        "http://127.0.0.1:54321",
		ConfigPath:     "config.json",
		LifecycleToken: "managed-token",
		StartedAt:      time.Now(),
	}); err != nil {
		t.Fatalf("WriteDaemonState: %v", err)
	}
	assertDaemonStateMode(t, statePath)
}

func TestLifecycleStartDoesNotLaunchWhenDaemonIsHealthy(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.RPC.Listen = "127.0.0.1:54322"
	statePath := t.TempDir() + "/daemon.json"
	if err := WriteDaemonState(statePath, &DaemonState{
		PID:            1111,
		Listen:         cfg.RPC.Listen,
		BaseURL:        cfg.APIBaseURL(),
		ConfigPath:     "config.json",
		LifecycleToken: "managed-token",
		StartedAt:      time.Now(),
	}); err != nil {
		t.Fatalf("WriteDaemonState: %v", err)
	}

	manager := NewLifecycleManager(LifecycleOptions{
		StatePath: statePath,
		StartProcess: func(BackgroundProcessSpec) (int, error) {
			t.Fatal("StartProcess should not be called for a healthy daemon")
			return 0, nil
		},
		HealthCheck:   func(context.Context, string) error { return nil },
		IdentityCheck: func(context.Context, string, string) error { return nil },
	})

	status, err := manager.Start(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !status.Running || !status.Managed || status.PID != 1111 {
		t.Fatalf("status = %+v, want existing managed daemon", status)
	}
}

func TestLifecycleStopSignalsManagedDaemonAndRemovesState(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.RPC.Listen = "127.0.0.1:54323"
	statePath := t.TempDir() + "/daemon.json"
	if err := WriteDaemonState(statePath, &DaemonState{
		PID:            1234,
		Listen:         cfg.RPC.Listen,
		BaseURL:        cfg.APIBaseURL(),
		ConfigPath:     "config.json",
		LifecycleToken: "managed-token",
		StartedAt:      time.Now(),
	}); err != nil {
		t.Fatalf("WriteDaemonState: %v", err)
	}

	signaled := false
	manager := NewLifecycleManager(LifecycleOptions{
		StatePath:    statePath,
		PollInterval: time.Nanosecond,
		Sleep:        func(time.Duration) {},
		SignalProcess: func(pid int) error {
			if pid != 1234 {
				t.Fatalf("signal pid = %d, want 1234", pid)
			}
			signaled = true
			return nil
		},
		HealthCheck: func(context.Context, string) error {
			if signaled {
				return errors.New("daemon stopped")
			}
			return nil
		},
		IdentityCheck: func(context.Context, string, string) error { return nil },
	})

	status, err := manager.Stop(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !signaled {
		t.Fatal("daemon process was not signaled")
	}
	if status.Running {
		t.Fatalf("status = %+v, want stopped", status)
	}
	if _, err := LoadDaemonState(statePath); !errors.Is(err, ErrDaemonStateNotFound) {
		t.Fatalf("LoadDaemonState after stop = %v, want ErrDaemonStateNotFound", err)
	}
}

func TestLifecycleStopRemovesStateWithoutSignalingWhenIdentityMismatches(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.RPC.Listen = "127.0.0.1:54326"
	statePath := t.TempDir() + "/daemon.json"
	if err := WriteDaemonState(statePath, &DaemonState{
		PID:            9999,
		Listen:         cfg.RPC.Listen,
		BaseURL:        cfg.APIBaseURL(),
		ConfigPath:     "config.json",
		LifecycleToken: "managed-token",
		StartedAt:      time.Now(),
	}); err != nil {
		t.Fatalf("WriteDaemonState: %v", err)
	}

	manager := NewLifecycleManager(LifecycleOptions{
		StatePath: statePath,
		SignalProcess: func(int) error {
			t.Fatal("mismatched daemon identity should not signal a process")
			return nil
		},
		HealthCheck: func(context.Context, string) error { return nil },
		IdentityCheck: func(context.Context, string, string) error {
			return errors.New("lifecycle token mismatch")
		},
	})

	status, err := manager.Stop(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if status.Running {
		t.Fatalf("status = %+v, want stopped", status)
	}
	if _, err := LoadDaemonState(statePath); !errors.Is(err, ErrDaemonStateNotFound) {
		t.Fatalf("LoadDaemonState after identity mismatch = %v, want ErrDaemonStateNotFound", err)
	}
}

func TestLifecycleStopRemovesStaleStateWithoutSignaling(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.RPC.Listen = "127.0.0.1:54325"
	statePath := t.TempDir() + "/daemon.json"
	if err := WriteDaemonState(statePath, &DaemonState{
		PID:        9999,
		Listen:     cfg.RPC.Listen,
		BaseURL:    cfg.APIBaseURL(),
		ConfigPath: "config.json",
		StartedAt:  time.Now(),
	}); err != nil {
		t.Fatalf("WriteDaemonState: %v", err)
	}

	manager := NewLifecycleManager(LifecycleOptions{
		StatePath: statePath,
		SignalProcess: func(int) error {
			t.Fatal("stale daemon state should not signal a process")
			return nil
		},
		HealthCheck: func(context.Context, string) error {
			return errors.New("daemon stopped")
		},
	})

	status, err := manager.Stop(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if status.Running {
		t.Fatalf("status = %+v, want stopped", status)
	}
	if _, err := LoadDaemonState(statePath); !errors.Is(err, ErrDaemonStateNotFound) {
		t.Fatalf("LoadDaemonState after stale stop = %v, want ErrDaemonStateNotFound", err)
	}
}

func TestLifecycleRestartStopsThenStarts(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.RPC.Listen = "127.0.0.1:54324"
	statePath := t.TempDir() + "/daemon.json"
	if err := WriteDaemonState(statePath, &DaemonState{
		PID:            1234,
		Listen:         cfg.RPC.Listen,
		BaseURL:        cfg.APIBaseURL(),
		ConfigPath:     "config.json",
		LifecycleToken: "managed-token",
		StartedAt:      time.Now(),
	}); err != nil {
		t.Fatalf("WriteDaemonState: %v", err)
	}

	running := true
	var signaledPID int
	var startCalls int
	manager := NewLifecycleManager(LifecycleOptions{
		StatePath:    statePath,
		Executable:   "/usr/local/bin/malt",
		PollInterval: time.Nanosecond,
		Sleep:        func(time.Duration) {},
		SignalProcess: func(pid int) error {
			signaledPID = pid
			running = false
			return nil
		},
		StartProcess: func(BackgroundProcessSpec) (int, error) {
			startCalls++
			running = true
			return 5678, nil
		},
		HealthCheck: func(context.Context, string) error {
			if running {
				return nil
			}
			return errors.New("daemon stopped")
		},
		IdentityCheck: func(context.Context, string, string) error {
			if running {
				return nil
			}
			return errors.New("daemon stopped")
		},
	})

	status, err := manager.Restart(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if signaledPID != 1234 {
		t.Fatalf("signaled pid = %d, want 1234", signaledPID)
	}
	if startCalls != 1 {
		t.Fatalf("start calls = %d, want 1", startCalls)
	}
	if !status.Running || status.PID != 5678 {
		t.Fatalf("status = %+v, want restarted pid 5678", status)
	}
}

func TestDefaultIdentityCheckFallsBackToLegacyHealthToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_lifecycle/identity":
			http.NotFound(w, r)
		case "/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok","lifecycle_token":"managed-token"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	if err := defaultIdentityCheck(context.Background(), ts.URL, "managed-token"); err != nil {
		t.Fatalf("defaultIdentityCheck legacy fallback: %v", err)
	}
}

func TestDefaultIdentityCheckRejectsMismatchedLegacyHealthToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/_lifecycle/identity":
			http.NotFound(w, r)
		case "/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok","lifecycle_token":"other-token"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	if err := defaultIdentityCheck(context.Background(), ts.URL, "managed-token"); err == nil {
		t.Fatal("defaultIdentityCheck succeeded with mismatched legacy health token")
	}
}

func assertDaemonStateMode(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat daemon state: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("daemon state mode = %v, want 0600", got)
	}
}

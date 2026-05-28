package daemon

import (
	"context"
	"errors"
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
		ConfigPath:     configPath,
		StatePath:      statePath,
		LogPath:        logPath,
		Executable:     "/usr/local/bin/malt",
		ForegroundArgs: []string{"--config", configPath, "daemon", "--listen", cfg.RPC.Listen},
		Now:            func() time.Time { return startedAt },
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
	if !slices.Equal(started.Args, []string{"--config", configPath, "daemon", "--listen", cfg.RPC.Listen}) {
		t.Fatalf("started args = %v", started.Args)
	}
	if started.LogPath != logPath {
		t.Fatalf("started log path = %q, want %q", started.LogPath, logPath)
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
}

func TestLifecycleStartDoesNotLaunchWhenDaemonIsHealthy(t *testing.T) {
	cfg := config.DefaultConfig()
	cfg.RPC.Listen = "127.0.0.1:54322"
	statePath := t.TempDir() + "/daemon.json"
	if err := WriteDaemonState(statePath, &DaemonState{
		PID:        1111,
		Listen:     cfg.RPC.Listen,
		BaseURL:    cfg.APIBaseURL(),
		ConfigPath: "config.json",
		StartedAt:  time.Now(),
	}); err != nil {
		t.Fatalf("WriteDaemonState: %v", err)
	}

	manager := NewLifecycleManager(LifecycleOptions{
		StatePath: statePath,
		StartProcess: func(BackgroundProcessSpec) (int, error) {
			t.Fatal("StartProcess should not be called for a healthy daemon")
			return 0, nil
		},
		HealthCheck: func(context.Context, string) error { return nil },
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
		PID:        1234,
		Listen:     cfg.RPC.Listen,
		BaseURL:    cfg.APIBaseURL(),
		ConfigPath: "config.json",
		StartedAt:  time.Now(),
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
		PID:        1234,
		Listen:     cfg.RPC.Listen,
		BaseURL:    cfg.APIBaseURL(),
		ConfigPath: "config.json",
		StartedAt:  time.Now(),
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

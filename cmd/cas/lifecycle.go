package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const (
	defaultStartTimeout = 15 * time.Second
	defaultStopTimeout  = 10 * time.Second
	defaultPollInterval = 200 * time.Millisecond
)

// ErrDaemonStateNotFound is returned when no daemon state file exists.
var ErrDaemonStateNotFound = errors.New("daemon state not found")

// DaemonState is the local state recorded for a managed CAS daemon.
type DaemonState struct {
	PID        int       `json:"pid"`
	Listen     string    `json:"listen"`
	ConfigPath string    `json:"config_path"`
	StartedAt  time.Time `json:"started_at"`
}

// DaemonStatus describes the observed CAS daemon lifecycle state.
type DaemonStatus struct {
	Running     bool
	Managed     bool
	PID         int
	Listen      string
	ConfigPath  string
	StatePath   string
	LogPath     string
	StartedAt   time.Time
	HealthError error
}

// ResolveStatePath returns ~/.malt/cas/daemon.json.
func ResolveStatePath() (string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.json"), nil
}

// ResolveLogPath returns ~/.malt/cas/cas.log.
func ResolveLogPath() (string, error) {
	dir, err := DefaultConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cas.log"), nil
}

// LoadState loads a daemon state file.
func LoadState(path string) (*DaemonState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrDaemonStateNotFound
		}
		return nil, fmt.Errorf("read daemon state: %w", err)
	}
	var state DaemonState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("decode daemon state: %w", err)
	}
	return &state, nil
}

// WriteState writes a daemon state file atomically.
func WriteState(path string, state *DaemonState) error {
	if state == nil {
		return fmt.Errorf("daemon state is nil")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create daemon state dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode daemon state: %w", err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write daemon state: %w", err)
	}
	return nil
}

// Status reports whether the configured CAS daemon is healthy.
func daemonStatus(statePath string, cfg *Config) *DaemonStatus {
	state, stateErr := LoadState(statePath)
	if stateErr != nil && !errors.Is(stateErr, ErrDaemonStateNotFound) {
		return &DaemonStatus{HealthError: stateErr}
	}

	baseURL := "http://" + cfg.Listen
	if state != nil {
		baseURL = "http://" + state.Listen
	}

	healthErr := healthCheck(baseURL)
	status := &DaemonStatus{
		Running:    healthErr == nil,
		Managed:    state != nil,
		Listen:     cfg.Listen,
		ConfigPath: cfg.SettingsPath(),
		StatePath:  statePath,
		HealthError: healthErr,
	}
	if state != nil {
		status.PID = state.PID
		status.Listen = state.Listen
		status.ConfigPath = state.ConfigPath
		status.StartedAt = state.StartedAt
	}
	return status
}

// daemonStart launches the CAS daemon in the background.
func daemonStart(ctx context.Context, statePath, logPath string, cfg *Config) (*DaemonStatus, error) {
	status := daemonStatus(statePath, cfg)
	if status.Running {
		return status, nil
	}
	if status.Managed {
		_ = os.Remove(statePath)
	}

	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("determine executable: %w", err)
	}

	env := daemonProcessEnv(os.Environ(), cfg.SettingsPath(), "")
	pid, err := startBackgroundProcess(exe, nil, env, logPath)
	if err != nil {
		return nil, fmt.Errorf("start daemon process: %w", err)
	}

	state := &DaemonState{
		PID:        pid,
		Listen:     cfg.Listen,
		ConfigPath: cfg.SettingsPath(),
		StartedAt:  time.Now().UTC(),
	}
	if err := WriteState(statePath, state); err != nil {
		_ = signalProcess(pid)
		return nil, err
	}

	baseURL := "http://" + cfg.Listen
	if err := waitForHealth(ctx, baseURL, defaultStartTimeout); err != nil {
		_ = signalProcess(pid)
		_ = os.Remove(statePath)
		return nil, err
	}
	return daemonStatus(statePath, cfg), nil
}

// daemonStop terminates a managed CAS daemon.
func daemonStop(ctx context.Context, statePath string) (*DaemonStatus, error) {
	state, err := LoadState(statePath)
	if err != nil {
		return nil, err
	}

	baseURL := "http://" + state.Listen
	if err := healthCheck(baseURL); err != nil {
		// Daemon is already dead — clean up stale state.
		_ = os.Remove(statePath)
		return stoppedStatus(state, statePath), nil
	}

	if err := signalProcess(state.PID); err != nil {
		return nil, fmt.Errorf("stop daemon process %d: %w", state.PID, err)
	}
	if err := waitForStopped(ctx, baseURL, defaultStopTimeout); err != nil {
		return nil, err
	}
	_ = os.Remove(statePath)
	return stoppedStatus(state, statePath), nil
}

// daemonRestart stops a managed daemon if present, then starts a new one.
func daemonRestart(ctx context.Context, statePath, logPath string, cfg *Config) (*DaemonStatus, error) {
	if _, err := LoadState(statePath); err == nil {
		if _, err := daemonStop(ctx, statePath); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, ErrDaemonStateNotFound) {
		return nil, err
	} else {
		status := daemonStatus(statePath, cfg)
		if status.Running {
			return nil, fmt.Errorf("CAS daemon is running at %s but no managed state file exists", status.Listen)
		}
	}
	return daemonStart(ctx, statePath, logPath, cfg)
}

func stoppedStatus(state *DaemonState, statePath string) *DaemonStatus {
	return &DaemonStatus{
		Running:    false,
		Managed:    true,
		PID:        state.PID,
		Listen:     state.Listen,
		ConfigPath: state.ConfigPath,
		StatePath:  statePath,
		StartedAt:  state.StartedAt,
	}
}

func healthCheck(baseURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	u := baseURL + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health status %d", resp.StatusCode)
	}
	var h struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil && err != io.EOF {
		return fmt.Errorf("decode health: %w", err)
	}
	if h.Status != "ok" {
		return fmt.Errorf("health status %q", h.Status)
	}
	return nil
}

func waitForHealth(ctx context.Context, baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := healthCheck(baseURL); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("CAS daemon at %s did not become healthy within %s: %w", baseURL, timeout, lastErr)
		}
		time.Sleep(defaultPollInterval)
	}
}

func waitForStopped(ctx context.Context, baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if err := healthCheck(baseURL); err != nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("CAS daemon at %s did not stop within %s", baseURL, timeout)
		}
		time.Sleep(defaultPollInterval)
	}
}

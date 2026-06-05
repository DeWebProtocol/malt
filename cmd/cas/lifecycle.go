package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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
	PID           int       `json:"pid"`
	Listen        string    `json:"listen"`
	ConfigPath    string    `json:"config_path"`
	ShutdownToken string    `json:"shutdown_token,omitempty"`
	StartedAt     time.Time `json:"started_at"`
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
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create daemon state dir: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode daemon state: %w", err)
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(dir, ".daemon-*.tmp")
	if err != nil {
		return fmt.Errorf("create daemon state temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write daemon state temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close daemon state temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return fmt.Errorf("replace daemon state: remove old state: %w", removeErr)
		}
		if renameErr := os.Rename(tmpPath, path); renameErr != nil {
			return fmt.Errorf("replace daemon state: %w", renameErr)
		}
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
		Running:     healthErr == nil,
		Managed:     state != nil,
		Listen:      cfg.Listen,
		ConfigPath:  cfg.SettingsPath(),
		StatePath:   statePath,
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
func daemonStart(ctx context.Context, statePath, logPath string, cfg *Config, overrides DaemonOverrides) (*DaemonStatus, error) {
	release, err := acquireDaemonLock(statePath + ".lock")
	if err != nil {
		return nil, err
	}
	defer release()

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

	token, err := newShutdownToken()
	if err != nil {
		return nil, err
	}
	overrides.ShutdownToken = token
	env := daemonProcessEnv(os.Environ(), cfg.SettingsPath(), overrides)
	pid, err := startBackgroundProcess(exe, nil, env, logPath)
	if err != nil {
		return nil, fmt.Errorf("start daemon process: %w", err)
	}

	state := &DaemonState{
		PID:           pid,
		Listen:        cfg.Listen,
		ConfigPath:    cfg.SettingsPath(),
		ShutdownToken: token,
		StartedAt:     time.Now().UTC(),
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

	if err := requestShutdown(ctx, baseURL, state.ShutdownToken); err != nil {
		if signalErr := signalProcess(state.PID); signalErr != nil {
			return nil, fmt.Errorf("stop daemon process %d: shutdown request failed: %v; signal failed: %w", state.PID, err, signalErr)
		}
	}
	if err := waitForStopped(ctx, baseURL, defaultStopTimeout); err != nil {
		return nil, err
	}
	_ = os.Remove(statePath)
	return stoppedStatus(state, statePath), nil
}

// daemonRestart stops a managed daemon if present, then starts a new one.
func daemonRestart(ctx context.Context, statePath, logPath string, cfg *Config, overrides DaemonOverrides) (*DaemonStatus, error) {
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
	return daemonStart(ctx, statePath, logPath, cfg, overrides)
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
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	var lastErr error
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := healthCheck(baseURL); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("CAS daemon at %s did not become healthy within %s: %w", baseURL, timeout, lastErr)
		case <-time.After(defaultPollInterval):
		}
	}
}

func waitForStopped(ctx context.Context, baseURL string, timeout time.Duration) error {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := healthCheck(baseURL); err != nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return fmt.Errorf("CAS daemon at %s did not stop within %s", baseURL, timeout)
		case <-time.After(defaultPollInterval):
		}
	}
}

func acquireDaemonLock(path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create daemon lock dir: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("CAS daemon start already in progress: %s", path)
		}
		return nil, fmt.Errorf("create daemon lock: %w", err)
	}
	fmt.Fprintf(f, "%d\n", os.Getpid())
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return nil, fmt.Errorf("close daemon lock: %w", err)
	}
	return func() { _ = os.Remove(path) }, nil
}

func newShutdownToken() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate shutdown token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func requestShutdown(ctx context.Context, baseURL, token string) error {
	if token == "" {
		return fmt.Errorf("daemon state has no shutdown token")
	}
	reqCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, baseURL+"/shutdown", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-MALT-CAS-Shutdown-Token", token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("shutdown status %d", resp.StatusCode)
	}
	return nil
}

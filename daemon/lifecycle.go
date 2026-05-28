package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dewebprotocol/malt/config"
)

const (
	defaultLifecycleStartTimeout = 15 * time.Second
	defaultLifecycleStopTimeout  = 10 * time.Second
	defaultLifecyclePollInterval = 100 * time.Millisecond
)

// ErrDaemonStateNotFound is returned when no managed daemon state file exists.
var ErrDaemonStateNotFound = errors.New("daemon state not found")

// DaemonState is the local state recorded for a managed background daemon.
type DaemonState struct {
	PID        int       `json:"pid"`
	Listen     string    `json:"listen"`
	BaseURL    string    `json:"base_url"`
	ConfigPath string    `json:"config_path"`
	StartedAt  time.Time `json:"started_at"`
}

// DaemonStatus describes the observed daemon lifecycle state.
type DaemonStatus struct {
	Running     bool
	Managed     bool
	PID         int
	Listen      string
	BaseURL     string
	ConfigPath  string
	StatePath   string
	LogPath     string
	StartedAt   time.Time
	HealthError error
}

// BackgroundProcessSpec describes the foreground daemon process to launch in
// the background.
type BackgroundProcessSpec struct {
	Executable string
	Args       []string
	Env        []string
	LogPath    string
}

// LifecycleOptions configures local managed daemon process operations.
type LifecycleOptions struct {
	ConfigPath     string
	StatePath      string
	LogPath        string
	Executable     string
	ForegroundArgs []string
	Env            []string
	Now            func() time.Time
	StartTimeout   time.Duration
	StopTimeout    time.Duration
	PollInterval   time.Duration
	Sleep          func(time.Duration)
	StartProcess   func(BackgroundProcessSpec) (int, error)
	SignalProcess  func(int) error
	HealthCheck    func(context.Context, string) error
}

// LifecycleManager manages a daemon process started by `malt daemon start`.
type LifecycleManager struct {
	configPath     string
	statePath      string
	logPath        string
	executable     string
	foregroundArgs []string
	env            []string
	now            func() time.Time
	startTimeout   time.Duration
	stopTimeout    time.Duration
	pollInterval   time.Duration
	sleep          func(time.Duration)
	startProcess   func(BackgroundProcessSpec) (int, error)
	signalProcess  func(int) error
	healthCheck    func(context.Context, string) error
}

// NewLifecycleManager creates a lifecycle manager with production defaults for
// any operation not supplied in opts.
func NewLifecycleManager(opts LifecycleOptions) *LifecycleManager {
	m := &LifecycleManager{
		configPath:     opts.ConfigPath,
		statePath:      opts.StatePath,
		logPath:        opts.LogPath,
		executable:     opts.Executable,
		foregroundArgs: append([]string(nil), opts.ForegroundArgs...),
		env:            append([]string(nil), opts.Env...),
		now:            opts.Now,
		startTimeout:   opts.StartTimeout,
		stopTimeout:    opts.StopTimeout,
		pollInterval:   opts.PollInterval,
		sleep:          opts.Sleep,
		startProcess:   opts.StartProcess,
		signalProcess:  opts.SignalProcess,
		healthCheck:    opts.HealthCheck,
	}
	if m.now == nil {
		m.now = time.Now
	}
	if m.startTimeout <= 0 {
		m.startTimeout = defaultLifecycleStartTimeout
	}
	if m.stopTimeout <= 0 {
		m.stopTimeout = defaultLifecycleStopTimeout
	}
	if m.pollInterval <= 0 {
		m.pollInterval = defaultLifecyclePollInterval
	}
	if m.sleep == nil {
		m.sleep = time.Sleep
	}
	if m.startProcess == nil {
		m.startProcess = defaultStartProcess
	}
	if m.signalProcess == nil {
		m.signalProcess = defaultSignalProcess
	}
	if m.healthCheck == nil {
		m.healthCheck = defaultHealthCheck
	}
	return m
}

// ResolveDaemonStatePath returns the managed daemon state file path for a
// config selection.
func ResolveDaemonStatePath(configFile string) (string, error) {
	dir, err := config.ConfigDir(configFile)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.json"), nil
}

// ResolveDaemonLogPath returns the managed daemon log file path for a config
// selection.
func ResolveDaemonLogPath(configFile string) (string, error) {
	dir, err := config.ConfigDir(configFile)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.log"), nil
}

// LoadDaemonState loads a managed daemon state file.
func LoadDaemonState(path string) (*DaemonState, error) {
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

// WriteDaemonState writes a managed daemon state file.
func WriteDaemonState(path string, state *DaemonState) error {
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

// Status reports whether the configured daemon endpoint is healthy.
func (m *LifecycleManager) Status(ctx context.Context, cfg *config.Config) (*DaemonStatus, error) {
	effective, err := effectiveLifecycleConfig(cfg)
	if err != nil {
		return nil, err
	}
	state, stateErr := LoadDaemonState(m.statePath)
	if stateErr != nil && !errors.Is(stateErr, ErrDaemonStateNotFound) {
		return nil, stateErr
	}

	baseURL := effective.APIBaseURL()
	if state != nil && state.BaseURL != "" {
		baseURL = state.BaseURL
	}
	healthErr := m.healthCheck(ctx, baseURL)
	status := &DaemonStatus{
		Running:     healthErr == nil,
		Managed:     state != nil,
		BaseURL:     baseURL,
		StatePath:   m.statePath,
		LogPath:     m.logPath,
		HealthError: healthErr,
	}
	if state != nil {
		status.PID = state.PID
		status.Listen = state.Listen
		status.ConfigPath = state.ConfigPath
		status.StartedAt = state.StartedAt
		return status, nil
	}
	status.Listen = effective.RPC.Listen
	status.ConfigPath = m.configPath
	return status, nil
}

// Start launches the daemon in the background unless a healthy daemon is
// already running for the configured endpoint.
func (m *LifecycleManager) Start(ctx context.Context, cfg *config.Config) (*DaemonStatus, error) {
	effective, err := effectiveLifecycleConfig(cfg)
	if err != nil {
		return nil, err
	}
	current, err := m.Status(ctx, &effective)
	if err != nil {
		return nil, err
	}
	if current.Running {
		return current, nil
	}
	if current.Managed {
		_ = os.Remove(m.statePath)
	}

	spec := BackgroundProcessSpec{
		Executable: m.executable,
		Args:       append([]string(nil), m.foregroundArgs...),
		Env:        append([]string(nil), m.env...),
		LogPath:    m.logPath,
	}
	pid, err := m.startProcess(spec)
	if err != nil {
		return nil, fmt.Errorf("start daemon process: %w", err)
	}

	state := &DaemonState{
		PID:        pid,
		Listen:     effective.RPC.Listen,
		BaseURL:    effective.APIBaseURL(),
		ConfigPath: m.configPath,
		StartedAt:  m.now().UTC(),
	}
	if err := WriteDaemonState(m.statePath, state); err != nil {
		_ = m.signalProcess(pid)
		return nil, err
	}
	if err := m.waitForHealth(ctx, state.BaseURL, m.startTimeout); err != nil {
		_ = m.signalProcess(pid)
		_ = os.Remove(m.statePath)
		return nil, err
	}
	return m.Status(ctx, &effective)
}

// Stop terminates a managed daemon process and removes its state file.
func (m *LifecycleManager) Stop(ctx context.Context, cfg *config.Config) (*DaemonStatus, error) {
	if _, err := effectiveLifecycleConfig(cfg); err != nil {
		return nil, err
	}
	state, err := LoadDaemonState(m.statePath)
	if err != nil {
		return nil, err
	}
	if healthErr := m.healthCheck(ctx, state.BaseURL); healthErr != nil {
		if err := os.Remove(m.statePath); err != nil && !os.IsNotExist(err) {
			return nil, fmt.Errorf("remove stale daemon state: %w", err)
		}
		return &DaemonStatus{
			Running:     false,
			Managed:     true,
			PID:         state.PID,
			Listen:      state.Listen,
			BaseURL:     state.BaseURL,
			ConfigPath:  state.ConfigPath,
			StatePath:   m.statePath,
			LogPath:     m.logPath,
			StartedAt:   state.StartedAt,
			HealthError: healthErr,
		}, nil
	}
	if state.PID <= 0 {
		_ = os.Remove(m.statePath)
		return nil, fmt.Errorf("daemon state has invalid pid %d", state.PID)
	}
	if err := m.signalProcess(state.PID); err != nil {
		return nil, fmt.Errorf("stop daemon process %d: %w", state.PID, err)
	}
	if err := m.waitForStopped(ctx, state.BaseURL, m.stopTimeout); err != nil {
		return nil, err
	}
	if err := os.Remove(m.statePath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("remove daemon state: %w", err)
	}
	return &DaemonStatus{
		Running:    false,
		Managed:    true,
		PID:        state.PID,
		Listen:     state.Listen,
		BaseURL:    state.BaseURL,
		ConfigPath: state.ConfigPath,
		StatePath:  m.statePath,
		LogPath:    m.logPath,
		StartedAt:  state.StartedAt,
	}, nil
}

// Restart stops a managed daemon when present, then starts a new managed daemon.
func (m *LifecycleManager) Restart(ctx context.Context, cfg *config.Config) (*DaemonStatus, error) {
	if _, err := LoadDaemonState(m.statePath); err == nil {
		if _, err := m.Stop(ctx, cfg); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, ErrDaemonStateNotFound) {
		return nil, err
	} else {
		status, statusErr := m.Status(ctx, cfg)
		if statusErr != nil {
			return nil, statusErr
		}
		if status.Running {
			return nil, fmt.Errorf("daemon is running at %s but no managed state file exists at %s", status.BaseURL, m.statePath)
		}
	}
	return m.Start(ctx, cfg)
}

func effectiveLifecycleConfig(cfg *config.Config) (config.Config, error) {
	if cfg == nil {
		return config.Config{}, fmt.Errorf("config is nil")
	}
	effective := *cfg
	if err := effective.Validate(); err != nil {
		return config.Config{}, err
	}
	return effective, nil
}

func (m *LifecycleManager) waitForHealth(ctx context.Context, baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := m.healthCheck(ctx, baseURL); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("daemon did not become healthy at %s within %s: %w", baseURL, timeout, lastErr)
		}
		if err := sleepWithContext(ctx, m.sleep, m.pollInterval); err != nil {
			return err
		}
	}
}

func (m *LifecycleManager) waitForStopped(ctx context.Context, baseURL string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if err := m.healthCheck(ctx, baseURL); err != nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("daemon at %s did not stop within %s", baseURL, timeout)
		}
		if err := sleepWithContext(ctx, m.sleep, m.pollInterval); err != nil {
			return err
		}
	}
}

func sleepWithContext(ctx context.Context, sleep func(time.Duration), d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	sleep(d)
	return ctx.Err()
}

func defaultHealthCheck(ctx context.Context, baseURL string) error {
	u := strings.TrimRight(baseURL, "/") + "/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health status %d", resp.StatusCode)
	}
	return nil
}

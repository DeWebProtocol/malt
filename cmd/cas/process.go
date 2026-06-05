package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// DaemonOverrides are command-line overrides passed to the daemon child.
type DaemonOverrides struct {
	Listen        string
	NoLatency     bool
	GetLatency    time.Duration
	PutLatency    time.Duration
	HasLatency    time.Duration
	Jitter        time.Duration
	ShutdownToken string
}

// startBackgroundProcess forks the given executable as a detached background process.
func startBackgroundProcess(executable string, args []string, env []string, logPath string) (pid int, err error) {
	if executable == "" {
		return 0, fmt.Errorf("executable is empty")
	}
	cmd := exec.Command(executable, args...)
	if len(env) > 0 {
		cmd.Env = env
	}
	if logPath != "" {
		if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
			return 0, fmt.Errorf("create log dir: %w", err)
		}
		log, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return 0, fmt.Errorf("open log: %w", err)
		}
		defer func() {
			if closeErr := log.Close(); err == nil && closeErr != nil {
				err = fmt.Errorf("close log: %w", closeErr)
			}
		}()
		cmd.Stdout = log
		cmd.Stderr = log
	}
	configureBackgroundCommand(cmd)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid = cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return 0, err
	}
	return pid, nil
}

const (
	daemonProcessKey       = "CAS_DAEMON_PROCESS"
	daemonConfigKey        = "CAS_DAEMON_CONFIG"
	daemonListenKey        = "CAS_DAEMON_LISTEN"
	daemonNoLatencyKey     = "CAS_DAEMON_NO_LATENCY"
	daemonGetLatencyKey    = "CAS_DAEMON_GET_LATENCY"
	daemonPutLatencyKey    = "CAS_DAEMON_PUT_LATENCY"
	daemonHasLatencyKey    = "CAS_DAEMON_HAS_LATENCY"
	daemonJitterKey        = "CAS_DAEMON_JITTER"
	daemonShutdownTokenKey = "CAS_DAEMON_SHUTDOWN_TOKEN"
)

// daemonProcessEnv builds the environment for the daemon child process.
func daemonProcessEnv(env []string, configPath string, overrides DaemonOverrides) []string {
	out := withoutEnvKeys(
		env,
		daemonProcessKey,
		daemonConfigKey,
		daemonListenKey,
		daemonNoLatencyKey,
		daemonGetLatencyKey,
		daemonPutLatencyKey,
		daemonHasLatencyKey,
		daemonJitterKey,
		daemonShutdownTokenKey,
	)
	out = append(out, daemonProcessKey+"=1")
	if configPath != "" {
		out = append(out, daemonConfigKey+"="+configPath)
	}
	if overrides.Listen != "" {
		out = append(out, daemonListenKey+"="+overrides.Listen)
	}
	if overrides.NoLatency {
		out = append(out, daemonNoLatencyKey+"=1")
	}
	if overrides.GetLatency > 0 {
		out = append(out, daemonGetLatencyKey+"="+overrides.GetLatency.String())
	}
	if overrides.PutLatency > 0 {
		out = append(out, daemonPutLatencyKey+"="+overrides.PutLatency.String())
	}
	if overrides.HasLatency > 0 {
		out = append(out, daemonHasLatencyKey+"="+overrides.HasLatency.String())
	}
	if overrides.Jitter > 0 {
		out = append(out, daemonJitterKey+"="+overrides.Jitter.String())
	}
	if overrides.ShutdownToken != "" {
		out = append(out, daemonShutdownTokenKey+"="+overrides.ShutdownToken)
	}
	return out
}

func withoutEnvKeys(env []string, keys ...string) []string {
	prefixes := make([]string, 0, len(keys))
	for _, key := range keys {
		prefixes = append(prefixes, key+"=")
	}
	out := make([]string, 0, len(env))
	for _, entry := range env {
		skip := false
		for _, prefix := range prefixes {
			if strings.HasPrefix(entry, prefix) {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, entry)
		}
	}
	return out
}

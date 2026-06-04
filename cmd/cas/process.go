package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// BackgroundProcessSpec describes a daemon process to launch.
type BackgroundProcessSpec struct {
	Executable string
	Args       []string
	Env        []string
	LogPath    string
}

// startBackgroundProcess forks the given executable as a detached background process.
func startBackgroundProcess(executable string, args []string, env []string, logPath string) (int, error) {
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
		defer log.Close()
		cmd.Stdout = log
		cmd.Stderr = log
	}
	configureBackgroundCommand(cmd)
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		return 0, err
	}
	return pid, nil
}

const (
	daemonProcessKey = "CAS_DAEMON_PROCESS"
	daemonConfigKey  = "CAS_DAEMON_CONFIG"
	daemonListenKey  = "CAS_DAEMON_LISTEN"
)

// daemonProcessEnv builds the environment for the daemon child process.
func daemonProcessEnv(env []string, configPath string, listenOverride string) []string {
	out := withoutEnvKeys(env, daemonProcessKey, daemonConfigKey, daemonListenKey)
	out = append(out, daemonProcessKey+"=1")
	if configPath != "" {
		out = append(out, daemonConfigKey+"="+configPath)
	}
	if listenOverride != "" {
		out = append(out, daemonListenKey+"="+listenOverride)
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

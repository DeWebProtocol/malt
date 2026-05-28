package daemon

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func defaultStartProcess(spec BackgroundProcessSpec) (int, error) {
	if spec.Executable == "" {
		return 0, fmt.Errorf("daemon executable is empty")
	}
	cmd := exec.Command(spec.Executable, spec.Args...)
	if len(spec.Env) > 0 {
		cmd.Env = spec.Env
	}
	if spec.LogPath != "" {
		if err := os.MkdirAll(filepath.Dir(spec.LogPath), 0o755); err != nil {
			return 0, fmt.Errorf("create daemon log dir: %w", err)
		}
		log, err := os.OpenFile(spec.LogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return 0, fmt.Errorf("open daemon log: %w", err)
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

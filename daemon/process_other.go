//go:build !linux && !darwin && !freebsd && !netbsd && !openbsd && !dragonfly && !windows

package daemon

import (
	"os"
	"os/exec"
)

func configureBackgroundCommand(*exec.Cmd) {}

func defaultSignalProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}

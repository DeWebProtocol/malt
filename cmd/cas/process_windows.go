//go:build windows

package main

import (
	"os"
	"os/exec"
	"syscall"
)

const (
	createNewProcessGroup = 0x00000200
	detachProcess         = 0x00000008
)

func configureBackgroundCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createNewProcessGroup | detachProcess,
	}
}

func signalProcess(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(os.Interrupt)
}

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
	return process.Kill()
}

func processExists(pid int) bool {
	if pid <= 0 {
		return false
	}
	const (
		processQueryLimitedInformation = 0x1000
		stillActive                    = 259
	)
	handle, err := syscall.OpenProcess(processQueryLimitedInformation, false, uint32(pid))
	if err != nil {
		return false
	}
	defer syscall.CloseHandle(handle)
	var exitCode uint32
	if err := syscall.GetExitCodeProcess(handle, &exitCode); err != nil {
		return true
	}
	return exitCode == stillActive
}

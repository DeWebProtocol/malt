//go:build windows

package main

import (
	"os"
	"syscall"
)

var kernel32 = syscall.NewLazyDLL("kernel32.dll")

func init() {
	if os.Getenv(managedDaemonProcessEnv) == "1" {
		detachConsole()
	}
}

// detachConsole releases the process's association with the parent console.
// This prevents the daemon from being terminated when the console window is
// closed (CTRL_CLOSE_EVENT), which CREATE_NEW_PROCESS_GROUP alone cannot block.
func detachConsole() {
	proc := kernel32.NewProc("FreeConsole")
	proc.Call()
}

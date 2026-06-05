//go:build windows

package main

import (
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestSignalProcessTerminatesDetachedProcess(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcess")
	cmd.Env = append(os.Environ(), "MALT_CAS_HELPER_PROCESS=1")
	configureBackgroundCommand(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Process.Release(); err != nil {
		t.Fatalf("release helper: %v", err)
	}

	if err := signalProcess(pid); err != nil {
		t.Fatalf("signalProcess: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !processExists(pid) {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("process %d still exists after signalProcess", pid)
}

func TestHelperProcess(t *testing.T) {
	if os.Getenv("MALT_CAS_HELPER_PROCESS") != "1" {
		return
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestWaitForHealthHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	start := time.Now()
	err := waitForHealth(ctx, "http://127.0.0.1:1", time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForHealth error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("waitForHealth took %s after cancellation", elapsed)
	}
}

func TestWriteStateReplacesExistingState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.json")

	first := &DaemonState{PID: 1, Listen: "127.0.0.1:4318", ConfigPath: "first", ShutdownToken: "one"}
	if err := WriteState(path, first); err != nil {
		t.Fatalf("WriteState first: %v", err)
	}
	second := &DaemonState{PID: 2, Listen: "127.0.0.1:4319", ConfigPath: "second", ShutdownToken: "two"}
	if err := WriteState(path, second); err != nil {
		t.Fatalf("WriteState second: %v", err)
	}

	got, err := LoadState(path)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	if got.PID != 2 || got.ConfigPath != "second" || got.ShutdownToken != "two" {
		t.Fatalf("state = %#v, want second state", got)
	}
}

func TestAcquireDaemonLockExcludesConcurrentStart(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "daemon.json.lock")

	release, err := acquireDaemonLock(lockPath)
	if err != nil {
		t.Fatalf("acquire first lock: %v", err)
	}
	if _, err := acquireDaemonLock(lockPath); err == nil {
		t.Fatal("second acquire should fail")
	}
	release()

	release, err = acquireDaemonLock(lockPath)
	if err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
	release()
}

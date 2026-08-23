package filelock

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireSerializesAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.lock")
	unlock, err := Acquire(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Acquire(path, 30*time.Millisecond); err == nil {
		t.Fatal("second lock acquisition succeeded while the first was held")
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	again, err := Acquire(path, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if err := again(); err != nil {
		t.Fatal(err)
	}
	if err := again(); err != nil {
		t.Fatalf("idempotent release: %v", err)
	}
}

func TestRetryableUnlockCloseRetainsHandleUntilUnlockAndClosesOnce(t *testing.T) {
	unlockFailure := errors.New("unlock failed")
	closeFailure := errors.New("close failed")
	var unlockCalls, closeCalls int
	release := retryableUnlockClose(func() error {
		unlockCalls++
		if unlockCalls == 1 {
			return unlockFailure
		}
		return nil
	}, func() error {
		closeCalls++
		return closeFailure
	})
	if err := release(); !errors.Is(err, unlockFailure) || unlockCalls != 1 || closeCalls != 0 {
		t.Fatalf("first release error=%v calls=(%d,%d)", err, unlockCalls, closeCalls)
	}
	if err := release(); !errors.Is(err, closeFailure) || unlockCalls != 2 || closeCalls != 1 {
		t.Fatalf("second release error=%v calls=(%d,%d)", err, unlockCalls, closeCalls)
	}
	if err := release(); err != nil || unlockCalls != 2 || closeCalls != 1 {
		t.Fatalf("acknowledged release error=%v calls=(%d,%d)", err, unlockCalls, closeCalls)
	}
}

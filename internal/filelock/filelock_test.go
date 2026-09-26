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

func TestBlockingAndImmediateAcquisitionShareTheSameLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.lock")
	unlock, err := TryAcquire(path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if release, err := TryAcquire(path); err == nil {
		_ = release()
		t.Fatal("immediate acquisition bypassed held lock")
	}
	type result struct {
		release func() error
		err     error
	}
	acquired := make(chan result, 1)
	go func() { release, err := AcquireBlocking(path); acquired <- result{release, err} }()
	select {
	case got := <-acquired:
		if got.release != nil {
			_ = got.release()
		}
		t.Fatalf("blocking acquisition did not wait: %v", got.err)
	case <-time.After(30 * time.Millisecond):
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-acquired:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if err := got.release(); err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocking acquisition did not resume after release")
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

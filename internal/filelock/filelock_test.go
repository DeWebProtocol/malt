package filelock

import (
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
}

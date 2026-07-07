package writetrace

import (
	"fmt"
	"reflect"
	"testing"
)

func TestCommitProgressLoggerLogsAtIntervalAndCompletion(t *testing.T) {
	var logs []string
	progress := newCommitProgressLogger(commitProgressConfig{
		repo:     "github.com/ipfs/kubo",
		systems:  []string{"maltflat", "hamt"},
		total:    5,
		interval: 2,
		logf: func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
	})

	for replayed := 1; replayed <= 5; replayed++ {
		progress.Observe(replayed)
	}
	progress.Complete()

	want := []string{
		"  progress repository=github.com/ipfs/kubo systems=maltflat,hamt replayed=2/5 percent=40.00%",
		"  progress repository=github.com/ipfs/kubo systems=maltflat,hamt replayed=4/5 percent=80.00%",
		"  progress repository=github.com/ipfs/kubo systems=maltflat,hamt replayed=5/5 percent=100.00%",
	}
	if !reflect.DeepEqual(logs, want) {
		t.Fatalf("progress logs = %#v, want %#v", logs, want)
	}
}

func TestCommitProgressLoggerCanBeDisabled(t *testing.T) {
	var logs []string
	progress := newCommitProgressLogger(commitProgressConfig{
		repo:     "github.com/ipfs/kubo",
		systems:  []string{"maltflat"},
		total:    3,
		interval: 0,
		logf: func(format string, args ...any) {
			logs = append(logs, fmt.Sprintf(format, args...))
		},
	})

	progress.Observe(1)
	progress.Observe(3)
	progress.Complete()

	if len(logs) != 0 {
		t.Fatalf("progress logs = %#v, want disabled logger to stay silent", logs)
	}
}

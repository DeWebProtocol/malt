package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

type staticJobs []Job

func (s staticJobs) BackupJobs() ([]Job, error) { return s, nil }

type recordingRunner struct {
	requests []Request
	result   *Result
	err      error
	history  *History
}

func (r *recordingRunner) Run(_ context.Context, request Request) (*Result, error) {
	r.requests = append(r.requests, request)
	if r.history != nil && r.result != nil {
		if err := r.history.ClearPending(r.result.CandidateRoot); err != nil {
			return r.result, err
		}
	}
	return r.result, r.err
}

func TestSchedulerPersistsAttemptAndDoesNotRunEarly(t *testing.T) {
	history, err := NewHistory(filepath.Join(t.TempDir(), "backups.json"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "docs.txt")
	if err := os.WriteFile(source, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	fingerprint, err := FingerprintSource(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{result: &Result{CandidateRoot: "candidate", SourceFingerprint: fingerprint}}
	scheduler, err := NewScheduler(runner, staticJobs{{
		Name: "docs", Source: source, Every: time.Hour, Enabled: true,
	}}, history, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	scheduler.now = func() time.Time { return now }
	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("runs = %d, want 1", len(runner.requests))
	}
	now = now.Add(30 * time.Minute)
	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("early runs = %d, want 1", len(runner.requests))
	}
	now = now.Add(2 * time.Hour)
	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("unchanged source runs = %d, want 1", len(runner.requests))
	}
	states, err := history.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if states["docs"].LastSuccessAt.IsZero() || states["docs"].LastResult == nil {
		t.Fatalf("job state = %#v", states["docs"])
	}
}

func TestSchedulerRetriesPendingJobWithoutWaitingOrReadingSource(t *testing.T) {
	history, err := NewHistory(filepath.Join(t.TempDir(), "backups.json"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "missing")
	candidate := "bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"
	pendingResult := Result{
		Source: source, CandidateRoot: candidate, SourceFingerprint: "sha256:pending",
	}
	if err := history.SetPending(PendingBackup{
		BucketID: "bucket-a", JobName: "docs", Message: "snapshot",
		Result: pendingResult, CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	old := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	if err := history.update("docs", func(state *JobState) {
		state.LastCheckedAt = old
		state.LastAttemptAt = old
		state.LastError = "response lost"
	}); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{result: &pendingResult, history: history}
	scheduler, err := NewScheduler(runner, staticJobs{{
		Name: "docs", Source: source, Every: 24 * time.Hour, Enabled: true,
	}}, history, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	scheduler.now = func() time.Time { return old.Add(time.Minute) }
	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 1 || runner.requests[0].JobName != "docs" {
		t.Fatalf("pending retry requests = %#v", runner.requests)
	}
	if runner.requests[0].ExpectedFingerprint != pendingResult.SourceFingerprint {
		t.Fatalf("pending fingerprint = %q", runner.requests[0].ExpectedFingerprint)
	}
	pending, err := history.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if pending != nil {
		t.Fatalf("pending journal was not cleared: %#v", pending)
	}
}

func TestHistoryTightensExistingPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "backups.json")
	if err := os.WriteFile(path, []byte("{\"version\":1,\"jobs\":{}}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewHistory(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("history mode = %#o, want 0600", info.Mode().Perm())
	}
}

func TestSchedulerRetriesFailuresBeforeNormalInterval(t *testing.T) {
	history, err := NewHistory(filepath.Join(t.TempDir(), "backups.json"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "docs.txt")
	if err := os.WriteFile(source, []byte("retry me"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{err: errors.New("gateway unavailable")}
	scheduler, err := NewScheduler(runner, staticJobs{{
		Name: "docs", Source: source, Every: 24 * time.Hour, Enabled: true,
	}}, history, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	scheduler.now = func() time.Time { return now }
	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = now.Add(30 * time.Second)
	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("failure retried too early: %d", len(runner.requests))
	}
	now = now.Add(31 * time.Second)
	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 2 {
		t.Fatalf("failure retries = %d, want 2", len(runner.requests))
	}
	states, err := history.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if states["docs"].FailureCount != 2 {
		t.Fatalf("failure state = %#v", states["docs"])
	}
}

func TestSchedulerRetriesInterruptedAttemptBeforeNormalInterval(t *testing.T) {
	history, err := NewHistory(filepath.Join(t.TempDir(), "backups.json"))
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "docs.txt")
	if err := os.WriteFile(source, []byte("retry interrupted"), 0o600); err != nil {
		t.Fatal(err)
	}
	started := time.Date(2026, 7, 26, 12, 0, 0, 0, time.UTC)
	if err := history.update("docs", func(state *JobState) {
		state.LastCheckedAt = started
		state.LastAttemptAt = started
		state.AttemptActive = true
	}); err != nil {
		t.Fatal(err)
	}
	runner := &recordingRunner{result: &Result{SourceFingerprint: "new"}}
	scheduler, err := NewScheduler(runner, staticJobs{{
		Name: "docs", Source: source, Every: 24 * time.Hour, Enabled: true,
	}}, history, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now := started.Add(30 * time.Second)
	scheduler.now = func() time.Time { return now }
	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 0 {
		t.Fatalf("interrupted attempt retried too early: %d", len(runner.requests))
	}
	now = started.Add(time.Minute + time.Second)
	if err := scheduler.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(runner.requests) != 1 {
		t.Fatalf("interrupted attempt retries = %d, want 1", len(runner.requests))
	}
	states, err := history.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if states["docs"].AttemptActive {
		t.Fatalf("successful retry remained active: %#v", states["docs"])
	}
}

func TestSchedulerRecoversPendingBackupAfterJobRemovalOrDisable(t *testing.T) {
	for _, test := range []struct {
		name string
		jobs staticJobs
	}{
		{name: "removed"},
		{name: "disabled", jobs: staticJobs{{
			Name: "daily", Source: "/old/source", Every: 24 * time.Hour, Enabled: false,
		}}},
		{name: "source changed", jobs: staticJobs{{
			Name: "daily", Source: "/new/source", Every: 24 * time.Hour, Enabled: true,
		}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			history, err := NewHistory(filepath.Join(t.TempDir(), "backups.json"))
			if err != nil {
				t.Fatal(err)
			}
			result := &Result{
				Source:            filepath.Join(t.TempDir(), "missing"),
				CandidateRoot:     "bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku",
				SourceFingerprint: "sha256:pending",
			}
			if err := history.SetPending(PendingBackup{
				BucketID: "bucket-a", JobName: "daily", Message: "snapshot",
				Result: *result, CreatedAt: time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}
			runner := &recordingRunner{result: result, history: history}
			scheduler, err := NewScheduler(runner, test.jobs, history, time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if err := scheduler.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if len(runner.requests) != 1 || runner.requests[0].JobName != "daily" {
				t.Fatalf("orphaned pending requests = %#v", runner.requests)
			}
			pending, err := history.Pending()
			if err != nil {
				t.Fatal(err)
			}
			if pending != nil {
				t.Fatalf("orphaned pending journal remains: %#v", pending)
			}
		})
	}
}

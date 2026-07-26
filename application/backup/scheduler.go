package backup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dewebprotocol/malt-client/bucketsync"
	"github.com/dewebprotocol/malt-client/internal/durablefile"
	"github.com/dewebprotocol/malt-client/internal/filelock"
	"github.com/dewebprotocol/malt-client/internal/securefile"
)

type Job struct {
	Name      string
	Source    string
	Every     time.Duration
	Enabled   bool
	Message   string
	Protected []string
}

type JobSource interface {
	BackupJobs() ([]Job, error)
}

type Runner interface {
	Run(context.Context, Request) (*Result, error)
}

type JobState struct {
	Name          string    `json:"name"`
	LastCheckedAt time.Time `json:"last_checked_at,omitempty"`
	LastAttemptAt time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	FailureCount  int       `json:"failure_count,omitempty"`
	AttemptActive bool      `json:"attempt_active,omitempty"`
	LastResult    *Result   `json:"last_result,omitempty"`
}

type SchedulerState struct {
	LastTickAt time.Time `json:"last_tick_at,omitempty"`
	LastError  string    `json:"last_error,omitempty"`
}

type historyFile struct {
	Version   int                 `json:"version"`
	Jobs      map[string]JobState `json:"jobs"`
	Pending   *PendingBackup      `json:"pending_backup,omitempty"`
	Scheduler SchedulerState      `json:"scheduler,omitempty"`
}

// PendingBackup is the durable publication journal for an encrypted snapshot.
// It lets a later daemon or CLI process reuse the already staged candidate and
// its frozen Bucket push request after a timeout or lost response.
type PendingBackup struct {
	BucketID  string    `json:"bucket_id"`
	JobName   string    `json:"job_name,omitempty"`
	Message   string    `json:"message"`
	StashID   string    `json:"stash_id,omitempty"`
	PushID    string    `json:"push_id,omitempty"`
	Result    Result    `json:"result"`
	CreatedAt time.Time `json:"created_at"`
}

type History struct {
	mu   sync.Mutex
	path string
}

func NewHistory(path string) (*History, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("backup history path is empty")
	}
	h := &History{path: path}
	if _, err := h.Snapshot(); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *History) Snapshot() (map[string]JobState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	unlock, err := filelock.Acquire(h.path+".lock", 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("lock backup history: %w", err)
	}
	defer func() { _ = unlock() }()
	value, err := h.load()
	if err != nil {
		return nil, err
	}
	out := make(map[string]JobState, len(value.Jobs))
	for name, state := range value.Jobs {
		out[name] = state
	}
	return out, nil
}

func (h *History) Pending() (*PendingBackup, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	unlock, err := filelock.Acquire(h.path+".lock", 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("lock backup history: %w", err)
	}
	defer func() { _ = unlock() }()
	value, err := h.load()
	if err != nil || value.Pending == nil {
		return nil, err
	}
	pending := *value.Pending
	return &pending, nil
}

func (h *History) SchedulerStatus() (SchedulerState, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	unlock, err := filelock.Acquire(h.path+".lock", 10*time.Second)
	if err != nil {
		return SchedulerState{}, fmt.Errorf("lock backup history: %w", err)
	}
	defer func() { _ = unlock() }()
	value, err := h.load()
	if err != nil {
		return SchedulerState{}, err
	}
	return value.Scheduler, nil
}

func (h *History) recordSchedulerTick(at time.Time, tickErr error) error {
	return h.mutate(func(value *historyFile) error {
		value.Scheduler.LastTickAt = at.UTC()
		value.Scheduler.LastError = ""
		if tickErr != nil {
			value.Scheduler.LastError = tickErr.Error()
		}
		return nil
	})
}

func (h *History) SetPending(pending PendingBackup) error {
	if strings.TrimSpace(pending.BucketID) == "" || strings.TrimSpace(pending.Result.Source) == "" ||
		strings.TrimSpace(pending.Result.CandidateRoot) == "" || pending.CreatedAt.IsZero() {
		return fmt.Errorf("pending backup journal is incomplete")
	}
	return h.mutate(func(value *historyFile) error {
		if value.Pending != nil {
			return fmt.Errorf("pending backup candidate %s must be retried before recording another candidate", value.Pending.Result.CandidateRoot)
		}
		value.Pending = &pending
		return nil
	})
}

func (h *History) ClearPending(candidate string) error {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return fmt.Errorf("pending backup candidate is empty")
	}
	return h.mutate(func(value *historyFile) error {
		if value.Pending == nil {
			return nil
		}
		if value.Pending.Result.CandidateRoot != candidate {
			return fmt.Errorf("pending backup candidate changed from %s to %s", value.Pending.Result.CandidateRoot, candidate)
		}
		value.Pending = nil
		return nil
	})
}

// CompletePending atomically removes a publication journal and, for an
// automatic job, records its successful result. This prevents a crash between
// push completion and scheduler bookkeeping from generating another snapshot.
func (h *History) CompletePending(candidate, jobName string, result Result) error {
	candidate = strings.TrimSpace(candidate)
	jobName = strings.TrimSpace(jobName)
	if candidate == "" || result.CandidateRoot != candidate {
		return fmt.Errorf("completed backup candidate does not match its result")
	}
	return h.mutate(func(value *historyFile) error {
		if value.Pending == nil || value.Pending.Result.CandidateRoot != candidate {
			return fmt.Errorf("pending backup candidate %s is unavailable for completion", candidate)
		}
		if value.Pending.JobName != "" && value.Pending.JobName != jobName {
			return fmt.Errorf("pending backup candidate %s belongs to automatic job %q", candidate, value.Pending.JobName)
		}
		value.Pending = nil
		if jobName == "" {
			return nil
		}
		state := value.Jobs[jobName]
		state.Name = jobName
		state.LastSuccessAt = result.CompletedAt
		state.LastResult = &result
		state.LastError = ""
		state.FailureCount = 0
		state.AttemptActive = false
		value.Jobs[jobName] = state
		return nil
	})
}

func (h *History) MarkPendingStaged(candidate string, stash bucketsync.Stash) error {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" || stash.ID == "" || stash.PushID == "" || stash.CandidateRoot != candidate {
		return fmt.Errorf("staged backup journal metadata is incomplete")
	}
	return h.mutate(func(value *historyFile) error {
		if value.Pending == nil || value.Pending.Result.CandidateRoot != candidate {
			return fmt.Errorf("pending backup candidate %s is unavailable", candidate)
		}
		if stash.Base != value.Pending.Result.Base || stash.Message != value.Pending.Message ||
			stash.ChangeSetCID != "" || (stash.Status != "pending" && stash.Status != "branched") {
			return fmt.Errorf("pending backup candidate %s does not match its staged request", candidate)
		}
		if value.Pending.StashID != "" &&
			(value.Pending.StashID != stash.ID || value.Pending.PushID != stash.PushID) {
			return fmt.Errorf("pending backup candidate %s changed its frozen stash identity", candidate)
		}
		value.Pending.StashID = stash.ID
		value.Pending.PushID = stash.PushID
		return nil
	})
}

func (h *History) update(name string, update func(*JobState)) error {
	return h.mutate(func(value *historyFile) error {
		state := value.Jobs[name]
		state.Name = name
		update(&state)
		value.Jobs[name] = state
		return nil
	})
}

func (h *History) mutate(update func(*historyFile) error) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	unlock, err := filelock.Acquire(h.path+".lock", 10*time.Second)
	if err != nil {
		return fmt.Errorf("lock backup history: %w", err)
	}
	defer func() { _ = unlock() }()
	value, err := h.load()
	if err != nil {
		return err
	}
	if err := update(&value); err != nil {
		return err
	}
	return h.write(value)
}

func (h *History) load() (historyFile, error) {
	data, err := os.ReadFile(h.path)
	if errors.Is(err, os.ErrNotExist) {
		return historyFile{Version: 1, Jobs: map[string]JobState{}}, nil
	}
	if err != nil {
		return historyFile{}, fmt.Errorf("read backup history: %w", err)
	}
	if err := securefile.Secure(h.path); err != nil {
		return historyFile{}, fmt.Errorf("secure backup history permissions: %w", err)
	}
	var value historyFile
	if err := json.Unmarshal(data, &value); err != nil {
		return historyFile{}, fmt.Errorf("decode backup history: %w", err)
	}
	if value.Version != 1 {
		return historyFile{}, fmt.Errorf("unsupported backup history version %d", value.Version)
	}
	if value.Jobs == nil {
		value.Jobs = map[string]JobState{}
	}
	if value.Pending != nil && ((value.Pending.StashID == "") != (value.Pending.PushID == "")) {
		return historyFile{}, fmt.Errorf("pending backup journal has incomplete frozen stash identity")
	}
	return value, nil
}

func (h *History) write(value historyFile) error {
	if err := os.MkdirAll(filepath.Dir(h.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(h.path), ".backups-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := securefile.Secure(name); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, h.path); err != nil {
		return err
	}
	if err := durablefile.SyncParent(h.path); err != nil {
		return fmt.Errorf("sync backup history directory: %w", err)
	}
	return nil
}

type Scheduler struct {
	runner  Runner
	source  JobSource
	history *History
	poll    time.Duration
	now     func() time.Time
}

func NewScheduler(runner Runner, source JobSource, history *History, poll time.Duration) (*Scheduler, error) {
	if runner == nil || source == nil || history == nil {
		return nil, fmt.Errorf("backup scheduler runner, source, and history are required")
	}
	if poll <= 0 {
		poll = 5 * time.Second
	}
	return &Scheduler{runner: runner, source: source, history: history, poll: poll, now: time.Now}, nil
}

func (s *Scheduler) Run(ctx context.Context) {
	s.tick(ctx)
	ticker := time.NewTicker(s.poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.tick(ctx)
		}
	}
}

func (s *Scheduler) tick(ctx context.Context) {
	err := s.Tick(ctx)
	_ = s.history.recordSchedulerTick(s.now(), err)
}

// Tick reloads jobs so CLI configuration changes take effect without a daemon
// restart. Individual failures are persisted and do not stop other jobs.
func (s *Scheduler) Tick(ctx context.Context) error {
	jobs, err := s.source.BackupJobs()
	if err != nil {
		return err
	}
	states, err := s.history.Snapshot()
	if err != nil {
		return err
	}
	pending, err := s.history.Pending()
	if err != nil {
		return err
	}
	if pending != nil && !pendingHasEnabledJob(*pending, jobs) {
		name := pending.JobName
		if name == "" {
			name = "_pending_backup"
		}
		jobs = append([]Job{{
			Name: name, Source: pending.Result.Source, Every: time.Minute,
			Enabled: true, Message: pending.Message,
		}}, jobs...)
	}
	now := s.now().UTC()
	for _, job := range jobs {
		if !job.Enabled || job.Every <= 0 {
			continue
		}
		state := states[job.Name]
		jobHasPending := pending != nil && pendingMatchesJob(*pending, job)
		if jobHasPending {
			if (state.LastError != "" || state.AttemptActive) && !state.LastAttemptAt.IsZero() &&
				now.Before(state.LastAttemptAt.Add(retryDelay(job.Every, state.FailureCount))) {
				continue
			}
		} else {
			lastCheck := state.LastCheckedAt
			delay := job.Every
			if state.LastError != "" || state.AttemptActive {
				lastCheck = state.LastAttemptAt
				delay = retryDelay(job.Every, state.FailureCount)
			} else if lastCheck.IsZero() {
				lastCheck = state.LastAttemptAt
			}
			if !lastCheck.IsZero() && now.Before(lastCheck.Add(delay)) {
				continue
			}
		}
		fingerprint := ""
		var fingerprintErr error
		if jobHasPending {
			fingerprint = pending.Result.SourceFingerprint
		} else {
			if fingerprintErr = ValidateSource(job.Source, job.Protected); fingerprintErr == nil {
				fingerprint, fingerprintErr = FingerprintSource(ctx, job.Source)
			}
		}
		if !jobHasPending && fingerprintErr == nil && state.LastError == "" && state.LastResult != nil && state.LastResult.SourceFingerprint == fingerprint {
			if err := s.history.update(job.Name, func(value *JobState) {
				value.LastCheckedAt = now
				value.LastError = ""
				value.FailureCount = 0
				value.AttemptActive = false
			}); err != nil {
				return err
			}
			continue
		}
		attempt := now
		if err := s.history.update(job.Name, func(value *JobState) {
			value.LastCheckedAt = attempt
			value.LastAttemptAt = attempt
			if fingerprintErr != nil {
				value.LastError = fingerprintErr.Error()
				value.FailureCount++
				value.AttemptActive = false
			} else {
				value.LastError = ""
				value.AttemptActive = true
			}
		}); err != nil {
			return err
		}
		if fingerprintErr != nil {
			continue
		}
		result, runErr := s.runner.Run(ctx, Request{
			Source: job.Source, Message: job.Message, JobName: job.Name, ExpectedFingerprint: fingerprint,
		})
		if runErr != nil && result != nil && !result.CompletedAt.IsZero() {
			// The remote publication completed, but its final durable history
			// transition reported an error. Do not overwrite a possibly
			// committed atomic completion; a retained journal will drive the
			// next exact-ID retry if the rename was not durable.
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		if err := s.history.update(job.Name, func(value *JobState) {
			value.AttemptActive = false
			if runErr != nil {
				value.LastError = runErr.Error()
				value.FailureCount++
				if result != nil {
					value.LastResult = result
				}
				return
			}
			value.LastSuccessAt = s.now().UTC()
			value.LastResult = result
			value.LastError = ""
			value.FailureCount = 0
		}); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if jobHasPending && runErr == nil {
			pending = nil
		}
	}
	return nil
}

func retryDelay(every time.Duration, failureCount int) time.Duration {
	delay := time.Minute
	for i := 1; i < failureCount && delay < time.Hour; i++ {
		delay *= 2
	}
	if delay > time.Hour {
		delay = time.Hour
	}
	if every < delay {
		return every
	}
	return delay
}

func pendingMatchesJob(pending PendingBackup, job Job) bool {
	if pending.JobName != "" && pending.JobName != job.Name {
		return false
	}
	source, err := filepath.Abs(strings.TrimSpace(job.Source))
	return err == nil && source == pending.Result.Source
}

func pendingHasEnabledJob(pending PendingBackup, jobs []Job) bool {
	for _, job := range jobs {
		if job.Enabled && job.Every > 0 && pendingMatchesJob(pending, job) {
			return true
		}
	}
	return false
}

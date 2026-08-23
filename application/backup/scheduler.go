package backup

import (
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
	encryptedfs "github.com/dewebprotocol/malt-client/unixfs/encrypted"
)

type PlanState struct {
	Name          string    `json:"name"`
	LastCheckedAt time.Time `json:"last_checked_at,omitempty"`
	LastAttemptAt time.Time `json:"last_attempt_at,omitempty"`
	LastSuccessAt time.Time `json:"last_success_at,omitempty"`
	LastError     string    `json:"last_error,omitempty"`
	FailureCount  int       `json:"failure_count,omitempty"`
	AttemptActive bool      `json:"attempt_active,omitempty"`
	LastResult    *Result   `json:"last_result,omitempty"`
}

type historyFile struct {
	Version  int                  `json:"version"`
	Plans    map[string]PlanState `json:"plans"`
	Pending  *PendingBackup       `json:"pending_backup,omitempty"`
	Conflict *ConflictCheckout    `json:"conflict_checkout,omitempty"`
}

// PendingBackup is the durable publication journal for an encrypted snapshot.
// It lets a later daemon or CLI process reuse the already staged candidate and
// its frozen Bucket push request after a timeout or lost response.
type PendingBackup struct {
	BucketID          string    `json:"bucket_id"`
	PlanID            string    `json:"plan_id"`
	Message           string    `json:"message"`
	StashID           string    `json:"stash_id,omitempty"`
	PushID            string    `json:"push_id,omitempty"`
	CandidateBase     string    `json:"candidate_base,omitempty"`
	CandidateRecorded bool      `json:"candidate_recorded,omitempty"`
	Result            Result    `json:"result"`
	CreatedAt         time.Time `json:"created_at"`
}

type BindingConflict struct {
	BindingID   string   `json:"binding_id"`
	BindingName string   `json:"binding_name"`
	Paths       []string `json:"paths"`
}

// ConflictCheckout points to immutable base/local/remote plaintext snapshots
// prepared for a user's merge tool. It never lives inside a bound directory
// and therefore cannot be included in a subsequent encrypted snapshot.
type ConflictCheckout struct {
	PlanID     string            `json:"plan_id"`
	StashID    string            `json:"stash_id"`
	Branch     string            `json:"branch"`
	BaseRoot   string            `json:"base_root,omitempty"`
	LocalRoot  string            `json:"local_root"`
	RemoteRoot string            `json:"remote_root"`
	Path       string            `json:"path"`
	Bindings   []BindingConflict `json:"bindings"`
	CreatedAt  time.Time         `json:"created_at"`
}

type History struct {
	mu   sync.Mutex
	path string
}

func NewHistory(path string) (*History, error) {
	if path == "" {
		return nil, fmt.Errorf("backup history path is empty")
	}
	h := &History{path: path}
	if _, err := h.Snapshot(); err != nil {
		return nil, err
	}
	return h, nil
}

func (h *History) Snapshot() (map[string]PlanState, error) {
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
	out := make(map[string]PlanState, len(value.Plans))
	for name, state := range value.Plans {
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

func (h *History) Conflict() (*ConflictCheckout, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	unlock, err := filelock.Acquire(h.path+".lock", 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("lock backup history: %w", err)
	}
	defer func() { _ = unlock() }()
	value, err := h.load()
	if err != nil || value.Conflict == nil {
		return nil, err
	}
	conflict := cloneConflictCheckout(*value.Conflict)
	return &conflict, nil
}

func (h *History) SetPending(pending PendingBackup) error {
	if strings.TrimSpace(pending.BucketID) == "" || strings.TrimSpace(pending.PlanID) == "" ||
		pending.PlanID != pending.Result.PlanID || strings.TrimSpace(pending.Result.Source) == "" ||
		strings.TrimSpace(pending.Result.CandidateRoot) == "" || pending.CreatedAt.IsZero() {
		return fmt.Errorf("pending backup journal is incomplete")
	}
	if pending.Result.Profile == encryptedfs.ProfileID && !pending.CandidateRecorded {
		return fmt.Errorf("encrypted filesystem pending backup lacks a recorded local candidate")
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

// CompletePending atomically removes a publication journal and records the
// Plan's successful result. This prevents a crash between push completion and
// durable bookkeeping from generating another snapshot.
func (h *History) CompletePending(candidate, planID string, result Result) error {
	candidate = strings.TrimSpace(candidate)
	planID = strings.TrimSpace(planID)
	if candidate == "" || planID == "" || result.PlanID != planID || result.CandidateRoot != candidate {
		return fmt.Errorf("completed backup candidate does not match its result")
	}
	return h.mutate(func(value *historyFile) error {
		if value.Pending == nil || value.Pending.Result.CandidateRoot != candidate {
			return fmt.Errorf("pending backup candidate %s is unavailable for completion", candidate)
		}
		if value.Pending.PlanID != planID {
			return fmt.Errorf("pending backup candidate %s belongs to backup plan %q", candidate, value.Pending.PlanID)
		}
		value.Pending = nil
		state := value.Plans[planID]
		state.Name = planID
		state.LastSuccessAt = result.CompletedAt
		state.LastResult = &result
		state.LastError = ""
		state.FailureCount = 0
		state.AttemptActive = false
		value.Plans[planID] = state
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

func (h *History) MarkPendingConflict(candidate string, result Result) error {
	candidate = strings.TrimSpace(candidate)
	if candidate == "" || result.CandidateRoot != candidate || result.Push.Result.Status != "branched" ||
		result.Push.Result.Branch == nil || strings.TrimSpace(result.Push.Result.Branch.Name) == "" {
		return fmt.Errorf("branched backup journal metadata is incomplete")
	}
	return h.mutate(func(value *historyFile) error {
		if value.Pending == nil || value.Pending.Result.CandidateRoot != candidate {
			return fmt.Errorf("pending backup candidate %s is unavailable", candidate)
		}
		value.Pending.Result = result
		return nil
	})
}

func (h *History) SetConflictCheckout(conflict ConflictCheckout) error {
	if strings.TrimSpace(conflict.PlanID) == "" || strings.TrimSpace(conflict.StashID) == "" ||
		strings.TrimSpace(conflict.LocalRoot) == "" || strings.TrimSpace(conflict.RemoteRoot) == "" ||
		strings.TrimSpace(conflict.Path) == "" || conflict.CreatedAt.IsZero() || len(conflict.Bindings) == 0 {
		return fmt.Errorf("backup conflict checkout is incomplete")
	}
	return h.mutate(func(value *historyFile) error {
		if value.Pending == nil || value.Pending.StashID != conflict.StashID ||
			value.Pending.Result.CandidateRoot != conflict.LocalRoot {
			return fmt.Errorf("backup conflict checkout does not match the pending candidate")
		}
		copy := cloneConflictCheckout(conflict)
		value.Conflict = &copy
		return nil
	})
}

func (h *History) ClearConflictCheckout(stashID string) error {
	stashID = strings.TrimSpace(stashID)
	if stashID == "" {
		return fmt.Errorf("backup conflict stash ID is empty")
	}
	return h.mutate(func(value *historyFile) error {
		if value.Conflict != nil && value.Conflict.StashID != stashID {
			return fmt.Errorf("backup conflict checkout belongs to another stash")
		}
		value.Conflict = nil
		return nil
	})
}

func (h *History) update(name string, update func(*PlanState)) error {
	return h.mutate(func(value *historyFile) error {
		state := value.Plans[name]
		state.Name = name
		update(&state)
		value.Plans[name] = state
		return nil
	})
}

// RecordResult persists a completed plan result outside the scheduler path,
// for example after sync has safely installed a pulled remote snapshot.
func (h *History) RecordResult(name string, result Result) error {
	name = strings.TrimSpace(name)
	if name == "" || result.CompletedAt.IsZero() {
		return fmt.Errorf("backup result name and completion time are required")
	}
	return h.update(name, func(value *PlanState) {
		value.LastCheckedAt = result.CompletedAt
		value.LastSuccessAt = result.CompletedAt
		value.LastResult = &result
		value.LastError = ""
		value.FailureCount = 0
		value.AttemptActive = false
	})
}

func (h *History) RecordPlanAttempt(name string, at time.Time) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("backup plan attempt name is empty")
	}
	return h.update(name, func(value *PlanState) {
		value.LastCheckedAt = at.UTC()
		value.LastAttemptAt = at.UTC()
		value.AttemptActive = true
	})
}

func (h *History) RecordPlanFailure(name string, at time.Time, runErr error) error {
	name = strings.TrimSpace(name)
	if name == "" || runErr == nil {
		return fmt.Errorf("backup plan failure name and error are required")
	}
	return h.update(name, func(value *PlanState) {
		value.LastCheckedAt = at.UTC()
		value.LastAttemptAt = at.UTC()
		value.LastError = runErr.Error()
		value.FailureCount++
		value.AttemptActive = false
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
		return historyFile{Version: 2, Plans: map[string]PlanState{}}, nil
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
	if value.Version != 2 {
		return historyFile{}, fmt.Errorf("unsupported backup history version %d", value.Version)
	}
	if value.Plans == nil {
		value.Plans = map[string]PlanState{}
	}
	if value.Pending != nil && ((value.Pending.StashID == "") != (value.Pending.PushID == "")) {
		return historyFile{}, fmt.Errorf("pending backup journal has incomplete frozen stash identity")
	}
	if value.Conflict != nil {
		if value.Pending == nil || value.Conflict.StashID == "" || value.Conflict.StashID != value.Pending.StashID ||
			value.Conflict.LocalRoot != value.Pending.Result.CandidateRoot || value.Conflict.Path == "" ||
			len(value.Conflict.Bindings) == 0 {
			return historyFile{}, fmt.Errorf("backup conflict checkout does not match pending backup")
		}
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

func cloneConflictCheckout(value ConflictCheckout) ConflictCheckout {
	value.Bindings = append([]BindingConflict(nil), value.Bindings...)
	for i := range value.Bindings {
		value.Bindings[i].Paths = append([]string(nil), value.Bindings[i].Paths...)
	}
	return value
}

// Package journal owns the durable, ordered operation journal used by future
// filesystem write-back and offline replay. It records local intent and retry
// identity only; it cannot accept roots or interpret transport success as a
// trusted commit.
package journal

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dewebprotocol/malt-client/internal/durablefile"
	"github.com/dewebprotocol/malt-client/internal/filelock"
	"github.com/dewebprotocol/malt-client/internal/securefile"
	"github.com/dewebprotocol/malt-client/internal/strictjson"
	cid "github.com/ipfs/go-cid"
)

const journalVersion = 1

var (
	ErrNotFound      = errors.New("journal operation not found")
	ErrIdentityReuse = errors.New("journal operation identity was reused with different intent")
	ErrInvalidStatus = errors.New("invalid journal status transition")
	ErrRequestFrozen = errors.New("pending upload request identity is frozen")
)

type Kind string

const (
	KindWrite  Kind = "write"
	KindMkdir  Kind = "mkdir"
	KindRename Kind = "rename"
	KindUnlink Kind = "unlink"
)

type Status string

const (
	StatusLocalDirty    Status = "local_dirty"
	StatusOfflineOnly   Status = "offline_only"
	StatusPendingUpload Status = "pending_upload"
	StatusConflicted    Status = "conflicted"
	StatusCompleted     Status = "completed"
	StatusSuperseded    Status = "superseded"
)

// Intent is the immutable identity of a local filesystem operation. BaseRoot
// is the locally selected accepted root at the time the operation was staged.
// PayloadCID is required for writes and omitted for namespace-only mutations.
type Intent struct {
	OperationID     string `json:"operation_id"`
	RetryID         string `json:"retry_id"`
	DatasetID       string `json:"dataset_id"`
	Branch          string `json:"branch"`
	BaseRoot        string `json:"base_root"`
	BaseRevision    uint64 `json:"base_revision"`
	Kind            Kind   `json:"kind"`
	Path            string `json:"path"`
	Destination     string `json:"destination,omitempty"`
	PayloadCID      string `json:"payload_cid,omitempty"`
	EncryptionEpoch uint32 `json:"encryption_epoch"`
}

// Operation is one durable journal record. Sequence is assigned locally and
// defines replay order. ResultRoot is an untrusted/locally verified candidate
// record only; this package has no accepted-root mutation capability.
type Operation struct {
	Intent
	Sequence               uint64    `json:"sequence"`
	Status                 Status    `json:"status"`
	ConflictID             string    `json:"conflict_id,omitempty"`
	ResultRoot             string    `json:"result_root,omitempty"`
	ReplacementOperationID string    `json:"replacement_operation_id,omitempty"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

type persistedState struct {
	Version          int                  `json:"version"`
	NextSequence     uint64               `json:"next_sequence"`
	Operations       map[string]Operation `json:"operations"`
	UsedOperationIDs map[string]bool      `json:"used_operation_ids"`
	UsedRetryIDs     map[string]bool      `json:"used_retry_ids"`
}

type Store struct {
	mu    sync.Mutex
	path  string
	state persistedState
}

func Open(filePath string) (*Store, error) {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return nil, fmt.Errorf("operation-journal path is empty")
	}
	store := &Store{path: filepath.Clean(filePath), state: emptyState()}
	if err := store.withState(false, func() error { return nil }); err != nil {
		return nil, err
	}
	return store, nil
}

// NewIntent constructs stable operation and retry identities. Callers that
// need crash-safe idempotence must persist the returned intent before I/O.
func NewIntent(datasetID, branch, baseRoot string, baseRevision uint64, kind Kind, operationPath, destination, payloadCID string, encryptionEpoch uint32) (Intent, error) {
	operationID, err := randomID("op_")
	if err != nil {
		return Intent{}, err
	}
	retryID, err := randomID("retry_")
	if err != nil {
		return Intent{}, err
	}
	return normalizeIntent(Intent{
		OperationID: operationID, RetryID: retryID, DatasetID: datasetID, Branch: branch,
		BaseRoot: baseRoot, BaseRevision: baseRevision, Kind: kind, Path: operationPath,
		Destination: destination, PayloadCID: payloadCID, EncryptionEpoch: encryptionEpoch,
	})
}

// Append durably records local intent before any upload. Repeating the exact
// same intent is idempotent and returns the original operation and sequence.
func (s *Store) Append(intent Intent, initial Status) (Operation, error) {
	intent, err := normalizeIntent(intent)
	if err != nil {
		return Operation{}, err
	}
	if initial != StatusLocalDirty && initial != StatusOfflineOnly {
		return Operation{}, fmt.Errorf("new journal operation must be local_dirty or offline_only")
	}
	var result Operation
	err = s.withState(true, func() error {
		var err error
		result, err = s.appendLocked(intent, initial)
		return err
	})
	return cloneOperation(result), err
}

// FreezeForUpload changes local/offline intent to pending_upload. The immutable
// RetryID is reused for every retry, including after restart.
func (s *Store) FreezeForUpload(operationID string) (Operation, error) {
	return s.transition(operationID, StatusPendingUpload, "", "")
}

// FreezeBatchForUpload atomically freezes every selected retry identity before
// any transport I/O. Already-pending records are accepted idempotently; no
// record changes if any identity is missing or no longer replayable.
func (s *Store) FreezeBatchForUpload(operationIDs []string) ([]Operation, error) {
	ids, err := normalizeOperationIDs(operationIDs)
	if err != nil {
		return nil, err
	}
	var result []Operation
	err = s.withState(true, func() error {
		selected := make([]Operation, len(ids))
		for index, id := range ids {
			operation, ok := s.state.Operations[id]
			if !ok {
				return ErrNotFound
			}
			switch operation.Status {
			case StatusLocalDirty, StatusOfflineOnly, StatusPendingUpload:
				selected[index] = operation
			default:
				return fmt.Errorf("%w: %s to %s", ErrInvalidStatus, operation.Status, StatusPendingUpload)
			}
		}
		now := time.Now().UTC()
		for index, operation := range selected {
			if operation.Status != StatusPendingUpload {
				operation.Status = StatusPendingUpload
				operation.UpdatedAt = now
				s.state.Operations[operation.OperationID] = operation
			}
			selected[index] = operation
		}
		result = selected
		return nil
	})
	sort.Slice(result, func(i, j int) bool { return result[i].Sequence < result[j].Sequence })
	return cloneOperations(result), err
}

// MarkOffline records that a never-submitted local change is waiting for
// connectivity. A pending upload cannot be demoted because its request may
// already have reached an untrusted executor.
func (s *Store) MarkOffline(operationID string) (Operation, error) {
	return s.transition(operationID, StatusOfflineOnly, "", "")
}

func (s *Store) MarkConflicted(operationID, conflictID string) (Operation, error) {
	conflictID = strings.TrimSpace(conflictID)
	if conflictID == "" || !utf8.ValidString(conflictID) {
		return Operation{}, fmt.Errorf("journal conflict identity is empty or not valid UTF-8")
	}
	return s.transition(operationID, StatusConflicted, conflictID, "")
}

// MarkBatchConflicted atomically preserves one conflict classification across
// an exact pending batch. Repeating the same conflict is idempotent.
func (s *Store) MarkBatchConflicted(operationIDs []string, conflictID string) ([]Operation, error) {
	ids, err := normalizeOperationIDs(operationIDs)
	if err != nil {
		return nil, err
	}
	conflictID = strings.TrimSpace(conflictID)
	if conflictID == "" || !utf8.ValidString(conflictID) {
		return nil, fmt.Errorf("journal conflict identity is empty or not valid UTF-8")
	}
	var result []Operation
	err = s.withState(true, func() error {
		selected := make([]Operation, len(ids))
		for index, id := range ids {
			operation, ok := s.state.Operations[id]
			if !ok {
				return ErrNotFound
			}
			if operation.Status == StatusConflicted {
				if operation.ConflictID != conflictID {
					return ErrIdentityReuse
				}
			} else if operation.Status != StatusPendingUpload {
				return fmt.Errorf("%w: %s to %s", ErrInvalidStatus, operation.Status, StatusConflicted)
			}
			selected[index] = operation
		}
		now := time.Now().UTC()
		for index, operation := range selected {
			if operation.Status != StatusConflicted {
				operation.Status = StatusConflicted
				operation.ConflictID = conflictID
				operation.UpdatedAt = now
				s.state.Operations[operation.OperationID] = operation
			}
			selected[index] = operation
		}
		result = selected
		return nil
	})
	sort.Slice(result, func(i, j int) bool { return result[i].Sequence < result[j].Sequence })
	return cloneOperations(result), err
}

// ResolveConflict atomically supersedes a conflicted operation with new intent.
// The replacement must have new operation and retry identities so a changed
// base or payload can never reuse the frozen request identity that conflicted.
func (s *Store) ResolveConflict(operationID string, replacement Intent, initial Status) (Operation, Operation, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" || !utf8.ValidString(operationID) {
		return Operation{}, Operation{}, fmt.Errorf("journal operation identity is empty")
	}
	var err error
	replacement, err = normalizeIntent(replacement)
	if err != nil {
		return Operation{}, Operation{}, err
	}
	if initial != StatusLocalDirty && initial != StatusOfflineOnly {
		return Operation{}, Operation{}, fmt.Errorf("replacement operation must be local_dirty or offline_only")
	}
	if replacement.OperationID == operationID {
		return Operation{}, Operation{}, ErrIdentityReuse
	}
	var original, next Operation
	err = s.withState(true, func() error {
		current, ok := s.state.Operations[operationID]
		if !ok {
			return ErrNotFound
		}
		if current.Status == StatusSuperseded {
			if current.ReplacementOperationID != replacement.OperationID {
				return ErrIdentityReuse
			}
			existing, ok := s.state.Operations[replacement.OperationID]
			if !ok || existing.Intent != replacement {
				return ErrIdentityReuse
			}
			original, next = current, existing
			return nil
		}
		if current.Status != StatusConflicted {
			return fmt.Errorf("%w: %s to %s", ErrInvalidStatus, current.Status, StatusSuperseded)
		}
		if replacement.DatasetID != current.DatasetID || replacement.Branch != current.Branch {
			return fmt.Errorf("replacement operation changes dataset or branch identity")
		}
		if _, exists := s.state.Operations[replacement.OperationID]; exists {
			return ErrIdentityReuse
		}
		next, err = s.appendLocked(replacement, initial)
		if err != nil {
			return err
		}
		current.Status = StatusSuperseded
		current.ReplacementOperationID = next.OperationID
		current.UpdatedAt = time.Now().UTC()
		s.state.Operations[operationID] = current
		original = current
		return nil
	})
	return cloneOperation(original), cloneOperation(next), err
}

// Complete records a verified candidate/result root but does not accept it.
// The record remains durable until explicitly pruned.
func (s *Store) Complete(operationID, resultRoot string) (Operation, error) {
	resultRoot = strings.TrimSpace(resultRoot)
	parsed, err := cid.Parse(resultRoot)
	if err != nil {
		return Operation{}, fmt.Errorf("journal result root: %w", err)
	}
	return s.transition(operationID, StatusCompleted, "", parsed.String())
}

// CompleteBatch atomically records one locally verified candidate for every
// pending operation in the exact batch. It cannot accept the candidate root.
func (s *Store) CompleteBatch(operationIDs []string, resultRoot string) ([]Operation, error) {
	ids, err := normalizeOperationIDs(operationIDs)
	if err != nil {
		return nil, err
	}
	parsed, err := cid.Parse(strings.TrimSpace(resultRoot))
	if err != nil {
		return nil, fmt.Errorf("journal result root: %w", err)
	}
	canonicalRoot := parsed.String()
	var result []Operation
	err = s.withState(true, func() error {
		selected := make([]Operation, len(ids))
		for index, id := range ids {
			operation, ok := s.state.Operations[id]
			if !ok {
				return ErrNotFound
			}
			if operation.Status == StatusCompleted {
				if operation.ResultRoot != canonicalRoot {
					return ErrIdentityReuse
				}
			} else if operation.Status != StatusPendingUpload {
				return fmt.Errorf("%w: %s to %s", ErrInvalidStatus, operation.Status, StatusCompleted)
			}
			selected[index] = operation
		}
		now := time.Now().UTC()
		for index, operation := range selected {
			if operation.Status != StatusCompleted {
				operation.Status = StatusCompleted
				operation.ResultRoot = canonicalRoot
				operation.UpdatedAt = now
				s.state.Operations[operation.OperationID] = operation
			}
			selected[index] = operation
		}
		result = selected
		return nil
	})
	sort.Slice(result, func(i, j int) bool { return result[i].Sequence < result[j].Sequence })
	return cloneOperations(result), err
}

func (s *Store) Get(operationID string) (Operation, error) {
	operationID = strings.TrimSpace(operationID)
	if !utf8.ValidString(operationID) {
		return Operation{}, fmt.Errorf("journal operation identity is not valid UTF-8")
	}
	var result Operation
	err := s.withState(false, func() error {
		value, ok := s.state.Operations[operationID]
		if !ok {
			return ErrNotFound
		}
		result = cloneOperation(value)
		return nil
	})
	return result, err
}

// Pending is the compatibility name for Replayable.
func (s *Store) Pending() ([]Operation, error) {
	return s.Replayable()
}

// Replayable returns ordered local, offline, and pending-upload work. An
// unresolved conflict is deliberately excluded from automatic replay.
func (s *Store) Replayable() ([]Operation, error) {
	return s.list(func(status Status) bool {
		return status == StatusLocalDirty || status == StatusOfflineOnly || status == StatusPendingUpload
	})
}

// Unfinished returns all unresolved work for inspection, including conflicts.
func (s *Store) Unfinished() ([]Operation, error) {
	return s.list(func(status Status) bool {
		return status != StatusCompleted && status != StatusSuperseded
	})
}

func (s *Store) List() ([]Operation, error) {
	return s.list(func(Status) bool { return true })
}

// PruneCompleted removes completed records and their superseded conflict
// ancestors as one finished audit chain. Pending, dirty, offline, and
// unresolved conflicted operations are never discarded by this method.
// Durable operation and retry identity tombstones are retained permanently.
func (s *Store) PruneCompleted() (int, error) {
	removed := 0
	err := s.withState(true, func() error {
		selected := make(map[string]struct{})
		for id, operation := range s.state.Operations {
			if operation.Status == StatusCompleted {
				selected[id] = struct{}{}
			}
		}
		for changed := true; changed; {
			changed = false
			for id, operation := range s.state.Operations {
				if operation.Status != StatusSuperseded {
					continue
				}
				if _, ok := selected[operation.ReplacementOperationID]; !ok {
					continue
				}
				if _, ok := selected[id]; !ok {
					selected[id] = struct{}{}
					changed = true
				}
			}
		}
		for id := range selected {
			delete(s.state.Operations, id)
		}
		removed = len(selected)
		return nil
	})
	return removed, err
}

func (s *Store) list(include func(Status) bool) ([]Operation, error) {
	var result []Operation
	err := s.withState(false, func() error {
		result = make([]Operation, 0, len(s.state.Operations))
		for _, operation := range s.state.Operations {
			if !include(operation.Status) {
				continue
			}
			result = append(result, cloneOperation(operation))
		}
		sort.Slice(result, func(i, j int) bool { return result[i].Sequence < result[j].Sequence })
		return nil
	})
	return result, err
}

func (s *Store) appendLocked(intent Intent, initial Status) (Operation, error) {
	if existing, ok := s.state.Operations[intent.OperationID]; ok {
		if existing.Intent != intent {
			return Operation{}, ErrIdentityReuse
		}
		return existing, nil
	}
	if s.state.UsedOperationIDs[intent.OperationID] || s.state.UsedRetryIDs[intent.RetryID] {
		return Operation{}, ErrIdentityReuse
	}
	now := time.Now().UTC()
	result := Operation{
		Intent: intent, Sequence: s.state.NextSequence, Status: initial,
		CreatedAt: now, UpdatedAt: now,
	}
	s.state.NextSequence++
	s.state.Operations[intent.OperationID] = result
	s.state.UsedOperationIDs[intent.OperationID] = true
	s.state.UsedRetryIDs[intent.RetryID] = true
	return result, nil
}

func (s *Store) transition(operationID string, next Status, conflictID, resultRoot string) (Operation, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" || !utf8.ValidString(operationID) {
		return Operation{}, fmt.Errorf("journal operation identity is empty")
	}
	var result Operation
	err := s.withState(true, func() error {
		operation, ok := s.state.Operations[operationID]
		if !ok {
			return ErrNotFound
		}
		if operation.Status == next {
			if operation.ConflictID != conflictID || operation.ResultRoot != resultRoot {
				return ErrIdentityReuse
			}
			result = operation
			return nil
		}
		if !allowedStatusTransition(operation.Status, next) {
			if operation.Status == StatusPendingUpload && next == StatusOfflineOnly {
				return ErrRequestFrozen
			}
			return fmt.Errorf("%w: %s to %s", ErrInvalidStatus, operation.Status, next)
		}
		operation.Status = next
		operation.ConflictID = conflictID
		operation.ResultRoot = resultRoot
		operation.UpdatedAt = time.Now().UTC()
		s.state.Operations[operationID] = operation
		result = operation
		return nil
	})
	return cloneOperation(result), err
}

func (s *Store) withState(write bool, operation func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	unlock, err := filelock.Acquire(s.path+".lock", 10*time.Second)
	if err != nil {
		return fmt.Errorf("lock operation journal: %w", err)
	}
	defer func() { _ = unlock() }()
	if err := s.reloadLocked(); err != nil {
		return err
	}
	if err := operation(); err != nil {
		return err
	}
	if write {
		return s.writeLocked()
	}
	return nil
}

func (s *Store) reloadLocked() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.state = emptyState()
		return nil
	}
	if err != nil {
		return err
	}
	if err := securefile.Secure(s.path); err != nil {
		return fmt.Errorf("protect operation journal: %w", err)
	}
	if err := strictjson.ValidateUnicode(data); err != nil {
		return fmt.Errorf("decode operation journal: %w", err)
	}
	var next persistedState
	if err := json.Unmarshal(data, &next); err != nil {
		return fmt.Errorf("decode operation journal: %w", err)
	}
	if next.Version != journalVersion || next.Operations == nil || next.UsedOperationIDs == nil || next.UsedRetryIDs == nil || next.NextSequence == 0 {
		return fmt.Errorf("unsupported operation-journal version %d", next.Version)
	}
	seenSequence := map[uint64]struct{}{}
	seenRetry := map[string]struct{}{}
	for id, operation := range next.Operations {
		normalized, err := normalizeOperation(operation)
		if err != nil {
			return fmt.Errorf("journal operation %s: %w", id, err)
		}
		if normalized.OperationID != id || normalized.Sequence == 0 || normalized.Sequence >= next.NextSequence {
			return fmt.Errorf("journal operation %s has invalid persisted identity or sequence", id)
		}
		if _, ok := seenSequence[normalized.Sequence]; ok {
			return fmt.Errorf("journal contains duplicate sequence %d", normalized.Sequence)
		}
		if _, ok := seenRetry[normalized.RetryID]; ok {
			return fmt.Errorf("journal contains duplicate retry identity %q", normalized.RetryID)
		}
		seenSequence[normalized.Sequence] = struct{}{}
		seenRetry[normalized.RetryID] = struct{}{}
		if !next.UsedOperationIDs[normalized.OperationID] || !next.UsedRetryIDs[normalized.RetryID] {
			return fmt.Errorf("journal operation %s is missing durable identity tombstones", id)
		}
		next.Operations[id] = normalized
	}
	for id, used := range next.UsedOperationIDs {
		if !used || strings.TrimSpace(id) == "" || strings.TrimSpace(id) != id || !utf8.ValidString(id) {
			return fmt.Errorf("journal contains an invalid operation identity tombstone")
		}
	}
	for id, used := range next.UsedRetryIDs {
		if !used || strings.TrimSpace(id) == "" || strings.TrimSpace(id) != id || !utf8.ValidString(id) {
			return fmt.Errorf("journal contains an invalid retry identity tombstone")
		}
	}
	replacementInbound := map[string]string{}
	for id, operation := range next.Operations {
		if operation.Status != StatusSuperseded {
			continue
		}
		replacement, ok := next.Operations[operation.ReplacementOperationID]
		if !ok || replacement.Sequence <= operation.Sequence || replacement.DatasetID != operation.DatasetID || replacement.Branch != operation.Branch {
			return fmt.Errorf("superseded journal operation %s has an invalid replacement", id)
		}
		if previous, exists := replacementInbound[operation.ReplacementOperationID]; exists {
			return fmt.Errorf("superseded journal operations %s and %s share replacement %s", previous, id, operation.ReplacementOperationID)
		}
		replacementInbound[operation.ReplacementOperationID] = id
	}
	s.state = next
	return nil
}

func (s *Store) writeLocked() error {
	s.state.Version = journalVersion
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".operations-*.json")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := securefile.Secure(name); err != nil {
		_ = tmp.Close()
		return err
	}
	written, err := tmp.Write(data)
	if err != nil {
		_ = tmp.Close()
		return err
	}
	if written != len(data) {
		_ = tmp.Close()
		return io.ErrShortWrite
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, s.path); err != nil {
		return err
	}
	if err := durablefile.SyncParent(s.path); err != nil {
		return err
	}
	return securefile.Secure(s.path)
}

func normalizeOperation(operation Operation) (Operation, error) {
	intent, err := normalizeIntent(operation.Intent)
	if err != nil {
		return Operation{}, err
	}
	operation.Intent = intent
	if operation.Sequence == 0 || !validStatus(operation.Status) || operation.CreatedAt.IsZero() || operation.UpdatedAt.IsZero() || operation.UpdatedAt.Before(operation.CreatedAt) {
		return Operation{}, fmt.Errorf("operation lifecycle metadata is invalid")
	}
	operation.ConflictID = strings.TrimSpace(operation.ConflictID)
	operation.ResultRoot = strings.TrimSpace(operation.ResultRoot)
	operation.ReplacementOperationID = strings.TrimSpace(operation.ReplacementOperationID)
	if !utf8.ValidString(operation.ConflictID) || !utf8.ValidString(operation.ReplacementOperationID) {
		return Operation{}, fmt.Errorf("operation lifecycle identity is not valid UTF-8")
	}
	if operation.Status == StatusConflicted && operation.ConflictID == "" {
		return Operation{}, fmt.Errorf("conflicted operation has no conflict identity")
	}
	if operation.Status != StatusConflicted && operation.Status != StatusSuperseded && operation.ConflictID != "" {
		return Operation{}, fmt.Errorf("non-conflicted operation contains conflict identity")
	}
	if operation.Status == StatusSuperseded {
		if operation.ReplacementOperationID == "" || operation.ConflictID == "" {
			return Operation{}, fmt.Errorf("superseded operation lacks conflict or replacement identity")
		}
	} else if operation.ReplacementOperationID != "" {
		return Operation{}, fmt.Errorf("non-superseded operation contains replacement identity")
	}
	if operation.Status == StatusCompleted {
		root, err := cid.Parse(operation.ResultRoot)
		if err != nil {
			return Operation{}, fmt.Errorf("completed operation result root: %w", err)
		}
		operation.ResultRoot = root.String()
	} else if operation.ResultRoot != "" {
		return Operation{}, fmt.Errorf("incomplete operation contains result root")
	}
	return operation, nil
}

func normalizeIntent(intent Intent) (Intent, error) {
	intent.OperationID = strings.TrimSpace(intent.OperationID)
	intent.RetryID = strings.TrimSpace(intent.RetryID)
	intent.DatasetID = strings.TrimSpace(intent.DatasetID)
	intent.Branch = strings.TrimSpace(intent.Branch)
	intent.BaseRoot = strings.TrimSpace(intent.BaseRoot)
	intent.Path = strings.TrimSpace(intent.Path)
	intent.Destination = strings.TrimSpace(intent.Destination)
	intent.PayloadCID = strings.TrimSpace(intent.PayloadCID)
	if intent.OperationID == "" || intent.RetryID == "" || intent.DatasetID == "" || intent.Branch == "" {
		return Intent{}, fmt.Errorf("journal operation, retry, dataset, and branch identities are required")
	}
	if strings.ContainsRune(intent.OperationID, '\x00') || strings.ContainsRune(intent.RetryID, '\x00') ||
		strings.ContainsRune(intent.DatasetID, '\x00') || strings.ContainsRune(intent.Branch, '\x00') {
		return Intent{}, fmt.Errorf("journal identities contain NUL")
	}
	if !utf8.ValidString(intent.OperationID) || !utf8.ValidString(intent.RetryID) ||
		!utf8.ValidString(intent.DatasetID) || !utf8.ValidString(intent.Branch) {
		return Intent{}, fmt.Errorf("journal identities are not valid UTF-8")
	}
	root, err := cid.Parse(intent.BaseRoot)
	if err != nil {
		return Intent{}, fmt.Errorf("journal base root: %w", err)
	}
	intent.BaseRoot = root.String()
	if !validKind(intent.Kind) {
		return Intent{}, fmt.Errorf("unsupported journal operation kind %q", intent.Kind)
	}
	if intent.Path, err = normalizePath(intent.Path); err != nil {
		return Intent{}, fmt.Errorf("journal path: %w", err)
	}
	switch intent.Kind {
	case KindWrite:
		if intent.Destination != "" {
			return Intent{}, fmt.Errorf("write operation contains a destination")
		}
		payload, err := cid.Parse(intent.PayloadCID)
		if err != nil {
			return Intent{}, fmt.Errorf("journal payload CID: %w", err)
		}
		intent.PayloadCID = payload.String()
	case KindRename:
		if intent.PayloadCID != "" {
			return Intent{}, fmt.Errorf("rename operation contains a payload CID")
		}
		if intent.Destination, err = normalizePath(intent.Destination); err != nil {
			return Intent{}, fmt.Errorf("journal rename destination: %w", err)
		}
		if intent.Destination == intent.Path {
			return Intent{}, fmt.Errorf("journal rename source and destination are equal")
		}
	case KindMkdir, KindUnlink:
		if intent.Destination != "" || intent.PayloadCID != "" {
			return Intent{}, fmt.Errorf("namespace operation contains payload or destination metadata")
		}
	}
	return intent, nil
}

func normalizePath(value string) (string, error) {
	if value == "" || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return "", fmt.Errorf("path must be a non-empty relative slash path")
	}
	cleaned := path.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || cleaned != value {
		return "", fmt.Errorf("path is not canonical")
	}
	return cleaned, nil
}

func allowedStatusTransition(current, next Status) bool {
	switch current {
	case StatusLocalDirty:
		return next == StatusOfflineOnly || next == StatusPendingUpload || next == StatusConflicted
	case StatusOfflineOnly:
		return next == StatusLocalDirty || next == StatusPendingUpload || next == StatusConflicted
	case StatusPendingUpload:
		return next == StatusConflicted || next == StatusCompleted
	case StatusConflicted, StatusCompleted, StatusSuperseded:
		return false
	default:
		return false
	}
}

func validKind(value Kind) bool {
	switch value {
	case KindWrite, KindMkdir, KindRename, KindUnlink:
		return true
	default:
		return false
	}
}

func validStatus(value Status) bool {
	switch value {
	case StatusLocalDirty, StatusOfflineOnly, StatusPendingUpload, StatusConflicted, StatusCompleted, StatusSuperseded:
		return true
	default:
		return false
	}
}

func cloneOperation(operation Operation) Operation { return operation }

func cloneOperations(operations []Operation) []Operation {
	return append([]Operation(nil), operations...)
}

func normalizeOperationIDs(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("journal operation batch is empty")
	}
	ids := make([]string, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			return nil, fmt.Errorf("journal operation identity is empty or invalid")
		}
		if _, ok := seen[value]; ok {
			return nil, fmt.Errorf("journal operation batch contains duplicate identity %q", value)
		}
		seen[value] = struct{}{}
		ids[index] = value
	}
	return ids, nil
}

func emptyState() persistedState {
	return persistedState{
		Version: journalVersion, NextSequence: 1, Operations: map[string]Operation{},
		UsedOperationIDs: map[string]bool{}, UsedRetryIDs: map[string]bool{},
	}
}

func randomID(prefix string) (string, error) {
	data := make([]byte, 18)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(data), nil
}

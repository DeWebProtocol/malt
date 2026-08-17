// Package cache owns non-authoritative local payload-cache state for the MALT
// runtime. Every read is bound to an exact dataset, branch, root, revision,
// payload CID, and encryption epoch. A cache hit rechecks the payload CID and
// requires a caller-supplied local proof verifier before bytes are returned.
package cache

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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

const metadataVersion = 1

var (
	ErrMiss            = errors.New("cache entry not found")
	ErrBindingMismatch = errors.New("cache entry exists under a different root, revision, or encryption epoch")
	ErrNotVerified     = errors.New("cache entry is not verified clean data")
	ErrCorrupt         = errors.New("cache payload is missing or corrupt")
	ErrInvalidState    = errors.New("invalid cache state transition")
)

// State distinguishes remote materialization, verified bytes, and every local
// mutation state. Only StateVerifiedClean can pass ReadVerified.
type State string

const (
	StateUnmaterializedRemote State = "unmaterialized_remote"
	StateVerifiedClean        State = "verified_clean"
	StateLocalDirty           State = "local_dirty"
	StatePendingUpload        State = "pending_upload"
	StateCandidate            State = "candidate"
	StateConflicted           State = "conflicted"
	StateOfflineOnly          State = "offline_only"
	StateStale                State = "stale"
)

// Binding is the complete identity of one cached payload. EncryptionEpoch is
// zero only when encryption is disabled for the selected dataset.
type Binding struct {
	DatasetID       string  `json:"dataset_id"`
	Branch          string  `json:"branch"`
	Root            cid.Cid `json:"root"`
	Revision        uint64  `json:"revision"`
	CID             cid.Cid `json:"cid"`
	EncryptionEpoch uint32  `json:"encryption_epoch"`
}

// VerificationEvidence is opaque to the cache. The verifier that originally
// accepted the ProofList chooses Profile and Evidence encoding and must verify
// them again on every cache hit.
type VerificationEvidence struct {
	Profile    string    `json:"profile"`
	Evidence   []byte    `json:"evidence"`
	VerifiedAt time.Time `json:"verified_at"`
}

// Entry is durable metadata. BodyPresent says only that a body was staged; it
// is never proof of integrity or authority.
type Entry struct {
	Binding      Binding               `json:"binding"`
	State        State                 `json:"state"`
	Verification *VerificationEvidence `json:"verification,omitempty"`
	BodyPresent  bool                  `json:"body_present"`
	Size         int64                 `json:"size"`
	UpdatedAt    time.Time             `json:"updated_at"`
}

// ProofVerifier validates the cached proof evidence against the exact caller-
// selected binding. Implementations must run local malt-core verification;
// transport assertions are not sufficient.
type ProofVerifier interface {
	VerifyCached(context.Context, Binding, VerificationEvidence) error
}

// ProofVerifierFunc adapts a function to ProofVerifier.
type ProofVerifierFunc func(context.Context, Binding, VerificationEvidence) error

func (f ProofVerifierFunc) VerifyCached(ctx context.Context, binding Binding, evidence VerificationEvidence) error {
	return f(ctx, binding, evidence)
}

type metadata struct {
	Version int              `json:"version"`
	Entries map[string]Entry `json:"entries"`
}

type Store struct {
	mu                sync.Mutex
	dir               string
	metaPath          string
	state             metadata
	removeFile        func(string) error
	metadataWriteHook func() error
}

// Open creates or opens one owner-private cache directory.
func Open(directory string) (*Store, error) {
	directory = strings.TrimSpace(directory)
	if directory == "" {
		return nil, fmt.Errorf("cache directory is empty")
	}
	directory = filepath.Clean(directory)
	store := &Store{
		dir:        directory,
		metaPath:   filepath.Join(directory, "metadata.json"),
		state:      emptyMetadata(),
		removeFile: os.Remove,
	}
	if err := store.withState(false, func() error { return nil }); err != nil {
		return nil, err
	}
	return store, nil
}

// RecordRemote records identity for a payload that has not been materialized.
func (s *Store) RecordRemote(binding Binding) (Entry, error) {
	binding, err := normalizeBinding(binding)
	if err != nil {
		return Entry{}, err
	}
	var result Entry
	err = s.withState(true, func() error {
		id := bindingID(binding)
		if existing, ok := s.state.Entries[id]; ok {
			result = cloneEntry(existing)
			return nil
		}
		result = Entry{Binding: binding, State: StateUnmaterializedRemote, UpdatedAt: time.Now().UTC()}
		s.state.Entries[id] = cloneEntry(result)
		return nil
	})
	return result, err
}

// PutVerified stages payload bytes after the caller has locally verified the
// path ProofList. It independently verifies the payload CID before recording
// StateVerifiedClean.
func (s *Store) PutVerified(binding Binding, body []byte, evidence VerificationEvidence) (Entry, error) {
	binding, err := normalizeBinding(binding)
	if err != nil {
		return Entry{}, err
	}
	evidence.Profile = strings.TrimSpace(evidence.Profile)
	if evidence.Profile == "" || !utf8.ValidString(evidence.Profile) || len(evidence.Evidence) == 0 || evidence.VerifiedAt.IsZero() {
		return Entry{}, fmt.Errorf("verified cache evidence is incomplete")
	}
	if err := verifyPayloadCID(binding.CID, body); err != nil {
		return Entry{}, err
	}
	copyEvidence := cloneEvidence(evidence)
	entry := Entry{
		Binding: binding, State: StateVerifiedClean, Verification: &copyEvidence,
		BodyPresent: true, Size: int64(len(body)), UpdatedAt: time.Now().UTC(),
	}
	if err := s.putWithBody(entry, body); err != nil {
		return Entry{}, err
	}
	return cloneEntry(entry), nil
}

// PutLocal stages CID-bound local bytes as local_dirty or offline_only. Pending
// and conflict states require explicit Transition calls; this method cannot
// create verified-clean state or accept proof evidence.
func (s *Store) PutLocal(binding Binding, body []byte, state State) (Entry, error) {
	binding, err := normalizeBinding(binding)
	if err != nil {
		return Entry{}, err
	}
	if state != StateLocalDirty && state != StateOfflineOnly {
		return Entry{}, fmt.Errorf("%w: cannot stage local body as %q", ErrInvalidState, state)
	}
	if err := verifyPayloadCID(binding.CID, body); err != nil {
		return Entry{}, err
	}
	entry := Entry{
		Binding: binding, State: state, BodyPresent: true,
		Size: int64(len(body)), UpdatedAt: time.Now().UTC(),
	}
	if err := s.putWithBody(entry, body); err != nil {
		return Entry{}, err
	}
	return cloneEntry(entry), nil
}

// ReadVerified returns bytes only after exact binding checks, payload-CID
// verification, and a fresh local verification of the cached proof evidence.
func (s *Store) ReadVerified(ctx context.Context, binding Binding, verifier ProofVerifier) ([]byte, Entry, error) {
	if verifier == nil {
		return nil, Entry{}, fmt.Errorf("cache proof verifier is nil")
	}
	binding, err := normalizeBinding(binding)
	if err != nil {
		return nil, Entry{}, err
	}
	var entry Entry
	var body []byte
	err = s.withState(false, func() error {
		id := bindingID(binding)
		value, ok := s.state.Entries[id]
		if !ok {
			if hasRelatedBinding(s.state.Entries, binding) {
				return ErrBindingMismatch
			}
			return ErrMiss
		}
		entry = cloneEntry(value)
		if value.State != StateVerifiedClean || value.Verification == nil {
			return ErrNotVerified
		}
		if !value.BodyPresent {
			return s.markCorruptLocked(id, value, ErrCorrupt)
		}
		body, err = os.ReadFile(s.bodyPath(id))
		if err != nil {
			return s.markCorruptLocked(id, value, errors.Join(ErrCorrupt, err))
		}
		if int64(len(body)) != value.Size {
			return s.markCorruptLocked(id, value, ErrCorrupt)
		}
		if err := verifyPayloadCID(binding.CID, body); err != nil {
			return s.markCorruptLocked(id, value, errors.Join(ErrCorrupt, err))
		}
		return nil
	})
	if err != nil {
		return nil, entry, err
	}
	if err := verifier.VerifyCached(ctx, binding, cloneEvidence(*entry.Verification)); err != nil {
		verificationErr := fmt.Errorf("verify cached ProofList evidence: %w", err)
		if invalidateErr := s.invalidate(binding, *entry.Verification); invalidateErr != nil {
			return nil, entry, errors.Join(verificationErr, fmt.Errorf("persist invalid cache evidence: %w", invalidateErr))
		}
		return nil, entry, verificationErr
	}
	return append([]byte(nil), body...), entry, nil
}

// ReadLocal returns exact CID-bound local bytes for dirty/journal workflows.
// It never treats them as remotely verified data.
func (s *Store) ReadLocal(binding Binding) ([]byte, Entry, error) {
	binding, err := normalizeBinding(binding)
	if err != nil {
		return nil, Entry{}, err
	}
	var entry Entry
	var body []byte
	err = s.withState(false, func() error {
		id := bindingID(binding)
		value, ok := s.state.Entries[id]
		if !ok {
			return ErrMiss
		}
		entry = cloneEntry(value)
		if value.State == StateVerifiedClean || value.State == StateUnmaterializedRemote || value.State == StateStale || !value.BodyPresent {
			return ErrInvalidState
		}
		body, err = os.ReadFile(s.bodyPath(id))
		if err != nil {
			return errors.Join(ErrCorrupt, err)
		}
		if int64(len(body)) != value.Size {
			return ErrCorrupt
		}
		return verifyPayloadCID(binding.CID, body)
	})
	if err != nil {
		return nil, entry, err
	}
	return append([]byte(nil), body...), entry, err
}

// Transition changes only the local cache state. Verified-clean entries can
// only become stale; creating verified-clean data always requires PutVerified.
func (s *Store) Transition(binding Binding, next State) (Entry, error) {
	binding, err := normalizeBinding(binding)
	if err != nil {
		return Entry{}, err
	}
	var result Entry
	err = s.withState(true, func() error {
		id := bindingID(binding)
		entry, ok := s.state.Entries[id]
		if !ok {
			return ErrMiss
		}
		if !allowedTransition(entry.State, next) {
			return fmt.Errorf("%w: %s to %s", ErrInvalidState, entry.State, next)
		}
		entry.State = next
		entry.UpdatedAt = time.Now().UTC()
		if next != StateVerifiedClean {
			entry.Verification = nil
		}
		s.state.Entries[id] = entry
		result = cloneEntry(entry)
		return nil
	})
	return result, err
}

// ReconcileLocalState aligns non-authoritative cache metadata with the
// durable operation journal after a crash or a batch transition. It accepts
// only local-body states, verifies the exact body and CID under the cache
// lock, and cannot create verified-clean data or proof evidence.
func (s *Store) ReconcileLocalState(binding Binding, next State) (Entry, error) {
	binding, err := normalizeBinding(binding)
	if err != nil {
		return Entry{}, err
	}
	if !isLocalBodyState(next) {
		return Entry{}, fmt.Errorf("%w: cannot reconcile local body as %q", ErrInvalidState, next)
	}
	var result Entry
	err = s.withState(false, func() error {
		id := bindingID(binding)
		entry, ok := s.state.Entries[id]
		if !ok {
			return ErrMiss
		}
		if !isLocalBodyState(entry.State) || !entry.BodyPresent {
			return fmt.Errorf("%w: cannot reconcile %s as %s", ErrInvalidState, entry.State, next)
		}
		body, err := os.ReadFile(s.bodyPath(id))
		if err != nil {
			return errors.Join(ErrCorrupt, err)
		}
		if int64(len(body)) != entry.Size {
			return ErrCorrupt
		}
		if err := verifyPayloadCID(binding.CID, body); err != nil {
			return errors.Join(ErrCorrupt, err)
		}
		entry.State = next
		entry.Verification = nil
		entry.UpdatedAt = time.Now().UTC()
		s.state.Entries[id] = entry
		if err := s.writeMetadataLocked(); err != nil {
			return err
		}
		result = cloneEntry(entry)
		return nil
	})
	return result, err
}

func (s *Store) Inspect(binding Binding) (Entry, error) {
	binding, err := normalizeBinding(binding)
	if err != nil {
		return Entry{}, err
	}
	var result Entry
	err = s.withState(false, func() error {
		entry, ok := s.state.Entries[bindingID(binding)]
		if !ok {
			return ErrMiss
		}
		result = cloneEntry(entry)
		return nil
	})
	return result, err
}

func (s *Store) List() ([]Entry, error) {
	var result []Entry
	err := s.withState(false, func() error {
		result = make([]Entry, 0, len(s.state.Entries))
		for _, entry := range s.state.Entries {
			result = append(result, cloneEntry(entry))
		}
		sort.Slice(result, func(i, j int) bool {
			return bindingID(result[i].Binding) < bindingID(result[j].Binding)
		})
		return nil
	})
	return result, err
}

func (s *Store) Remove(binding Binding) error {
	binding, err := normalizeBinding(binding)
	if err != nil {
		return err
	}
	id := bindingID(binding)
	return s.withState(false, func() error {
		if _, ok := s.state.Entries[id]; !ok {
			return ErrMiss
		}
		if err := s.removeFile(s.bodyPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove cache body: %w", err)
		}
		delete(s.state.Entries, id)
		return s.writeMetadataLocked()
	})
}

func (s *Store) putWithBody(entry Entry, body []byte) error {
	return s.withState(false, func() error {
		id := bindingID(entry.Binding)
		existing, existed := s.state.Entries[id]
		if existed && !allowedPut(existing.State, entry.State) {
			return fmt.Errorf("%w: Put cannot replace %s with %s", ErrInvalidState, existing.State, entry.State)
		}
		if err := s.writeBodyLocked(id, body); err != nil {
			return err
		}
		s.state.Entries[id] = cloneEntry(entry)
		if err := s.writeMetadataLocked(); err != nil {
			if !existed || !existing.BodyPresent {
				if removeErr := s.removeFile(s.bodyPath(id)); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
					return errors.Join(err, fmt.Errorf("remove uncommitted cache body: %w", removeErr))
				}
			}
			return err
		}
		return nil
	})
}

func (s *Store) invalidate(binding Binding, rejected VerificationEvidence) error {
	return s.withState(true, func() error {
		id := bindingID(binding)
		entry, ok := s.state.Entries[id]
		if !ok {
			return ErrMiss
		}
		if entry.Verification == nil || !sameEvidence(*entry.Verification, rejected) {
			return nil
		}
		entry.State = StateStale
		entry.Verification = nil
		entry.UpdatedAt = time.Now().UTC()
		s.state.Entries[id] = entry
		return nil
	})
}

func (s *Store) markCorruptLocked(id string, entry Entry, cause error) error {
	entry.State = StateStale
	entry.Verification = nil
	entry.UpdatedAt = time.Now().UTC()
	s.state.Entries[id] = entry
	if err := s.writeMetadataLocked(); err != nil {
		return errors.Join(cause, fmt.Errorf("persist stale cache state: %w", err))
	}
	return cause
}

func (s *Store) withState(write bool, operation func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(s.dir, "blobs"), 0o700); err != nil {
		return err
	}
	unlock, err := filelock.Acquire(s.metaPath+".lock", 10*time.Second)
	if err != nil {
		return fmt.Errorf("lock cache metadata: %w", err)
	}
	defer func() { _ = unlock() }()
	if err := s.reloadLocked(); err != nil {
		return err
	}
	if err := operation(); err != nil {
		return err
	}
	if write {
		return s.writeMetadataLocked()
	}
	return nil
}

func (s *Store) reloadLocked() error {
	data, err := os.ReadFile(s.metaPath)
	if errors.Is(err, os.ErrNotExist) {
		s.state = emptyMetadata()
		return s.reconcileLocked()
	}
	if err != nil {
		return err
	}
	if err := securefile.Secure(s.metaPath); err != nil {
		return fmt.Errorf("protect cache metadata: %w", err)
	}
	if err := strictjson.ValidateUnicode(data); err != nil {
		return fmt.Errorf("decode cache metadata: %w", err)
	}
	var next metadata
	if err := json.Unmarshal(data, &next); err != nil {
		return fmt.Errorf("decode cache metadata: %w", err)
	}
	if next.Version != metadataVersion || next.Entries == nil {
		return fmt.Errorf("unsupported cache metadata version %d", next.Version)
	}
	for id, entry := range next.Entries {
		normalized, err := normalizeEntry(entry)
		if err != nil {
			return fmt.Errorf("cache entry %s: %w", id, err)
		}
		if bindingID(normalized.Binding) != id {
			return fmt.Errorf("cache entry %s has mismatched binding identity", id)
		}
		next.Entries[id] = normalized
	}
	s.state = next
	return s.reconcileLocked()
}

func (s *Store) writeMetadataLocked() error {
	if s.metadataWriteHook != nil {
		if err := s.metadataWriteHook(); err != nil {
			return err
		}
	}
	s.state.Version = metadataVersion
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(s.metaPath, ".cache-metadata-*.json", append(data, '\n'))
}

func (s *Store) reconcileLocked() error {
	referenced := make(map[string]struct{})
	for id, entry := range s.state.Entries {
		if entry.BodyPresent {
			referenced[id+".blob"] = struct{}{}
		}
	}
	blobDirectory := filepath.Join(s.dir, "blobs")
	entries, err := os.ReadDir(blobDirectory)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		_, isReferenced := referenced[name]
		if !isReferenced && (strings.HasSuffix(name, ".blob") || strings.HasPrefix(name, ".cache-body-")) {
			if err := s.removeFile(filepath.Join(blobDirectory, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove orphaned cache body: %w", err)
			}
		}
	}
	rootEntries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, entry := range rootEntries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), ".cache-metadata-") {
			continue
		}
		if err := s.removeFile(filepath.Join(s.dir, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove orphaned cache metadata: %w", err)
		}
	}
	return nil
}

func (s *Store) writeBodyLocked(id string, body []byte) error {
	return writeAtomic(s.bodyPath(id), ".cache-body-*.blob", body)
}

func (s *Store) bodyPath(id string) string {
	return filepath.Join(s.dir, "blobs", id+".blob")
}

func writeAtomic(target, pattern string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), pattern)
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
	if err := os.Rename(name, target); err != nil {
		return err
	}
	if err := durablefile.SyncParent(target); err != nil {
		return err
	}
	return securefile.Secure(target)
}

func normalizeEntry(entry Entry) (Entry, error) {
	var err error
	entry.Binding, err = normalizeBinding(entry.Binding)
	if err != nil {
		return Entry{}, err
	}
	if !validState(entry.State) || entry.UpdatedAt.IsZero() || entry.Size < 0 {
		return Entry{}, fmt.Errorf("invalid state metadata")
	}
	if !entry.BodyPresent && entry.Size != 0 {
		return Entry{}, fmt.Errorf("bodyless entry has a nonzero size")
	}
	switch entry.State {
	case StateUnmaterializedRemote:
		if entry.BodyPresent || entry.Size != 0 || entry.Verification != nil {
			return Entry{}, fmt.Errorf("unmaterialized entry contains body or verification metadata")
		}
	case StateVerifiedClean:
		if entry.Verification == nil || !entry.BodyPresent || entry.Verification.VerifiedAt.IsZero() || strings.TrimSpace(entry.Verification.Profile) == "" || !utf8.ValidString(entry.Verification.Profile) || len(entry.Verification.Evidence) == 0 {
			return Entry{}, fmt.Errorf("verified-clean entry lacks verification evidence or body")
		}
	case StateLocalDirty, StatePendingUpload, StateCandidate, StateConflicted, StateOfflineOnly:
		if !entry.BodyPresent || entry.Verification != nil {
			return Entry{}, fmt.Errorf("local mutation entry lacks a body or contains verification evidence")
		}
	case StateStale:
		if entry.Verification != nil {
			return Entry{}, fmt.Errorf("stale entry contains verification evidence")
		}
	}
	return cloneEntry(entry), nil
}

func normalizeBinding(binding Binding) (Binding, error) {
	binding.DatasetID = strings.TrimSpace(binding.DatasetID)
	binding.Branch = strings.TrimSpace(binding.Branch)
	if binding.DatasetID == "" || binding.Branch == "" || !utf8.ValidString(binding.DatasetID) || !utf8.ValidString(binding.Branch) || strings.ContainsRune(binding.DatasetID, '\x00') || strings.ContainsRune(binding.Branch, '\x00') {
		return Binding{}, fmt.Errorf("cache dataset and branch are required")
	}
	if !binding.Root.Defined() || !binding.CID.Defined() {
		return Binding{}, fmt.Errorf("cache root and payload CID are required")
	}
	root, err := cid.Parse(binding.Root.String())
	if err != nil {
		return Binding{}, fmt.Errorf("cache root: %w", err)
	}
	payload, err := cid.Parse(binding.CID.String())
	if err != nil {
		return Binding{}, fmt.Errorf("cache payload CID: %w", err)
	}
	binding.Root, binding.CID = root, payload
	return binding, nil
}

func verifyPayloadCID(expected cid.Cid, body []byte) error {
	actual, err := expected.Prefix().Sum(body)
	if err != nil {
		return fmt.Errorf("compute cached payload CID: %w", err)
	}
	if !actual.Equals(expected) {
		return fmt.Errorf("payload CID mismatch: expected %s, got %s", expected, actual)
	}
	return nil
}

func bindingID(binding Binding) string {
	data, _ := json.Marshal(binding)
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func hasRelatedBinding(entries map[string]Entry, binding Binding) bool {
	for _, entry := range entries {
		if entry.Binding.DatasetID == binding.DatasetID && entry.Binding.Branch == binding.Branch && entry.Binding.CID.Equals(binding.CID) {
			return true
		}
	}
	return false
}

func allowedTransition(current, next State) bool {
	if current == next {
		return true
	}
	switch current {
	case StateVerifiedClean, StateUnmaterializedRemote:
		return next == StateStale
	case StateLocalDirty:
		return next == StatePendingUpload || next == StateOfflineOnly || next == StateConflicted || next == StateStale
	case StateOfflineOnly:
		return next == StatePendingUpload || next == StateLocalDirty || next == StateConflicted || next == StateStale
	case StatePendingUpload:
		return next == StateCandidate || next == StateConflicted || next == StateStale
	case StateCandidate:
		return next == StatePendingUpload || next == StateLocalDirty || next == StateOfflineOnly || next == StateConflicted || next == StateStale
	case StateConflicted:
		return next == StateLocalDirty || next == StateOfflineOnly || next == StateStale
	case StateStale:
		return false
	default:
		return false
	}
}

func allowedPut(current, next State) bool {
	if current == next {
		return true
	}
	switch current {
	case StateUnmaterializedRemote, StateStale:
		return next == StateVerifiedClean || next == StateLocalDirty || next == StateOfflineOnly
	default:
		return false
	}
}

func validState(value State) bool {
	switch value {
	case StateUnmaterializedRemote, StateVerifiedClean, StateLocalDirty, StatePendingUpload, StateCandidate, StateConflicted, StateOfflineOnly, StateStale:
		return true
	default:
		return false
	}
}

func isLocalBodyState(value State) bool {
	switch value {
	case StateLocalDirty, StatePendingUpload, StateCandidate, StateConflicted, StateOfflineOnly:
		return true
	default:
		return false
	}
}

func cloneEntry(entry Entry) Entry {
	if entry.Verification != nil {
		value := cloneEvidence(*entry.Verification)
		entry.Verification = &value
	}
	return entry
}

func cloneEvidence(evidence VerificationEvidence) VerificationEvidence {
	evidence.Evidence = append([]byte(nil), evidence.Evidence...)
	return evidence
}

func sameEvidence(left, right VerificationEvidence) bool {
	return left.Profile == right.Profile && left.VerifiedAt.Equal(right.VerifiedAt) && bytes.Equal(left.Evidence, right.Evidence)
}

func emptyMetadata() metadata {
	return metadata{Version: metadataVersion, Entries: map[string]Entry{}}
}

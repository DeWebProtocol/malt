// Package truststore persists the roots accepted by the local MALT runtime.
// Remote observations, locally verified candidates, and accepted roots are
// separate states and never promote one another without an explicit policy
// action.
package trust

import (
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

	"github.com/dewebprotocol/malt-client/internal/durablefile"
	"github.com/dewebprotocol/malt-client/internal/securefile"
	cid "github.com/ipfs/go-cid"
)

const (
	trustStoreVersion    = 2
	legacyRecoverySuffix = ".v1-recovery"
)

var (
	ErrNotFound               = errors.New("trusted-root alias not found")
	ErrNoAcceptedRoot         = errors.New("trusted-root alias has no accepted root")
	ErrCandidateNotFound      = errors.New("candidate root not found")
	ErrObservationNotFound    = errors.New("observed root not found")
	ErrStaleCandidate         = errors.New("candidate is based on a stale accepted root")
	ErrAcceptedRootChanged    = errors.New("accepted root changed before guarded operation")
	ErrStaleObservation       = errors.New("remote head observation is stale")
	ErrConflictingObservation = errors.New("remote head observation conflicts at the same revision")
)

// CandidateRoot is a locally computed or strictly verified update candidate.
// It is never an accepted root until AcceptCandidate succeeds.
type CandidateRoot struct {
	Root       string    `json:"root"`
	BaseRoot   string    `json:"base_root,omitempty"`
	Source     string    `json:"source,omitempty"`
	ObservedAt time.Time `json:"observed_at"`
}

// Candidate is the pre-v2 compatibility name for CandidateRoot.
type Candidate = CandidateRoot

// AcceptedRootState is the only authoritative root state used by local reads.
type AcceptedRootState struct {
	Root         string    `json:"root"`
	PreviousRoot string    `json:"previous_root,omitempty"`
	Source       string    `json:"source,omitempty"`
	AcceptedAt   time.Time `json:"accepted_at"`
}

// ObservedHead records an untrusted remote head without making it a candidate
// or an accepted root. Source, dataset, and branch identify one observation
// stream whose revision must not regress.
type ObservedHead struct {
	Source     string    `json:"source"`
	DatasetID  string    `json:"dataset_id"`
	Branch     string    `json:"branch"`
	CommitID   string    `json:"commit_id,omitempty"`
	Root       string    `json:"root,omitempty"`
	Revision   uint64    `json:"revision"`
	ObservedAt time.Time `json:"observed_at"`
}

// RootState is the v2 persisted trust-plane model. Accepted may be nil when a
// runtime has observed a dataset before the user establishes initial trust.
type RootState struct {
	Alias         string             `json:"alias"`
	Profile       string             `json:"profile,omitempty"`
	Gateway       string             `json:"gateway,omitempty"`
	Accepted      *AcceptedRootState `json:"accepted,omitempty"`
	Candidates    []CandidateRoot    `json:"candidates,omitempty"`
	ObservedHeads []ObservedHead     `json:"observed_heads,omitempty"`
}

// Record retains the exact pre-v2 flattened API. New code that needs the
// explicit observation plane should use Store.GetState/ListStates.
type Record struct {
	Alias        string          `json:"alias"`
	Profile      string          `json:"profile,omitempty"`
	Gateway      string          `json:"gateway,omitempty"`
	AcceptedRoot string          `json:"accepted_root"`
	PreviousRoot string          `json:"previous_root,omitempty"`
	Source       string          `json:"source,omitempty"`
	AcceptedAt   time.Time       `json:"accepted_at"`
	Candidates   []CandidateRoot `json:"candidates,omitempty"`
}

type state struct {
	Version int                  `json:"version"`
	Roots   map[string]RootState `json:"roots"`
}

type legacyState struct {
	Version int               `json:"version"`
	Roots   map[string]Record `json:"roots"`
}

type storeFileOps struct {
	secure     func(string) error
	syncParent func(string) error
	rename     func(string, string) error
	write      func(*os.File, []byte) (int, error)
}

type Store struct {
	mu         sync.Mutex
	path       string
	state      state
	files      storeFileOps
	legacyData []byte
}

func Open(path string) (*Store, error) {
	return openWithFileOps(path, defaultStoreFileOps())
}

func openWithFileOps(path string, files storeFileOps) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("trust-store path is empty")
	}
	s := &Store{path: path, state: emptyState(), files: files}
	if err := s.withLockedState(func() error { return nil }); err != nil {
		return nil, err
	}
	return s, nil
}

func defaultStoreFileOps() storeFileOps {
	return storeFileOps{
		secure:     securefile.Secure,
		syncParent: durablefile.SyncParent,
		rename:     os.Rename,
		write:      func(file *os.File, data []byte) (int, error) { return file.Write(data) },
	}
}

// LegacyRecoveryPath returns the owner-only recovery artifact retained when a
// schema-v1 trust store is first migrated to schema v2. The runtime never
// deletes this exact-byte copy automatically; operators may remove it after
// they no longer require rollback to a pre-v2 runtime.
func LegacyRecoveryPath(path string) string {
	return path + legacyRecoverySuffix
}

func (s *Store) List() ([]Record, error) {
	states, err := s.ListStates()
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(states))
	for _, value := range states {
		if value.Accepted == nil {
			continue
		}
		out = append(out, recordFromState(value))
	}
	return out, nil
}

func (s *Store) ListStates() ([]RootState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadWithFileLock(); err != nil {
		return nil, err
	}
	out := make([]RootState, 0, len(s.state.Roots))
	for _, value := range s.state.Roots {
		out = append(out, cloneRootState(value))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Alias < out[j].Alias })
	return out, nil
}

func (s *Store) Get(alias string) (Record, error) {
	value, err := s.GetState(alias)
	if err != nil {
		return Record{}, err
	}
	if value.Accepted == nil {
		return Record{}, ErrNotFound
	}
	return recordFromState(value), nil
}

func (s *Store) GetState(alias string) (RootState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.reloadWithFileLock(); err != nil {
		return RootState{}, err
	}
	value, ok := s.state.Roots[normalizeAlias(alias)]
	if !ok {
		return RootState{}, ErrNotFound
	}
	return cloneRootState(value), nil
}

// WithAcceptedRoot runs operation while holding the same process and
// cross-process locks used by every accepted-root promotion. The callback must
// not call back into this Store. It is intended for a short local durable
// classification that must be fenced against Trust, AcceptCandidate, and
// AcceptObserved.
func (s *Store) WithAcceptedRoot(alias, expectedRoot string, operation func() error) error {
	alias = normalizeAlias(alias)
	if alias == "" {
		return fmt.Errorf("trusted-root alias is empty")
	}
	expected, err := canonicalCID(expectedRoot)
	if err != nil {
		return fmt.Errorf("invalid expected accepted root: %w", err)
	}
	if operation == nil {
		return fmt.Errorf("accepted-root guarded operation is nil")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, migrated, err := s.lockAndReload()
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	if migrated {
		if err := s.finishMigrationLocked(); err != nil {
			return err
		}
	}
	value, ok := s.state.Roots[alias]
	if !ok {
		return ErrNotFound
	}
	if value.Accepted == nil {
		return ErrNoAcceptedRoot
	}
	if value.Accepted.Root != expected {
		return fmt.Errorf("%w: expected %s, current %s", ErrAcceptedRootChanged, expected, value.Accepted.Root)
	}
	return operation()
}

func (s *Store) Trust(alias, root, profile, gateway, source string) (Record, error) {
	alias = normalizeAlias(alias)
	if alias == "" {
		return Record{}, fmt.Errorf("trusted-root alias is empty")
	}
	canonicalRoot, err := canonicalCID(root)
	if err != nil {
		return Record{}, fmt.Errorf("invalid trusted root: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, migrated, err := s.lockAndReload()
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = unlock() }()
	if err := s.prepareMigrationLocked(migrated); err != nil {
		return Record{}, err
	}
	value := s.state.Roots[alias]
	value.Alias = alias
	value.Profile = profile
	value.Gateway = gateway
	acceptRoot(&value, canonicalRoot, source, time.Now().UTC())
	s.state.Roots[alias] = value
	if err := s.writeLocked(); err != nil {
		return Record{}, err
	}
	return recordFromState(value), nil
}

func (s *Store) AddCandidate(alias, root, baseRoot, source string) (Record, error) {
	alias = normalizeAlias(alias)
	if alias == "" {
		return Record{}, fmt.Errorf("candidate alias is empty")
	}
	canonicalRoot, err := canonicalCID(root)
	if err != nil {
		return Record{}, fmt.Errorf("invalid candidate root: %w", err)
	}
	canonicalBaseRoot := ""
	if strings.TrimSpace(baseRoot) != "" {
		canonicalBaseRoot, err = canonicalCID(baseRoot)
		if err != nil {
			return Record{}, fmt.Errorf("invalid candidate base root: %w", err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, migrated, err := s.lockAndReload()
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = unlock() }()
	if err := s.prepareMigrationLocked(migrated); err != nil {
		return Record{}, err
	}
	value := s.state.Roots[alias]
	value.Alias = alias
	if value.Accepted == nil {
		if canonicalBaseRoot != "" {
			return Record{}, fmt.Errorf("%w: bootstrap candidate has unexpected base %s", ErrStaleCandidate, canonicalBaseRoot)
		}
	} else if canonicalBaseRoot != value.Accepted.Root {
		return Record{}, fmt.Errorf("%w: candidate base %q, accepted root %s", ErrStaleCandidate, canonicalBaseRoot, value.Accepted.Root)
	}
	if value.Accepted != nil && canonicalRoot == value.Accepted.Root {
		return recordFromState(value), nil
	}
	value.Candidates = removeCandidate(value.Candidates, canonicalRoot)
	value.Candidates = append(value.Candidates, CandidateRoot{
		Root: canonicalRoot, BaseRoot: canonicalBaseRoot, Source: source, ObservedAt: time.Now().UTC(),
	})
	s.state.Roots[alias] = value
	if err := s.writeLocked(); err != nil {
		return Record{}, err
	}
	return recordFromState(value), nil
}

func (s *Store) ObserveHead(alias string, observation ObservedHead) (Record, error) {
	alias = normalizeAlias(alias)
	if alias == "" {
		return Record{}, fmt.Errorf("observed-head alias is empty")
	}
	observation, err := normalizeObservation(observation)
	if err != nil {
		return Record{}, err
	}
	observation.ObservedAt = time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, migrated, err := s.lockAndReload()
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = unlock() }()
	if err := s.prepareMigrationLocked(migrated); err != nil {
		return Record{}, err
	}
	value := s.state.Roots[alias]
	value.Alias = alias
	value.ObservedHeads, err = upsertObservation(value.ObservedHeads, observation)
	if err != nil {
		return Record{}, err
	}
	s.state.Roots[alias] = value
	if err := s.writeLocked(); err != nil {
		return Record{}, err
	}
	return recordFromState(value), nil
}

func (s *Store) AcceptCandidate(alias, root, source string) (Record, error) {
	alias = normalizeAlias(alias)
	canonicalRoot, err := canonicalCID(root)
	if err != nil {
		return Record{}, fmt.Errorf("invalid candidate root: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, migrated, err := s.lockAndReload()
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = unlock() }()
	if err := s.prepareMigrationLocked(migrated); err != nil {
		return Record{}, err
	}
	value, ok := s.state.Roots[alias]
	if !ok {
		return Record{}, ErrNotFound
	}
	var candidate CandidateRoot
	found := false
	for _, item := range value.Candidates {
		if item.Root == canonicalRoot {
			candidate = item
			found = true
			break
		}
	}
	if !found {
		return Record{}, ErrCandidateNotFound
	}
	if value.Accepted == nil {
		if candidate.BaseRoot != "" {
			return Record{}, fmt.Errorf("%w: bootstrap candidate base %q", ErrStaleCandidate, candidate.BaseRoot)
		}
	} else if candidate.BaseRoot == "" || candidate.BaseRoot != value.Accepted.Root {
		return Record{}, fmt.Errorf("%w: candidate base %q, accepted root %s", ErrStaleCandidate, candidate.BaseRoot, value.Accepted.Root)
	}
	acceptRoot(&value, canonicalRoot, source, time.Now().UTC())
	s.state.Roots[alias] = value
	if err := s.writeLocked(); err != nil {
		return Record{}, err
	}
	return recordFromState(value), nil
}

// AcceptObserved explicitly promotes a previously recorded remote observation.
// It never accepts an arbitrary unobserved root and remains distinct from
// accepting a locally computed candidate.
func (s *Store) AcceptObserved(alias, root, profile, gateway, source string) (Record, error) {
	alias = normalizeAlias(alias)
	canonicalRoot, err := canonicalCID(root)
	if err != nil {
		return Record{}, fmt.Errorf("invalid observed root: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, migrated, err := s.lockAndReload()
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = unlock() }()
	if err := s.prepareMigrationLocked(migrated); err != nil {
		return Record{}, err
	}
	value, ok := s.state.Roots[alias]
	if !ok {
		return Record{}, ErrNotFound
	}
	found := false
	for _, observation := range value.ObservedHeads {
		if observation.Root == canonicalRoot {
			found = true
			break
		}
	}
	if !found {
		return Record{}, ErrObservationNotFound
	}
	value.Profile = profile
	value.Gateway = gateway
	acceptRoot(&value, canonicalRoot, source, time.Now().UTC())
	s.state.Roots[alias] = value
	if err := s.writeLocked(); err != nil {
		return Record{}, err
	}
	return recordFromState(value), nil
}

func (s *Store) reloadWithFileLock() error {
	unlock, migrated, err := s.lockAndReload()
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	if migrated {
		return s.finishMigrationLocked()
	}
	return nil
}

func (s *Store) withLockedState(operation func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, migrated, err := s.lockAndReload()
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	if err := operation(); err != nil {
		return err
	}
	if migrated {
		return s.finishMigrationLocked()
	}
	return nil
}

func (s *Store) lockAndReload() (func() error, bool, error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return nil, false, fmt.Errorf("create trust-store directory: %w", err)
	}
	unlock, err := acquireTrustStoreLock(s.path + ".lock")
	if err != nil {
		return nil, false, fmt.Errorf("lock trust store: %w", err)
	}
	migrated, err := s.reloadLocked()
	if err != nil {
		_ = unlock()
		return nil, false, err
	}
	return unlock, migrated, nil
}

func (s *Store) reloadLocked() (bool, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.state = emptyState()
		s.legacyData = nil
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read trust store: %w", err)
	}
	if err := s.files.secure(s.path); err != nil {
		return false, fmt.Errorf("protect trust store: %w", err)
	}
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return false, fmt.Errorf("decode trust store: %w", err)
	}
	var next state
	migrated := false
	switch header.Version {
	case 1:
		var legacy legacyState
		if err := json.Unmarshal(data, &legacy); err != nil {
			return false, fmt.Errorf("decode legacy trust store: %w", err)
		}
		next, err = migrateLegacyState(legacy)
		if err != nil {
			return false, err
		}
		migrated = true
		s.legacyData = append(s.legacyData[:0], data...)
	case trustStoreVersion:
		if err := json.Unmarshal(data, &next); err != nil {
			return false, fmt.Errorf("decode trust store: %w", err)
		}
		s.legacyData = nil
	default:
		return false, fmt.Errorf("unsupported trust-store version %d", header.Version)
	}
	if next.Roots == nil {
		next.Roots = map[string]RootState{}
	}
	for alias, value := range next.Roots {
		value, err = normalizeRootState(alias, value)
		if err != nil {
			return false, err
		}
		next.Roots[alias] = value
	}
	next.Version = trustStoreVersion
	s.state = next
	return migrated, nil
}

func (s *Store) writeLocked() error {
	s.state.Version = trustStoreVersion
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return s.writeAtomicLocked(s.path, ".roots-*.json", data)
}

func (s *Store) prepareMigrationLocked(migrated bool) error {
	if !migrated {
		return nil
	}
	return s.preserveLegacyLocked()
}

func (s *Store) finishMigrationLocked() error {
	if err := s.preserveLegacyLocked(); err != nil {
		return err
	}
	return s.writeLocked()
}

func (s *Store) preserveLegacyLocked() error {
	if len(s.legacyData) == 0 {
		return fmt.Errorf("migrate trust store: legacy source bytes are unavailable")
	}
	if err := s.writeAtomicLocked(LegacyRecoveryPath(s.path), ".roots-v1-recovery-*.json", s.legacyData); err != nil {
		return fmt.Errorf("preserve schema-v1 trust-store recovery artifact: %w", err)
	}
	return nil
}

func (s *Store) writeAtomicLocked(target, pattern string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return fmt.Errorf("create trust-store directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), pattern)
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := s.files.secure(tmpName); err != nil {
		_ = tmp.Close()
		return err
	}
	written, err := s.files.write(tmp, data)
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
	if err := s.files.rename(tmpName, target); err != nil {
		return fmt.Errorf("replace trust-store file: %w", err)
	}
	if err := s.files.syncParent(target); err != nil {
		return fmt.Errorf("sync trust-store directory: %w", err)
	}
	if err := s.files.secure(target); err != nil {
		return fmt.Errorf("protect trust store: %w", err)
	}
	return nil
}

func emptyState() state {
	return state{Version: trustStoreVersion, Roots: map[string]RootState{}}
}

func migrateLegacyState(legacy legacyState) (state, error) {
	next := emptyState()
	for alias, record := range legacy.Roots {
		if strings.TrimSpace(record.AcceptedRoot) == "" {
			return state{}, fmt.Errorf("trusted-root alias %q has an empty legacy accepted root", alias)
		}
		value := RootState{
			Alias: record.Alias, Profile: record.Profile, Gateway: record.Gateway,
			Candidates: append([]CandidateRoot(nil), record.Candidates...),
		}
		value.Accepted = &AcceptedRootState{
			Root: record.AcceptedRoot, PreviousRoot: record.PreviousRoot,
			Source: record.Source, AcceptedAt: record.AcceptedAt,
		}
		next.Roots[alias] = value
	}
	return next, nil
}

func normalizeRootState(alias string, value RootState) (RootState, error) {
	canonicalAlias := normalizeAlias(alias)
	if canonicalAlias == "" || canonicalAlias != alias {
		return RootState{}, fmt.Errorf("trust store contains invalid alias key %q", alias)
	}
	if value.Alias == "" {
		value.Alias = alias
	}
	if normalizeAlias(value.Alias) != alias {
		return RootState{}, fmt.Errorf("trust-store alias %q contains mismatched identity %q", alias, value.Alias)
	}
	value.Alias = alias
	if value.Accepted != nil {
		accepted := *value.Accepted
		var err error
		if accepted.Root, err = canonicalCID(accepted.Root); err != nil {
			return RootState{}, fmt.Errorf("trusted-root alias %q has invalid accepted root: %w", alias, err)
		}
		if accepted.PreviousRoot, err = canonicalOptionalCID(accepted.PreviousRoot); err != nil {
			return RootState{}, fmt.Errorf("trusted-root alias %q has invalid previous root: %w", alias, err)
		}
		if accepted.PreviousRoot == accepted.Root {
			accepted.PreviousRoot = ""
		}
		value.Accepted = &accepted
	}
	candidates := make([]CandidateRoot, 0, len(value.Candidates))
	for i, candidate := range value.Candidates {
		var err error
		candidate.Root, err = canonicalCID(candidate.Root)
		if err != nil {
			return RootState{}, fmt.Errorf("trusted-root alias %q candidate %d has invalid root: %w", alias, i, err)
		}
		candidate.BaseRoot, err = canonicalOptionalCID(candidate.BaseRoot)
		if err != nil {
			return RootState{}, fmt.Errorf("trusted-root alias %q candidate %d has invalid base root: %w", alias, i, err)
		}
		if value.Accepted == nil && candidate.BaseRoot != "" {
			return RootState{}, fmt.Errorf("trusted-root alias %q bootstrap candidate has a base root", alias)
		}
		if value.Accepted != nil && candidate.BaseRoot == "" {
			return RootState{}, fmt.Errorf("trusted-root alias %q candidate has no accepted base root", alias)
		}
		if value.Accepted != nil && candidate.Root == value.Accepted.Root {
			continue
		}
		candidates = append(removeCandidate(candidates, candidate.Root), candidate)
	}
	value.Candidates = candidates
	observations := make([]ObservedHead, 0, len(value.ObservedHeads))
	for i, observation := range value.ObservedHeads {
		if observation.ObservedAt.IsZero() {
			return RootState{}, fmt.Errorf("trusted-root alias %q observation %d has no observation time", alias, i)
		}
		normalized, err := normalizeObservation(observation)
		if err != nil {
			return RootState{}, fmt.Errorf("trusted-root alias %q observation %d is invalid: %w", alias, i, err)
		}
		normalized.ObservedAt = observation.ObservedAt
		observations, err = upsertObservation(observations, normalized)
		if err != nil {
			return RootState{}, fmt.Errorf("trusted-root alias %q observation %d is invalid: %w", alias, i, err)
		}
	}
	value.ObservedHeads = observations
	if value.Accepted == nil && len(value.Candidates) == 0 && len(value.ObservedHeads) == 0 {
		return RootState{}, fmt.Errorf("trusted-root alias %q has no accepted root, candidate, or observation", alias)
	}
	return value, nil
}

func normalizeObservation(value ObservedHead) (ObservedHead, error) {
	value.Source = strings.TrimSpace(value.Source)
	value.DatasetID = strings.TrimSpace(value.DatasetID)
	value.Branch = strings.TrimSpace(value.Branch)
	value.CommitID = strings.TrimSpace(value.CommitID)
	value.Root = strings.TrimSpace(value.Root)
	if value.Source == "" || value.DatasetID == "" || value.Branch == "" {
		return ObservedHead{}, fmt.Errorf("observed head source, dataset, and branch are required")
	}
	if value.CommitID == "" {
		if value.Root != "" || value.Revision != 0 {
			return ObservedHead{}, fmt.Errorf("empty observed head has a partial commit tuple")
		}
		return value, nil
	}
	if value.Root == "" || value.Revision == 0 {
		return ObservedHead{}, fmt.Errorf("observed head has an incomplete commit tuple")
	}
	root, err := canonicalCID(value.Root)
	if err != nil {
		return ObservedHead{}, fmt.Errorf("observed head has invalid root: %w", err)
	}
	value.Root = root
	return value, nil
}

func upsertObservation(values []ObservedHead, incoming ObservedHead) ([]ObservedHead, error) {
	key := observationKey(incoming)
	out := append([]ObservedHead(nil), values...)
	for i, existing := range out {
		if observationKey(existing) != key {
			continue
		}
		if incoming.Revision < existing.Revision {
			return nil, fmt.Errorf("%w: observed revision %d after %d", ErrStaleObservation, incoming.Revision, existing.Revision)
		}
		if incoming.Revision == existing.Revision && (incoming.CommitID != existing.CommitID || incoming.Root != existing.Root) {
			return nil, fmt.Errorf("%w: revision %d", ErrConflictingObservation, incoming.Revision)
		}
		out[i] = incoming
		sortObservations(out)
		return out, nil
	}
	out = append(out, incoming)
	sortObservations(out)
	return out, nil
}

func observationKey(value ObservedHead) string {
	return value.Source + "\x00" + value.DatasetID + "\x00" + value.Branch
}

func sortObservations(values []ObservedHead) {
	sort.Slice(values, func(i, j int) bool { return observationKey(values[i]) < observationKey(values[j]) })
}

func acceptRoot(value *RootState, root, source string, acceptedAt time.Time) {
	previous := ""
	bootstrap := value.Accepted == nil
	if value.Accepted != nil && value.Accepted.Root != root {
		previous = value.Accepted.Root
	} else if value.Accepted != nil {
		previous = value.Accepted.PreviousRoot
	}
	value.Accepted = &AcceptedRootState{
		Root: root, PreviousRoot: previous, Source: source, AcceptedAt: acceptedAt,
	}
	if bootstrap {
		value.Candidates = nil
	} else {
		value.Candidates = removeCandidate(value.Candidates, root)
	}
}

func normalizeAlias(alias string) string { return strings.TrimSpace(alias) }

func canonicalCID(raw string) (string, error) {
	parsed, err := cid.Parse(raw)
	if err != nil {
		return "", err
	}
	return parsed.String(), nil
}

func canonicalOptionalCID(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	return canonicalCID(raw)
}

func removeCandidate(values []CandidateRoot, root string) []CandidateRoot {
	out := make([]CandidateRoot, 0, len(values))
	for _, value := range values {
		if value.Root != root {
			out = append(out, value)
		}
	}
	return out
}

func recordFromState(value RootState) Record {
	record := Record{
		Alias: value.Alias, Profile: value.Profile, Gateway: value.Gateway,
		Candidates: append([]CandidateRoot(nil), value.Candidates...),
	}
	if value.Accepted != nil {
		record.AcceptedRoot = value.Accepted.Root
		record.PreviousRoot = value.Accepted.PreviousRoot
		record.Source = value.Accepted.Source
		record.AcceptedAt = value.Accepted.AcceptedAt
	}
	return record
}

func cloneRootState(value RootState) RootState {
	if value.Accepted != nil {
		accepted := *value.Accepted
		value.Accepted = &accepted
	}
	value.Candidates = append([]CandidateRoot(nil), value.Candidates...)
	value.ObservedHeads = append([]ObservedHead(nil), value.ObservedHeads...)
	return value
}

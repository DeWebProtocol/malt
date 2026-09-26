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
	"github.com/dewebprotocol/malt-client/internal/filelock"
	"github.com/dewebprotocol/malt-client/internal/securefile"
	"github.com/dewebprotocol/malt-client/internal/strictjson"
	cid "github.com/ipfs/go-cid"
)

const trustStoreVersion = 2

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

type state struct {
	Version int                  `json:"version"`
	Roots   map[string]RootState `json:"roots"`
}

type storeFileOps struct {
	secure     func(string) error
	syncParent func(string) error
	rename     func(string, string) error
	write      func(*os.File, []byte) (int, error)
}

type Store struct {
	mu    sync.Mutex
	path  string
	state state
	files storeFileOps
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
	unlock, err := s.lockAndReload()
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
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

func (s *Store) Trust(alias, root, profile, gateway, source string) (RootState, error) {
	alias = normalizeAlias(alias)
	if alias == "" {
		return RootState{}, fmt.Errorf("trusted-root alias is empty")
	}
	canonicalRoot, err := canonicalCID(root)
	if err != nil {
		return RootState{}, fmt.Errorf("invalid trusted root: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockAndReload()
	if err != nil {
		return RootState{}, err
	}
	defer func() { _ = unlock() }()
	value := s.state.Roots[alias]
	value.Alias = alias
	value.Profile = profile
	value.Gateway = gateway
	acceptRoot(&value, canonicalRoot, source, time.Now().UTC())
	s.state.Roots[alias] = value
	if err := s.writeLocked(); err != nil {
		return RootState{}, err
	}
	return cloneRootState(value), nil
}

func (s *Store) AddCandidate(alias, root, baseRoot, source string) (RootState, error) {
	alias = normalizeAlias(alias)
	if alias == "" {
		return RootState{}, fmt.Errorf("candidate alias is empty")
	}
	canonicalRoot, err := canonicalCID(root)
	if err != nil {
		return RootState{}, fmt.Errorf("invalid candidate root: %w", err)
	}
	canonicalBaseRoot := ""
	if strings.TrimSpace(baseRoot) != "" {
		canonicalBaseRoot, err = canonicalCID(baseRoot)
		if err != nil {
			return RootState{}, fmt.Errorf("invalid candidate base root: %w", err)
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockAndReload()
	if err != nil {
		return RootState{}, err
	}
	defer func() { _ = unlock() }()
	value := s.state.Roots[alias]
	value.Alias = alias
	if value.Accepted == nil {
		if canonicalBaseRoot != "" {
			return RootState{}, fmt.Errorf("%w: bootstrap candidate has unexpected base %s", ErrStaleCandidate, canonicalBaseRoot)
		}
	} else if canonicalBaseRoot != value.Accepted.Root {
		return RootState{}, fmt.Errorf("%w: candidate base %q, accepted root %s", ErrStaleCandidate, canonicalBaseRoot, value.Accepted.Root)
	}
	if value.Accepted != nil && canonicalRoot == value.Accepted.Root {
		return cloneRootState(value), nil
	}
	value.Candidates = removeCandidate(value.Candidates, canonicalRoot)
	value.Candidates = append(value.Candidates, CandidateRoot{
		Root: canonicalRoot, BaseRoot: canonicalBaseRoot, Source: source, ObservedAt: time.Now().UTC(),
	})
	s.state.Roots[alias] = value
	if err := s.writeLocked(); err != nil {
		return RootState{}, err
	}
	return cloneRootState(value), nil
}

func (s *Store) ObserveHead(alias string, observation ObservedHead) (RootState, error) {
	alias = normalizeAlias(alias)
	if alias == "" {
		return RootState{}, fmt.Errorf("observed-head alias is empty")
	}
	observation, err := normalizeObservation(observation)
	if err != nil {
		return RootState{}, err
	}
	observation.ObservedAt = time.Now().UTC()
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockAndReload()
	if err != nil {
		return RootState{}, err
	}
	defer func() { _ = unlock() }()
	value := s.state.Roots[alias]
	value.Alias = alias
	value.ObservedHeads, err = upsertObservation(value.ObservedHeads, observation)
	if err != nil {
		return RootState{}, err
	}
	s.state.Roots[alias] = value
	if err := s.writeLocked(); err != nil {
		return RootState{}, err
	}
	return cloneRootState(value), nil
}

func (s *Store) AcceptCandidate(alias, root, source string) (RootState, error) {
	alias = normalizeAlias(alias)
	canonicalRoot, err := canonicalCID(root)
	if err != nil {
		return RootState{}, fmt.Errorf("invalid candidate root: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockAndReload()
	if err != nil {
		return RootState{}, err
	}
	defer func() { _ = unlock() }()
	value, ok := s.state.Roots[alias]
	if !ok {
		return RootState{}, ErrNotFound
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
		return RootState{}, ErrCandidateNotFound
	}
	if value.Accepted == nil {
		if candidate.BaseRoot != "" {
			return RootState{}, fmt.Errorf("%w: bootstrap candidate base %q", ErrStaleCandidate, candidate.BaseRoot)
		}
	} else if candidate.BaseRoot == "" || candidate.BaseRoot != value.Accepted.Root {
		return RootState{}, fmt.Errorf("%w: candidate base %q, accepted root %s", ErrStaleCandidate, candidate.BaseRoot, value.Accepted.Root)
	}
	acceptRoot(&value, canonicalRoot, source, time.Now().UTC())
	s.state.Roots[alias] = value
	if err := s.writeLocked(); err != nil {
		return RootState{}, err
	}
	return cloneRootState(value), nil
}

// AcceptObserved explicitly promotes a previously recorded remote observation.
// It never accepts an arbitrary unobserved root and remains distinct from
// accepting a locally computed candidate.
func (s *Store) AcceptObserved(alias, root, profile, gateway, source string) (RootState, error) {
	alias = normalizeAlias(alias)
	canonicalRoot, err := canonicalCID(root)
	if err != nil {
		return RootState{}, fmt.Errorf("invalid observed root: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockAndReload()
	if err != nil {
		return RootState{}, err
	}
	defer func() { _ = unlock() }()
	value, ok := s.state.Roots[alias]
	if !ok {
		return RootState{}, ErrNotFound
	}
	found := false
	for _, observation := range value.ObservedHeads {
		if observation.Root == canonicalRoot {
			found = true
			break
		}
	}
	if !found {
		return RootState{}, ErrObservationNotFound
	}
	value.Profile = profile
	value.Gateway = gateway
	acceptRoot(&value, canonicalRoot, source, time.Now().UTC())
	s.state.Roots[alias] = value
	if err := s.writeLocked(); err != nil {
		return RootState{}, err
	}
	return cloneRootState(value), nil
}

func (s *Store) reloadWithFileLock() error {
	unlock, err := s.lockAndReload()
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	return nil
}

func (s *Store) withLockedState(operation func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := s.lockAndReload()
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	if err := operation(); err != nil {
		return err
	}
	return nil
}

func (s *Store) lockAndReload() (func() error, error) {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return nil, fmt.Errorf("create trust-store directory: %w", err)
	}
	unlock, err := filelock.AcquireBlocking(s.path + ".lock")
	if err != nil {
		return nil, fmt.Errorf("lock trust store: %w", err)
	}
	if err := s.reloadLocked(); err != nil {
		_ = unlock()
		return nil, err
	}
	return unlock, nil
}

func (s *Store) reloadLocked() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.state = emptyState()
		return nil
	}
	if err != nil {
		return fmt.Errorf("read trust store: %w", err)
	}
	if err := s.files.secure(s.path); err != nil {
		return fmt.Errorf("protect trust store: %w", err)
	}
	var next state
	if err := strictjson.Decode(data, &next); err != nil {
		return fmt.Errorf("decode trust store: %w", err)
	}
	if next.Version != trustStoreVersion {
		return fmt.Errorf("unsupported trust-store version %d", next.Version)
	}
	if next.Roots == nil {
		next.Roots = map[string]RootState{}
	}
	for alias, value := range next.Roots {
		value, err = normalizeRootState(alias, value)
		if err != nil {
			return err
		}
		next.Roots[alias] = value
	}
	s.state = next
	return nil
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

func cloneRootState(value RootState) RootState {
	if value.Accepted != nil {
		accepted := *value.Accepted
		value.Accepted = &accepted
	}
	value.Candidates = append([]CandidateRoot(nil), value.Candidates...)
	value.ObservedHeads = append([]ObservedHead(nil), value.ObservedHeads...)
	return value
}

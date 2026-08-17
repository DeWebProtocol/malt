package mount

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dewebprotocol/malt-client/internal/durablefile"
	"github.com/dewebprotocol/malt-client/internal/filelock"
	"github.com/dewebprotocol/malt-client/internal/securefile"
	"github.com/dewebprotocol/malt-client/internal/strictjson"
)

const registryVersion = 1

var (
	ErrNotFound            = errors.New("mount binding not found")
	ErrInvalidSpec         = errors.New("invalid mount specification")
	ErrIdentityReuse       = errors.New("mount identity is already bound differently")
	ErrMountpointUse       = errors.New("mountpoint is already reserved")
	ErrPendingUnmount      = errors.New("mount has a pending unmount")
	ErrUnsupportedPlatform = errors.New("mount lifecycle is unsupported on this platform")
)

type CachePolicy string
type WritePolicy string
type ConflictPolicy string

const (
	CacheVerified        CachePolicy    = "verified"
	WriteReadOnly        WritePolicy    = "read_only"
	ConflictFailReadOnly ConflictPolicy = "fail_read_only"
)

// Spec is the durable identity and local policy for one mount. TrustAlias is
// resolved only by a local ViewSelector; no remote head is persisted here as
// accepted state.
type Spec struct {
	ID              string         `json:"id"`
	DatasetID       string         `json:"dataset_id"`
	Branch          string         `json:"branch"`
	Mountpoint      string         `json:"mountpoint"`
	TrustAlias      string         `json:"trust_alias"`
	CachePolicy     CachePolicy    `json:"cache_policy"`
	WritePolicy     WritePolicy    `json:"write_policy"`
	EncryptionEpoch uint32         `json:"encryption_epoch"`
	ConflictPolicy  ConflictPolicy `json:"conflict_policy"`
}

// Record survives daemon crashes. Desired=false is an unmount tombstone and
// must remain until a platform adapter confirms stale/live mount cleanup.
type Record struct {
	Spec      Spec      `json:"spec"`
	Desired   bool      `json:"desired"`
	UpdatedAt time.Time `json:"updated_at"`
}

type registryState struct {
	Version int               `json:"version"`
	Records map[string]Record `json:"records"`
}

type Store struct {
	mu           sync.Mutex
	path         string
	state        registryState
	now          func() time.Time
	leaseTimeout time.Duration
}

func OpenStore(path string) (*Store, error) {
	if !lifecyclePlatformSupported {
		return nil, fmt.Errorf("%w: %s lacks a process-released manager lease", ErrUnsupportedPlatform, runtime.GOOS)
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("mount registry path is empty")
	}
	path = filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	store := &Store{path: path, state: emptyRegistry(), now: time.Now, leaseTimeout: 10 * time.Second}
	if err := store.withState(false, func() error { return nil }); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) List() ([]Record, error) {
	var records []Record
	err := s.withState(false, func() error {
		records = make([]Record, 0, len(s.state.Records))
		for _, record := range s.state.Records {
			records = append(records, record)
		}
		sort.Slice(records, func(i, j int) bool { return records[i].Spec.ID < records[j].Spec.ID })
		return nil
	})
	return records, err
}

func (s *Store) Get(id string) (Record, error) {
	id = strings.TrimSpace(id)
	var record Record
	err := s.withState(false, func() error {
		var ok bool
		record, ok = s.state.Records[id]
		if !ok {
			return ErrNotFound
		}
		return nil
	})
	return record, err
}

// PutDesired atomically reserves the identity and mountpoint before platform
// mount I/O begins. An equal desired Spec is idempotent; a pending-unmount
// tombstone must be cleaned before the identity can be mounted again.
func (s *Store) PutDesired(spec Spec) (Record, error) {
	spec, err := normalizeSpec(spec)
	if err != nil {
		return Record{}, err
	}
	var result Record
	err = s.withState(true, func() error {
		if existing, ok := s.state.Records[spec.ID]; ok {
			if existing.Spec != spec {
				return ErrIdentityReuse
			}
			if !existing.Desired {
				return ErrPendingUnmount
			}
		}
		for id, existing := range s.state.Records {
			if id != spec.ID && mountpointKey(existing.Spec.Mountpoint) == mountpointKey(spec.Mountpoint) {
				return ErrMountpointUse
			}
		}
		result = Record{Spec: spec, Desired: true, UpdatedAt: s.now().UTC()}
		s.state.Records[spec.ID] = result
		return nil
	})
	return result, err
}

// MarkPendingUnmount persists the cleanup intent before platform unmount I/O.
func (s *Store) MarkPendingUnmount(id string) (Record, error) {
	id = strings.TrimSpace(id)
	var result Record
	err := s.withState(true, func() error {
		existing, ok := s.state.Records[id]
		if !ok {
			return ErrNotFound
		}
		existing.Desired = false
		existing.UpdatedAt = s.now().UTC()
		s.state.Records[id] = existing
		result = existing
		return nil
	})
	return result, err
}

// DeleteUnmounted removes only a confirmed unmount tombstone.
func (s *Store) DeleteUnmounted(id string) error {
	id = strings.TrimSpace(id)
	return s.withState(true, func() error {
		existing, ok := s.state.Records[id]
		if !ok {
			return ErrNotFound
		}
		if existing.Desired {
			return fmt.Errorf("refusing to delete desired mount %s", id)
		}
		delete(s.state.Records, id)
		return nil
	})
}

func (s *Store) withState(write bool, operation func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := filelock.Acquire(s.path+".lock", 10*time.Second)
	if err != nil {
		return fmt.Errorf("lock mount registry: %w", err)
	}
	defer func() { _ = unlock() }()
	if err := securefile.Secure(s.path + ".lock"); err != nil {
		return fmt.Errorf("protect mount registry lock: %w", err)
	}
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

// acquireManagerLease excludes a second daemon or Manager from performing
// platform lifecycle I/O against this registry. OpenStore limits this package
// to targets where the operating system releases the lock after process exit.
func (s *Store) acquireManagerLease() (func() error, error) {
	path := s.path + ".manager.lock"
	unlock, err := filelock.Acquire(path, s.leaseTimeout)
	if err != nil {
		return nil, err
	}
	if err := securefile.Secure(path); err != nil {
		_ = unlock()
		return nil, fmt.Errorf("protect mount manager lease: %w", err)
	}
	return unlock, nil
}

func (s *Store) reloadLocked() error {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.state = emptyRegistry()
		return nil
	}
	if err != nil {
		return err
	}
	if err := securefile.Secure(s.path); err != nil {
		return fmt.Errorf("protect mount registry: %w", err)
	}
	if err := strictjson.ValidateUnicode(data); err != nil {
		return fmt.Errorf("decode mount registry: %w", err)
	}
	type persistedRecord struct {
		Spec      Spec      `json:"spec"`
		Desired   *bool     `json:"desired"`
		UpdatedAt time.Time `json:"updated_at"`
	}
	type persistedState struct {
		Version int                        `json:"version"`
		Records map[string]persistedRecord `json:"records"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var persisted persistedState
	if err := decoder.Decode(&persisted); err != nil {
		return fmt.Errorf("decode mount registry: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("decode mount registry: expected one JSON object")
	}
	if persisted.Version != registryVersion || persisted.Records == nil {
		return fmt.Errorf("unsupported mount registry version %d", persisted.Version)
	}
	next := emptyRegistry()
	reserved := map[string]string{}
	for id, wire := range persisted.Records {
		if wire.Desired == nil {
			return fmt.Errorf("mount record %s is missing required desired state", id)
		}
		record := Record{Spec: wire.Spec, Desired: *wire.Desired, UpdatedAt: wire.UpdatedAt}
		normalized, err := normalizeSpec(record.Spec)
		if err != nil {
			return fmt.Errorf("mount record %s: %w", id, err)
		}
		if normalized != record.Spec || normalized.ID != id || record.UpdatedAt.IsZero() {
			return fmt.Errorf("mount record %s has invalid persisted identity or timestamp", id)
		}
		key := mountpointKey(normalized.Mountpoint)
		if previous, ok := reserved[key]; ok {
			return fmt.Errorf("mount records %s and %s reserve the same mountpoint", previous, id)
		}
		reserved[key] = id
		record.Spec = normalized
		next.Records[id] = record
	}
	s.state = next
	return nil
}

func (s *Store) writeLocked() error {
	s.state.Version = registryVersion
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".mounts-*.json")
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
	if err := replaceFile(name, s.path); err != nil {
		return err
	}
	if err := durablefile.SyncParent(s.path); err != nil {
		return err
	}
	return securefile.Secure(s.path)
}

func normalizeSpec(spec Spec) (_ Spec, err error) {
	defer func() {
		if err != nil && !errors.Is(err, ErrInvalidSpec) {
			err = fmt.Errorf("%w: %v", ErrInvalidSpec, err)
		}
	}()
	spec.ID = strings.TrimSpace(spec.ID)
	spec.DatasetID = strings.TrimSpace(spec.DatasetID)
	spec.Branch = strings.TrimSpace(spec.Branch)
	spec.Mountpoint = strings.TrimSpace(spec.Mountpoint)
	spec.TrustAlias = strings.TrimSpace(spec.TrustAlias)
	for name, value := range map[string]string{
		"id": spec.ID, "dataset": spec.DatasetID, "branch": spec.Branch,
		"mountpoint": spec.Mountpoint, "trust alias": spec.TrustAlias,
	} {
		if value == "" || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
			return Spec{}, fmt.Errorf("mount %s is empty or invalid UTF-8", name)
		}
	}
	if len(spec.ID) > 128 {
		return Spec{}, fmt.Errorf("mount id exceeds 128 bytes")
	}
	for _, character := range spec.ID {
		if (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') && character != '-' && character != '_' && character != '.' {
			return Spec{}, fmt.Errorf("mount id contains unsupported character %q", character)
		}
	}
	if !filepath.IsAbs(spec.Mountpoint) {
		return Spec{}, fmt.Errorf("mountpoint must be an absolute path")
	}
	spec.Mountpoint = filepath.Clean(spec.Mountpoint)
	if filepath.Dir(spec.Mountpoint) == spec.Mountpoint {
		return Spec{}, fmt.Errorf("refusing to use a filesystem root as mountpoint")
	}
	if spec.CachePolicy == "" {
		spec.CachePolicy = CacheVerified
	}
	if spec.WritePolicy == "" {
		spec.WritePolicy = WriteReadOnly
	}
	if spec.ConflictPolicy == "" {
		spec.ConflictPolicy = ConflictFailReadOnly
	}
	if spec.CachePolicy != CacheVerified || spec.WritePolicy != WriteReadOnly || spec.ConflictPolicy != ConflictFailReadOnly {
		return Spec{}, fmt.Errorf("mount policy is unsupported by the read-only runtime")
	}
	return spec, nil
}

func mountpointKey(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		return strings.ToLower(path)
	}
	return path
}

func emptyRegistry() registryState {
	return registryState{Version: registryVersion, Records: map[string]Record{}}
}

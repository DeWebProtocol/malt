// Package keyring persists client-owned backup master keys. Bucket content
// keys are derived in memory, so the client stores one key per epoch rather
// than one key per Bucket.
package keyring

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dewebprotocol/malt-client/internal/durablefile"
	"github.com/dewebprotocol/malt-client/internal/filelock"
	"github.com/dewebprotocol/malt-client/internal/securefile"
)

const (
	version = 1
	keySize = 32
)

var ErrNotInitialized = errors.New("backup keyring is not initialized")

type file struct {
	Version     int               `json:"version"`
	ActiveEpoch uint32            `json:"active_epoch"`
	Epochs      map[string]string `json:"epochs"`
}

type Keyring struct {
	path string
	data file
}

// Create initializes a keyring without overwriting an existing one.
func Create(path string) (*Keyring, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("backup keyring path is empty")
	}
	unlock, err := filelock.Acquire(path+".lock", 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("lock backup keyring: %w", err)
	}
	defer func() { _ = unlock() }()
	if _, err := os.Stat(path); err == nil {
		return Open(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate backup master key: %w", err)
	}
	k := &Keyring{
		path: path,
		data: file{
			Version:     version,
			ActiveEpoch: 1,
			Epochs:      map[string]string{"1": base64.RawStdEncoding.EncodeToString(key)},
		},
	}
	if err := k.write(); err != nil {
		return nil, err
	}
	return k, nil
}

func Open(path string) (*Keyring, error) {
	path = strings.TrimSpace(path)
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%w: run `malt backup key-init` for a new vault or restore your existing backup keyring", ErrNotInitialized)
	}
	if err != nil {
		return nil, fmt.Errorf("read backup keyring: %w", err)
	}
	if err := securefile.Secure(path); err != nil {
		return nil, fmt.Errorf("secure backup keyring permissions: %w", err)
	}
	var value file
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("decode backup keyring: %w", err)
	}
	if value.Version != version || value.ActiveEpoch == 0 || len(value.Epochs) == 0 {
		return nil, fmt.Errorf("unsupported or invalid backup keyring")
	}
	for rawEpoch, encoded := range value.Epochs {
		epoch, err := strconv.ParseUint(rawEpoch, 10, 32)
		if err != nil || epoch == 0 {
			return nil, fmt.Errorf("invalid backup key epoch %q", rawEpoch)
		}
		key, err := base64.RawStdEncoding.DecodeString(encoded)
		if err != nil || len(key) != keySize {
			return nil, fmt.Errorf("invalid backup key material for epoch %s", rawEpoch)
		}
	}
	if _, ok := value.Epochs[strconv.FormatUint(uint64(value.ActiveEpoch), 10)]; !ok {
		return nil, fmt.Errorf("active backup key epoch is missing")
	}
	return &Keyring{path: path, data: value}, nil
}

func (k *Keyring) ActiveEpoch() uint32 { return k.data.ActiveEpoch }

// Rotate creates a new active master key while retaining old epochs for
// restore. Existing remote archives are not re-encrypted.
func (k *Keyring) Rotate() (uint32, error) {
	if k == nil || strings.TrimSpace(k.path) == "" {
		return 0, fmt.Errorf("backup keyring is nil")
	}
	unlock, err := filelock.Acquire(k.path+".lock", 10*time.Second)
	if err != nil {
		return 0, fmt.Errorf("lock backup keyring: %w", err)
	}
	defer func() { _ = unlock() }()
	current, err := Open(k.path)
	if err != nil {
		return 0, err
	}
	k.data = current.data
	if k.data.ActiveEpoch == ^uint32(0) {
		return 0, fmt.Errorf("backup key epoch cannot be advanced")
	}
	next := k.data.ActiveEpoch + 1
	key := make([]byte, keySize)
	if _, err := rand.Read(key); err != nil {
		return 0, fmt.Errorf("generate backup master key: %w", err)
	}
	k.data.Epochs[strconv.FormatUint(uint64(next), 10)] = base64.RawStdEncoding.EncodeToString(key)
	k.data.ActiveEpoch = next
	if err := k.write(); err != nil {
		return 0, err
	}
	return next, nil
}

// BucketKey derives a domain-separated per-Bucket key for one epoch. The
// derived key is not persisted.
func (k *Keyring) BucketKey(epoch uint32, bucketID string) ([keySize]byte, error) {
	var out [keySize]byte
	bucketID = strings.TrimSpace(bucketID)
	if bucketID == "" {
		return out, fmt.Errorf("backup Bucket ID is empty")
	}
	encoded, ok := k.data.Epochs[strconv.FormatUint(uint64(epoch), 10)]
	if !ok {
		return out, fmt.Errorf("backup key epoch %d is unavailable", epoch)
	}
	master, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil || len(master) != keySize {
		return out, fmt.Errorf("backup key epoch %d is invalid", epoch)
	}
	mac := hmac.New(sha256.New, master)
	_, _ = mac.Write([]byte("malt-client/backup/bucket-key/v1\x00"))
	_, _ = mac.Write([]byte(bucketID))
	copy(out[:], mac.Sum(nil))
	return out, nil
}

func (k *Keyring) write() error {
	if err := os.MkdirAll(filepath.Dir(k.path), 0o700); err != nil {
		return fmt.Errorf("create backup keyring directory: %w", err)
	}
	data, err := json.MarshalIndent(k.data, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(k.path), ".backup-keys-*.json")
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
	if err := os.Rename(name, k.path); err != nil {
		return err
	}
	if err := durablefile.SyncParent(k.path); err != nil {
		return fmt.Errorf("sync backup keyring directory: %w", err)
	}
	return securefile.Secure(k.path)
}

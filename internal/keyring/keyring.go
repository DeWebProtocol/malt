// Package keyring persists runtime-owned backup master keys. Bucket content
// keys are derived in memory, so the runtime stores one key per epoch rather
// than one key per Bucket.
package keyring

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dewebprotocol/malt-client/internal/durablefile"
	"github.com/dewebprotocol/malt-client/internal/filelock"
	"github.com/dewebprotocol/malt-client/internal/securefile"
	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/scrypt"
)

const (
	version           = 1
	keySize           = 32
	recoveryVersion   = 1
	recoveryScryptN   = 1 << 15
	recoveryScryptR   = 8
	recoveryScryptP   = 1
	recoveryKDF       = "scrypt"
	recoveryCipher    = "xchacha20poly1305"
	recoveryAAD       = "malt-client/keyring-recovery/v1"
	minimumPassphrase = 12
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

type recoveryEnvelope struct {
	Version    int    `json:"version"`
	KDF        string `json:"kdf"`
	ScryptN    int    `json:"scrypt_n"`
	ScryptR    int    `json:"scrypt_r"`
	ScryptP    int    `json:"scrypt_p"`
	Salt       string `json:"salt"`
	Cipher     string `json:"cipher"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
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
	if err := validateFile(value); err != nil {
		return nil, err
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

// ExportRecovery writes a passphrase-encrypted copy of every key epoch. The
// bundle intentionally grants restore access to all Buckets owned by this
// single-user keyring and therefore must be stored separately from the
// encrypted remote data.
func (k *Keyring) ExportRecovery(path string, passphrase []byte) error {
	if k == nil {
		return fmt.Errorf("backup keyring is nil")
	}
	if err := validateRecoveryPassphrase(passphrase); err != nil {
		return err
	}
	unlock, err := filelock.Acquire(k.path+".lock", 10*time.Second)
	if err != nil {
		return fmt.Errorf("lock backup keyring: %w", err)
	}
	current, readErr := Open(k.path)
	unlockErr := unlock()
	if readErr != nil {
		return readErr
	}
	if unlockErr != nil {
		return unlockErr
	}
	k.data = current.data
	plaintext, err := json.Marshal(k.data)
	if err != nil {
		return err
	}
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return err
	}
	key, err := scrypt.Key(passphrase, salt, recoveryScryptN, recoveryScryptR, recoveryScryptP, chacha20poly1305.KeySize)
	if err != nil {
		return err
	}
	defer clearBytes(key)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	ciphertext := aead.Seal(nil, nonce, plaintext, []byte(recoveryAAD))
	envelope := recoveryEnvelope{
		Version: recoveryVersion, KDF: recoveryKDF,
		ScryptN: recoveryScryptN, ScryptR: recoveryScryptR, ScryptP: recoveryScryptP,
		Salt: base64.RawStdEncoding.EncodeToString(salt), Cipher: recoveryCipher,
		Nonce:      base64.RawStdEncoding.EncodeToString(nonce),
		Ciphertext: base64.RawStdEncoding.EncodeToString(ciphertext),
	}
	return writeRecoveryEnvelope(path, envelope)
}

// ImportRecovery restores a missing keyring or merges missing epochs into an
// existing keyring. Overlapping epochs must contain byte-identical key
// material; conflicts fail without changing the local keyring.
func ImportRecovery(bundlePath, keyringPath string, passphrase []byte) (*Keyring, error) {
	if err := validateRecoveryPassphrase(passphrase); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(strings.TrimSpace(bundlePath))
	if err != nil {
		return nil, fmt.Errorf("read backup recovery bundle: %w", err)
	}
	var envelope recoveryEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("decode backup recovery bundle: %w", err)
	}
	if envelope.Version != recoveryVersion || envelope.KDF != recoveryKDF ||
		envelope.ScryptN != recoveryScryptN || envelope.ScryptR != recoveryScryptR ||
		envelope.ScryptP != recoveryScryptP || envelope.Cipher != recoveryCipher {
		return nil, fmt.Errorf("unsupported backup recovery bundle")
	}
	salt, err := base64.RawStdEncoding.DecodeString(envelope.Salt)
	if err != nil || len(salt) != 16 {
		return nil, fmt.Errorf("backup recovery bundle salt is invalid")
	}
	key, err := scrypt.Key(passphrase, salt, envelope.ScryptN, envelope.ScryptR, envelope.ScryptP, chacha20poly1305.KeySize)
	if err != nil {
		return nil, err
	}
	defer clearBytes(key)
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		return nil, err
	}
	nonce, ciphertext, err := decodeRecoveryCiphertext(envelope)
	if err != nil {
		return nil, err
	}
	plaintext, err := aead.Open(nil, nonce, ciphertext, []byte(recoveryAAD))
	if err != nil {
		return nil, fmt.Errorf("backup recovery passphrase or bundle is invalid")
	}
	var value file
	decoder := json.NewDecoder(bytes.NewReader(plaintext))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("decode recovered backup keyring: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("recovered backup keyring contains trailing data")
	}
	if err := validateFile(value); err != nil {
		return nil, err
	}
	keyringPath = strings.TrimSpace(keyringPath)
	if keyringPath == "" {
		return nil, fmt.Errorf("backup keyring path is empty")
	}
	unlock, err := filelock.Acquire(keyringPath+".lock", 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("lock backup keyring: %w", err)
	}
	defer func() { _ = unlock() }()
	if _, err := os.Lstat(keyringPath); errors.Is(err, os.ErrNotExist) {
		result := &Keyring{path: keyringPath, data: value}
		if err := result.write(); err != nil {
			return nil, err
		}
		return result, nil
	} else if err != nil {
		return nil, err
	}

	current, err := Open(keyringPath)
	if err != nil {
		return nil, err
	}
	merged := file{
		Version:     current.data.Version,
		ActiveEpoch: current.data.ActiveEpoch,
		Epochs:      make(map[string]string, len(current.data.Epochs)+len(value.Epochs)),
	}
	for epoch, encoded := range current.data.Epochs {
		merged.Epochs[epoch] = encoded
	}
	for epoch, recoveredEncoded := range value.Epochs {
		currentEncoded, exists := merged.Epochs[epoch]
		if !exists {
			merged.Epochs[epoch] = recoveredEncoded
			continue
		}
		currentKey, currentErr := base64.RawStdEncoding.DecodeString(currentEncoded)
		recoveredKey, recoveredErr := base64.RawStdEncoding.DecodeString(recoveredEncoded)
		if currentErr != nil || recoveredErr != nil || len(currentKey) != keySize ||
			len(recoveredKey) != keySize || subtle.ConstantTimeCompare(currentKey, recoveredKey) != 1 {
			return nil, fmt.Errorf("backup key epoch %s conflicts with the existing keyring", epoch)
		}
	}
	if value.ActiveEpoch > merged.ActiveEpoch {
		merged.ActiveEpoch = value.ActiveEpoch
	}
	if err := validateFile(merged); err != nil {
		return nil, err
	}
	result := &Keyring{path: keyringPath, data: merged}
	if err := result.write(); err != nil {
		return nil, err
	}
	return result, nil
}

func decodeRecoveryCiphertext(envelope recoveryEnvelope) ([]byte, []byte, error) {
	nonce, err := base64.RawStdEncoding.DecodeString(envelope.Nonce)
	if err != nil || len(nonce) != chacha20poly1305.NonceSizeX {
		return nil, nil, fmt.Errorf("backup recovery bundle nonce is invalid")
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(envelope.Ciphertext)
	if err != nil || len(ciphertext) < chacha20poly1305.Overhead {
		return nil, nil, fmt.Errorf("backup recovery bundle ciphertext is invalid")
	}
	return nonce, ciphertext, nil
}

func validateRecoveryPassphrase(passphrase []byte) error {
	if len(passphrase) < minimumPassphrase {
		return fmt.Errorf("backup recovery passphrase must contain at least %d bytes", minimumPassphrase)
	}
	return nil
}

func clearBytes(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

func validateFile(value file) error {
	if value.Version != version || value.ActiveEpoch == 0 || len(value.Epochs) == 0 {
		return fmt.Errorf("unsupported or invalid backup keyring")
	}
	for rawEpoch, encoded := range value.Epochs {
		epoch, err := strconv.ParseUint(rawEpoch, 10, 32)
		if err != nil || epoch == 0 {
			return fmt.Errorf("invalid backup key epoch %q", rawEpoch)
		}
		key, err := base64.RawStdEncoding.DecodeString(encoded)
		if err != nil || len(key) != keySize {
			return fmt.Errorf("invalid backup key material for epoch %s", rawEpoch)
		}
	}
	if _, ok := value.Epochs[strconv.FormatUint(uint64(value.ActiveEpoch), 10)]; !ok {
		return fmt.Errorf("active backup key epoch is missing")
	}
	return nil
}

func writeRecoveryEnvelope(path string, envelope recoveryEnvelope) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("backup recovery bundle path is empty")
	}
	unlock, err := filelock.Acquire(path+".lock", 10*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("backup recovery bundle already exists: %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(envelope, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".malt-recovery-*.json")
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
	if err := os.Rename(name, path); err != nil {
		return err
	}
	if err := durablefile.SyncParent(path); err != nil {
		return err
	}
	return securefile.Secure(path)
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

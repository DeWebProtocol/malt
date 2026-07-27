// Package deviceauth owns the client-side credential issued through browser
// authorization. The file provider is the portable fallback behind the
// future OS Keychain/TPM provider boundary.
package deviceauth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
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
	credentialVersion         = 2
	deviceAuthorizationScheme = "MaltDevice "
	deviceTimestampHeader     = "X-Malt-Device-Timestamp"
	deviceCounterHeader       = "X-Malt-Device-Counter"
	deviceDigestHeader        = "X-Malt-Device-Content-SHA256"
	deviceSignatureHeader     = "X-Malt-Device-Signature"
	// CAS batches permit 64 MiB of decoded blocks; base64 plus JSON framing can
	// expand the signed HTTP body to just under the Gateway's 96 MiB contract.
	maxSignedRequestBody = int64(96 << 20)
)

var ErrNotFound = errors.New("MALT device credential is not initialized")

type Credential struct {
	Version      int       `json:"version"`
	Gateway      string    `json:"gateway"`
	Name         string    `json:"name"`
	CredentialID string    `json:"credential_id"`
	TenantID     string    `json:"tenant_id"`
	PrincipalID  string    `json:"principal_id"`
	PublicKey    string    `json:"public_key"`
	PrivateKey   string    `json:"private_key"`
	KeyProvider  string    `json:"key_provider"`
	Counter      uint64    `json:"counter"`
	CreatedAt    time.Time `json:"created_at"`
}

type Provider interface {
	Load() (Credential, error)
	Save(Credential) error
}

type FileProvider struct {
	Path string
	Now  func() time.Time
}

func (p FileProvider) Load() (Credential, error) {
	data, err := os.ReadFile(p.Path)
	if errors.Is(err, os.ErrNotExist) {
		return Credential{}, ErrNotFound
	}
	if err != nil {
		return Credential{}, err
	}
	if err := securefile.Secure(p.Path); err != nil {
		return Credential{}, fmt.Errorf("secure device credential: %w", err)
	}
	var value Credential
	if err := json.Unmarshal(data, &value); err != nil {
		return Credential{}, fmt.Errorf("decode device credential: %w", err)
	}
	if err := value.Validate(); err != nil {
		return Credential{}, err
	}
	return value, nil
}

func (p FileProvider) Save(value Credential) error {
	if strings.TrimSpace(p.Path) == "" {
		return fmt.Errorf("device credential path is empty")
	}
	unlock, err := filelock.Acquire(p.Path+".lock", 30*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	return p.saveUnlocked(value)
}

// saveUnlocked persists a credential while the caller holds Path+".lock".
// Keeping this separate prevents request counter reservation from deadlocking
// while ensuring login cannot overwrite or be overwritten by an in-flight
// daemon reservation.
func (p FileProvider) saveUnlocked(value Credential) error {
	if value.Version == 0 {
		value.Version = credentialVersion
	}
	if err := value.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p.Path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(p.Path), ".device-credential-*.json")
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
	if err := os.Rename(name, p.Path); err != nil {
		return err
	}
	if err := securefile.Secure(p.Path); err != nil {
		return err
	}
	return durablefile.SyncParent(p.Path)
}

// Authorize signs the complete request with the local device key. The
// monotonically increasing counter is durably reserved before the request can
// be sent, so concurrent CLI and daemon processes cannot reuse a proof.
func (p FileProvider) Authorize(request *http.Request) error {
	if request == nil || request.URL == nil {
		return fmt.Errorf("device authorization request is nil")
	}
	unlock, err := filelock.Acquire(p.Path+".lock", 30*time.Second)
	if err != nil {
		return err
	}
	defer unlock()
	value, err := p.Load()
	if err != nil {
		return err
	}
	if !sameGateway(value.Gateway, request.URL) {
		return fmt.Errorf("stored device credential belongs to %s", value.Gateway)
	}
	if value.Counter == ^uint64(0) {
		return fmt.Errorf("device authorization counter is exhausted")
	}
	value.Counter++
	if err := p.saveUnlocked(value); err != nil {
		return fmt.Errorf("reserve device authorization counter: %w", err)
	}
	digest, err := requestBodyDigest(request, maxSignedRequestBody)
	if err != nil {
		return err
	}
	privateKey, err := base64.RawURLEncoding.DecodeString(value.PrivateKey)
	if err != nil || len(privateKey) != ed25519.PrivateKeySize {
		return fmt.Errorf("device credential private key is invalid")
	}
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	counter := strconv.FormatUint(value.Counter, 10)
	timestamp := strconv.FormatInt(now.Unix(), 10)
	encodedDigest := base64.RawURLEncoding.EncodeToString(digest[:])
	message := deviceRequestMessage(
		value.CredentialID,
		counter,
		timestamp,
		request.Method,
		strings.ToLower(request.URL.Host),
		request.URL.RequestURI(),
		encodedDigest,
	)
	signature := ed25519.Sign(ed25519.PrivateKey(privateKey), message)
	request.Header.Set("Authorization", deviceAuthorizationScheme+value.CredentialID)
	request.Header.Set(deviceTimestampHeader, timestamp)
	request.Header.Set(deviceCounterHeader, counter)
	request.Header.Set(deviceDigestHeader, encodedDigest)
	request.Header.Set(deviceSignatureHeader, base64.RawURLEncoding.EncodeToString(signature))
	return nil
}

func (c Credential) Validate() error {
	if c.Version != credentialVersion || strings.TrimSpace(c.Gateway) == "" || strings.TrimSpace(c.Name) == "" ||
		strings.TrimSpace(c.CredentialID) == "" || strings.TrimSpace(c.TenantID) == "" ||
		strings.TrimSpace(c.PrincipalID) == "" || strings.TrimSpace(c.KeyProvider) == "" || c.CreatedAt.IsZero() {
		return fmt.Errorf("device credential is incomplete or unsupported")
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(c.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return fmt.Errorf("device credential public key is invalid")
	}
	privateKey, err := base64.RawURLEncoding.DecodeString(c.PrivateKey)
	if err != nil || len(privateKey) != ed25519.PrivateKeySize ||
		!ed25519.PublicKey(privateKey[32:]).Equal(ed25519.PublicKey(publicKey)) {
		return fmt.Errorf("device credential private key is invalid")
	}
	return nil
}

func requestBodyDigest(request *http.Request, maximum int64) ([sha256.Size]byte, error) {
	if request.Body == nil {
		return sha256.Sum256(nil), nil
	}
	var body io.ReadCloser
	var err error
	if request.GetBody != nil {
		body, err = request.GetBody()
	} else {
		var data []byte
		data, err = io.ReadAll(io.LimitReader(request.Body, maximum+1))
		if err == nil {
			if int64(len(data)) > maximum {
				err = fmt.Errorf("device-authenticated request body exceeds %d bytes", maximum)
			} else {
				request.Body = io.NopCloser(bytes.NewReader(data))
				request.GetBody = func() (io.ReadCloser, error) {
					return io.NopCloser(bytes.NewReader(data)), nil
				}
				body = io.NopCloser(bytes.NewReader(data))
			}
		}
	}
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	defer body.Close()
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(body, maximum+1))
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	if written > maximum {
		return [sha256.Size]byte{}, fmt.Errorf("device-authenticated request body exceeds %d bytes", maximum)
	}
	var result [sha256.Size]byte
	copy(result[:], hash.Sum(nil))
	return result, nil
}

func deviceRequestMessage(fields ...string) []byte {
	var result bytes.Buffer
	result.WriteString("malt-device-request-v1")
	result.WriteByte(0)
	for _, field := range fields {
		_ = binary.Write(&result, binary.BigEndian, uint32(len(field)))
		result.WriteString(field)
	}
	return result.Bytes()
}

func sameGateway(configured string, requestURL *url.URL) bool {
	base, err := url.Parse(strings.TrimRight(strings.TrimSpace(configured), "/"))
	if err != nil || requestURL == nil {
		return false
	}
	return strings.EqualFold(base.Scheme, requestURL.Scheme) && strings.EqualFold(base.Host, requestURL.Host)
}

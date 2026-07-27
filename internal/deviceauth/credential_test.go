package deviceauth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/dewebprotocol/malt-client/internal/filelock"
)

func TestFileProviderRoundTrip(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	provider := FileProvider{Path: filepath.Join(t.TempDir(), "device.json")}
	value := Credential{
		Version: 2, Gateway: "https://gateway.example", Name: "Laptop",
		CredentialID: "key_test", TenantID: "tenant", PrincipalID: "principal",
		PublicKey:   base64.RawURLEncoding.EncodeToString(publicKey),
		PrivateKey:  base64.RawURLEncoding.EncodeToString(privateKey),
		KeyProvider: "software-file", CreatedAt: time.Now().UTC(),
	}
	if err := provider.Save(value); err != nil {
		t.Fatal(err)
	}
	loaded, err := provider.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.KeyProvider != value.KeyProvider || loaded.PrivateKey != value.PrivateKey {
		t.Fatalf("loaded credential = %#v", loaded)
	}
}

func TestFileProviderAuthorizesCompleteRequestAndReservesCounter(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_900_000_000, 0).UTC()
	provider := FileProvider{Path: filepath.Join(t.TempDir(), "device.json"), Now: func() time.Time { return now }}
	if err := provider.Save(Credential{
		Version: 2, Gateway: "https://gateway.example", Name: "Laptop",
		CredentialID: "key_test", TenantID: "tenant", PrincipalID: "principal",
		PublicKey: base64.RawURLEncoding.EncodeToString(publicKey), PrivateKey: base64.RawURLEncoding.EncodeToString(privateKey),
		KeyProvider: "software-file", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, "https://gateway.example/v1/buckets/example/push?branch=work", bytes.NewReader([]byte("payload")))
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.Authorize(request); err != nil {
		t.Fatal(err)
	}
	if got := request.Header.Get("Authorization"); got != "MaltDevice key_test" {
		t.Fatalf("Authorization = %q", got)
	}
	counter, err := strconv.ParseUint(request.Header.Get(deviceCounterHeader), 10, 64)
	if err != nil || counter != 1 {
		t.Fatalf("counter = %d, %v", counter, err)
	}
	body, _ := request.GetBody()
	data, _ := io.ReadAll(body)
	_ = body.Close()
	digest := sha256.Sum256(data)
	message := deviceRequestMessage(
		"key_test", "1", strconv.FormatInt(now.Unix(), 10), http.MethodPost,
		"gateway.example", "/v1/buckets/example/push?branch=work",
		base64.RawURLEncoding.EncodeToString(digest[:]),
	)
	signature, err := base64.RawURLEncoding.DecodeString(request.Header.Get(deviceSignatureHeader))
	if err != nil || !ed25519.Verify(publicKey, message, signature) {
		t.Fatal("request proof did not verify")
	}
	loaded, err := provider.Load()
	if err != nil || loaded.Counter != 1 {
		t.Fatalf("persisted counter = %d, %v", loaded.Counter, err)
	}
}

func TestFileProviderReportsMissingCredential(t *testing.T) {
	_, err := (FileProvider{Path: filepath.Join(t.TempDir(), "missing.json")}).Load()
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing error = %v", err)
	}
}

func TestFileProviderSaveUsesAuthorizationLock(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	provider := FileProvider{Path: filepath.Join(t.TempDir(), "device.json")}
	credential := Credential{
		Version: 2, Gateway: "https://gateway.example", Name: "Laptop",
		CredentialID: "key_first", TenantID: "tenant", PrincipalID: "principal",
		PublicKey: base64.RawURLEncoding.EncodeToString(publicKey), PrivateKey: base64.RawURLEncoding.EncodeToString(privateKey),
		KeyProvider: "software-file", CreatedAt: time.Now().UTC(),
	}
	if err := provider.Save(credential); err != nil {
		t.Fatal(err)
	}
	unlock, err := filelock.Acquire(provider.Path+".lock", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	credential.CredentialID = "key_second"
	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		close(started)
		finished <- provider.Save(credential)
	}()
	<-started
	select {
	case err := <-finished:
		_ = unlock()
		t.Fatalf("Save bypassed the credential lock: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	loaded, err := provider.Load()
	if err != nil || loaded.CredentialID != "key_second" {
		t.Fatalf("saved credential = %#v, %v", loaded, err)
	}
}

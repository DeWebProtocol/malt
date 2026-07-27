package keyring

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCreateOpenAndDeriveBucketKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	created, err := Create(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("keyring mode = %#o, want 0600", info.Mode().Perm())
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := created.BucketKey(created.ActiveEpoch(), "bucket-a")
	if err != nil {
		t.Fatal(err)
	}
	again, err := reopened.BucketKey(reopened.ActiveEpoch(), "bucket-a")
	if err != nil {
		t.Fatal(err)
	}
	other, err := reopened.BucketKey(reopened.ActiveEpoch(), "bucket-b")
	if err != nil {
		t.Fatal(err)
	}
	if first != again {
		t.Fatal("same epoch and Bucket did not derive the same key")
	}
	if first == other {
		t.Fatal("different Buckets derived the same key")
	}
	next, err := reopened.Rotate()
	if err != nil {
		t.Fatal(err)
	}
	if next != 2 {
		t.Fatalf("rotated epoch = %d, want 2", next)
	}
	if _, err := reopened.BucketKey(1, "bucket-a"); err != nil {
		t.Fatalf("old epoch was not retained: %v", err)
	}
	rotated, err := reopened.BucketKey(2, "bucket-a")
	if err != nil {
		t.Fatal(err)
	}
	if rotated == first {
		t.Fatal("rotated epoch reused the old derived key")
	}
}

func TestRecoveryExportImportRoundTripRejectsWrongPassphraseAndMergesIdempotently(t *testing.T) {
	root := t.TempDir()
	sourcePath := filepath.Join(root, "source-keys.json")
	source, err := Create(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	epochOne, err := os.ReadFile(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.Rotate(); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "recovery.json")
	passphrase := []byte("correct horse battery staple")
	if err := source.ExportRecovery(bundle, passphrase); err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	data, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	if _, ok := envelope["ciphertext"]; !ok || string(data) == "" {
		t.Fatalf("invalid recovery envelope: %s", data)
	}
	if _, err := ImportRecovery(bundle, filepath.Join(root, "wrong.json"), []byte("wrong passphrase long enough")); err == nil {
		t.Fatal("wrong recovery passphrase was accepted")
	}
	targetPath := filepath.Join(root, "restored-keys.json")
	if err := os.WriteFile(targetPath, epochOne, 0o600); err != nil {
		t.Fatal(err)
	}
	restored, err := ImportRecovery(bundle, targetPath, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	for _, epoch := range []uint32{1, 2} {
		want, err := source.BucketKey(epoch, "bucket-a")
		if err != nil {
			t.Fatal(err)
		}
		got, err := restored.BucketKey(epoch, "bucket-a")
		if err != nil || got != want {
			t.Fatalf("restored epoch %d key mismatch: %v", epoch, err)
		}
	}
	if restored.ActiveEpoch() != 2 {
		t.Fatalf("merged active epoch = %d, want 2", restored.ActiveEpoch())
	}
	reimported, err := ImportRecovery(bundle, targetPath, passphrase)
	if err != nil {
		t.Fatalf("idempotent recovery import failed: %v", err)
	}
	if reimported.ActiveEpoch() != 2 {
		t.Fatalf("reimported active epoch = %d, want 2", reimported.ActiveEpoch())
	}
}

func TestRecoveryImportRejectsConflictingEpochWithoutChangingKeyring(t *testing.T) {
	root := t.TempDir()
	source, err := Create(filepath.Join(root, "source-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "recovery.json")
	passphrase := []byte("correct horse battery staple")
	if err := source.ExportRecovery(bundle, passphrase); err != nil {
		t.Fatal(err)
	}
	targetPath := filepath.Join(root, "target-keys.json")
	if _, err := Create(targetPath); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ImportRecovery(bundle, targetPath, passphrase); err == nil {
		t.Fatal("conflicting recovery epoch was accepted")
	}
	after, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("conflicting recovery import changed the existing keyring")
	}
}

func TestRecoveryBundleTamperingFailsAuthentication(t *testing.T) {
	root := t.TempDir()
	source, err := Create(filepath.Join(root, "source-keys.json"))
	if err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "recovery.json")
	passphrase := []byte("correct horse battery staple")
	if err := source.ExportRecovery(bundle, passphrase); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(data, &envelope); err != nil {
		t.Fatal(err)
	}
	ciphertext, err := base64.RawStdEncoding.DecodeString(envelope["ciphertext"].(string))
	if err != nil {
		t.Fatal(err)
	}
	ciphertext[0] ^= 0x01
	envelope["ciphertext"] = base64.RawStdEncoding.EncodeToString(ciphertext)
	data, err = json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundle, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ImportRecovery(bundle, filepath.Join(root, "restored.json"), passphrase); err == nil {
		t.Fatal("tampered recovery bundle was accepted")
	}
}

func TestOpenTightensKeyringPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows owner-only protection is verified through the DACL test in internal/securefile")
	}
	path := filepath.Join(t.TempDir(), "keys.json")
	if _, err := Create(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("keyring mode = %#o, want 0600", info.Mode().Perm())
	}
}

func TestOpenMissingKeyringFailsInsteadOfGeneratingAReplacement(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("Open generated or accepted a missing keyring")
	}
}

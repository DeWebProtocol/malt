package keyring

import (
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

package backup

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRecoverSnapshotDirectoryRemovesOnlySelectedPlanNamespace(t *testing.T) {
	parent := t.TempDir()
	first := filepath.Join(parent, "plan-first.encrypted-snapshots")
	second := filepath.Join(parent, "plan-second.encrypted-snapshots")
	for directory, body := range map[string]string{first: "first", second: "second"} {
		staging := filepath.Join(directory, ".encrypted-snapshot-crashed", "blocks")
		if err := os.MkdirAll(staging, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(staging, "block"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := recoverSnapshotDirectory(first); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first); !os.IsNotExist(err) {
		t.Fatalf("recovered Plan snapshot namespace still exists: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(second, ".encrypted-snapshot-crashed", "blocks", "block"))
	if err != nil || string(body) != "second" {
		t.Fatalf("other Plan snapshot changed: body=%q err=%v", body, err)
	}
}

func TestEncryptedPlanSnapshotCloseReportsDeletionFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory write-permission failure injection is Unix-specific")
	}
	namespace := filepath.Join(t.TempDir(), "plan.encrypted-snapshots")
	staging := filepath.Join(namespace, ".encrypted-snapshot-active")
	if err := os.MkdirAll(filepath.Join(staging, "blocks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "blocks", "block"), []byte("ciphertext"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(namespace, 0o500); err != nil {
		t.Fatal(err)
	}
	snapshot := &encryptedPlanSnapshot{directory: staging}
	err := snapshot.Close()
	if chmodErr := os.Chmod(namespace, 0o700); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	if err == nil || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("snapshot Close error = %v", err)
	}
	if _, statErr := os.Stat(staging); statErr != nil {
		t.Fatalf("failed Close did not retain retryable snapshot: %v", statErr)
	}
	if err := recoverSnapshotDirectory(namespace); err != nil {
		t.Fatal(err)
	}
}

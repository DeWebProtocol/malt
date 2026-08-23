package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRecoverPreparedInstallLeavesOriginalTree(t *testing.T) {
	parent := t.TempDir()
	entry := makeInstallEntry(t, parent, "binding", "old", "new", true)
	journal := filepath.Join(parent, "transaction.json")
	writeTestTransaction(t, journal, installStatePrepared, entry)
	service := &PlanService{plan: Plan{ID: "plan_test"}}
	if err := service.recoverInstallTransaction(journal); err != nil {
		t.Fatal(err)
	}
	assertTreeValue(t, entry.Destination, "old")
	assertPathMissing(t, entry.Staging)
	assertPathMissing(t, journal)
}

func TestRecoverPartialMultiBindingInstallRestoresAllOriginals(t *testing.T) {
	parent := t.TempDir()
	first := makeInstallEntry(t, parent, "first", "first-old", "first-new", true)
	second := makeInstallEntry(t, parent, "second", "second-old", "second-new", true)
	if err := os.Rename(first.Destination, first.Rollback); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(first.Next, first.Destination); err != nil {
		t.Fatal(err)
	}
	first.Phase = installPhaseInstalled
	if err := os.Rename(second.Destination, second.Rollback); err != nil {
		t.Fatal(err)
	}
	second.Phase = installPhasePreserved
	journal := filepath.Join(parent, "transaction.json")
	writeTestTransaction(t, journal, installStatePrepared, first, second)
	service := &PlanService{plan: Plan{ID: "plan_test"}}
	if err := service.recoverInstallTransaction(journal); err != nil {
		t.Fatal(err)
	}
	assertTreeValue(t, first.Destination, "first-old")
	assertTreeValue(t, second.Destination, "second-old")
	assertPathMissing(t, first.Staging)
	assertPathMissing(t, second.Staging)
	assertPathMissing(t, journal)
}

func TestRecoverCompletedRollbackIsIdempotent(t *testing.T) {
	parent := t.TempDir()
	entry := makeInstallEntry(t, parent, "binding", "old", "new", true)
	if err := os.RemoveAll(entry.Next); err != nil {
		t.Fatal(err)
	}
	entry.Phase = installPhaseInstalled
	journal := filepath.Join(parent, "transaction.json")
	writeTestTransaction(t, journal, installStatePrepared, entry)
	service := &PlanService{plan: Plan{ID: "plan_test"}}
	if err := service.recoverInstallTransaction(journal); err != nil {
		t.Fatal(err)
	}
	assertTreeValue(t, entry.Destination, "old")
	assertPathMissing(t, entry.Staging)
	assertPathMissing(t, journal)
}

func TestRecoverResumesAfterStagingCleanupBeforeJournalRemoval(t *testing.T) {
	parent := t.TempDir()
	entry := makeInstallEntry(t, parent, "binding", "old", "new", true)
	journal := filepath.Join(parent, "transaction.json")
	writeTestTransaction(t, journal, installStatePrepared, entry)
	if err := removeInstallStaging([]installTransactionEntry{entry}); err != nil {
		t.Fatal(err)
	}
	assertPathMissing(t, entry.Staging)
	if _, err := os.Stat(filepath.Join(parent, entry.ParentPin)); err != nil {
		t.Fatalf("parent pin was removed before journal: %v", err)
	}
	if _, err := os.Stat(journal); err != nil {
		t.Fatalf("journal was removed before the parent pin: %v", err)
	}
	service := &PlanService{plan: Plan{ID: "plan_test"}}
	if err := service.recoverInstallTransaction(journal); err != nil {
		t.Fatal(err)
	}
	assertTreeValue(t, entry.Destination, "old")
	assertPathMissing(t, filepath.Join(parent, entry.ParentPin))
	assertPathMissing(t, journal)
}

func TestRecoverRetainsJournalAndPinWhenStagingCleanupFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory write-permission failure injection is Unix-specific")
	}
	parent := t.TempDir()
	entry := makeInstallEntry(t, parent, "binding", "old", "new", true)
	journal := filepath.Join(t.TempDir(), "transaction.json")
	writeTestTransaction(t, journal, installStatePrepared, entry)
	if err := os.Chmod(parent, 0o500); err != nil {
		t.Fatal(err)
	}
	service := &PlanService{plan: Plan{ID: "plan_test"}}
	err := service.recoverInstallTransaction(journal)
	if chmodErr := os.Chmod(parent, 0o700); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	if err == nil || !strings.Contains(err.Error(), "remove installation staging") {
		t.Fatalf("cleanup failure error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(parent, entry.ParentPin)); err != nil {
		t.Fatalf("cleanup failure removed parent pin: %v", err)
	}
	if _, err := os.Stat(journal); err != nil {
		t.Fatalf("cleanup failure removed journal: %v", err)
	}
	if err := service.recoverInstallTransaction(journal); err != nil {
		t.Fatal(err)
	}
}

func TestPreJournalCleanupKeepsPinWhenStagingRemovalFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory write-permission failure injection is Unix-specific")
	}
	parent := t.TempDir()
	entry := makeInstallEntry(t, parent, "binding", "old", "new", true)
	if err := os.Chmod(entry.Staging, 0o500); err != nil {
		t.Fatal(err)
	}
	err := cleanupInstallStaging([]installTransactionEntry{entry})
	if chmodErr := os.Chmod(entry.Staging, 0o700); chmodErr != nil {
		t.Fatal(chmodErr)
	}
	if err == nil || !strings.Contains(err.Error(), "remove installation staging") {
		t.Fatalf("pre-journal cleanup failure = %v", err)
	}
	if _, err := os.Stat(filepath.Join(parent, entry.ParentPin)); err != nil {
		t.Fatalf("pre-journal cleanup removed parent pin: %v", err)
	}
	if err := cleanupInstallStaging([]installTransactionEntry{entry}); err != nil {
		t.Fatal(err)
	}
	assertPathMissing(t, entry.Staging)
	assertPathMissing(t, filepath.Join(parent, entry.ParentPin))
}

func TestPrepareInstallEntryDurablyCreatesNestedParentChain(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "new", "nested", "parent")
	entry, err := prepareInstallEntry("binding", filepath.Join(parent, "destination"), ".malt-restore-")
	if err != nil {
		t.Fatal(err)
	}
	for path := parent; filepath.Base(path) != "."; path = filepath.Dir(path) {
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			t.Fatalf("created parent %s: info=%v err=%v", path, info, err)
		}
		if path == filepath.Dir(filepath.Dir(filepath.Dir(parent))) {
			break
		}
	}
	if err := cleanupInstallStaging([]installTransactionEntry{entry}); err != nil {
		t.Fatal(err)
	}
}

func TestRecoverPartialMultiBindingRollbackCanResume(t *testing.T) {
	parent := t.TempDir()
	blocked := makeInstallEntry(t, parent, "blocked", "blocked-old", "blocked-new", true)
	recovered := makeInstallEntry(t, parent, "recovered", "recovered-old", "recovered-new", true)
	if err := os.Rename(recovered.Destination, recovered.Rollback); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(recovered.Next, recovered.Destination); err != nil {
		t.Fatal(err)
	}
	recovered.Phase = installPhaseInstalled
	if err := os.WriteFile(filepath.Join(parent, blocked.ParentPin), []byte(strings.Repeat("0", 64)), 0o600); err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(parent, "transaction.json")
	writeTestTransaction(t, journal, installStatePrepared, blocked, recovered)
	service := &PlanService{plan: Plan{ID: "plan_test"}}
	if err := service.recoverInstallTransaction(journal); err == nil || !strings.Contains(err.Error(), "parent identity") {
		t.Fatalf("first recovery error = %v", err)
	}
	assertTreeValue(t, recovered.Destination, "recovered-old")
	if err := os.WriteFile(filepath.Join(parent, blocked.ParentPin), []byte(blocked.ParentToken), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := service.recoverInstallTransaction(journal); err != nil {
		t.Fatal(err)
	}
	assertTreeValue(t, blocked.Destination, "blocked-old")
	assertTreeValue(t, recovered.Destination, "recovered-old")
	assertPathMissing(t, blocked.Staging)
	assertPathMissing(t, recovered.Staging)
	assertPathMissing(t, journal)
}

func TestRecoverInstalledNewDestinationRemovesUncommittedTree(t *testing.T) {
	parent := t.TempDir()
	entry := makeInstallEntry(t, parent, "new-binding", "", "new", false)
	if err := os.Rename(entry.Next, entry.Destination); err != nil {
		t.Fatal(err)
	}
	entry.Phase = installPhaseInstalled
	journal := filepath.Join(parent, "transaction.json")
	writeTestTransaction(t, journal, installStatePrepared, entry)
	service := &PlanService{plan: Plan{ID: "plan_test"}}
	if err := service.recoverInstallTransaction(journal); err != nil {
		t.Fatal(err)
	}
	assertPathMissing(t, entry.Destination)
	assertPathMissing(t, entry.Staging)
	assertPathMissing(t, journal)
}

func TestRecoverCommittedInstallKeepsNewTreeAndCleansJournal(t *testing.T) {
	parent := t.TempDir()
	entry := makeInstallEntry(t, parent, "binding", "old", "new", true)
	if err := os.Rename(entry.Destination, entry.Rollback); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(entry.Next, entry.Destination); err != nil {
		t.Fatal(err)
	}
	entry.Phase = installPhaseInstalled
	journal := filepath.Join(parent, "transaction.json")
	writeTestTransaction(t, journal, installStateCommitted, entry)
	service := &PlanService{plan: Plan{ID: "plan_test"}}
	if err := service.recoverInstallTransaction(journal); err != nil {
		t.Fatal(err)
	}
	assertTreeValue(t, entry.Destination, "new")
	assertPathMissing(t, entry.Staging)
	assertPathMissing(t, journal)
}

func TestRecoverInvalidJournalFailsClosed(t *testing.T) {
	parent := t.TempDir()
	entry := makeInstallEntry(t, parent, "binding", "old", "new", true)
	entry.Phase = "unknown"
	transaction := installTransaction{
		Version: installTransactionVersion, PlanID: "plan_test",
		State: installStatePrepared, Entries: []installTransactionEntry{entry},
	}
	data, err := json.Marshal(transaction)
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(parent, "transaction.json")
	if err := os.WriteFile(journal, data, 0o600); err != nil {
		t.Fatal(err)
	}
	service := &PlanService{plan: Plan{ID: "plan_test"}}
	if err := service.recoverInstallTransaction(journal); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("invalid journal error = %v", err)
	}
	assertTreeValue(t, entry.Destination, "old")
	if _, err := os.Stat(journal); err != nil {
		t.Fatalf("invalid journal was removed: %v", err)
	}
}

func TestRecoverQuarantinesEditsMadeAfterInterruptedInstall(t *testing.T) {
	parent := t.TempDir()
	entry := makeInstallEntry(t, parent, "binding", "old", "new", true)
	if err := os.Rename(entry.Destination, entry.Rollback); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(entry.Next, entry.Destination); err != nil {
		t.Fatal(err)
	}
	entry.Phase = installPhaseInstalled
	writeTreeValue(t, entry.Destination, "user-edit")
	journal := filepath.Join(parent, "transaction.json")
	writeTestTransaction(t, journal, installStatePrepared, entry)
	service := &PlanService{plan: Plan{ID: "plan_test"}}
	err := service.recoverInstallTransaction(journal)
	var quarantineErr *RecoveryQuarantineError
	if !errors.As(err, &quarantineErr) || len(quarantineErr.Paths) != 1 {
		t.Fatalf("recovery error = %v", err)
	}
	assertTreeValue(t, entry.Destination, "old")
	assertTreeValue(t, entry.Staging+"-recovery", "user-edit")
	assertPathMissing(t, journal)
}

func TestRecoverTransactionJournalsFindsUnregisteredBranchRestore(t *testing.T) {
	parent := t.TempDir()
	entry := makeInstallEntry(t, parent, "binding", "old", "new", true)
	if err := os.Rename(entry.Destination, entry.Rollback); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(entry.Next, entry.Destination); err != nil {
		t.Fatal(err)
	}
	entry.Phase = installPhaseInstalled
	journal := filepath.Join(parent, "restore_ephemeral.json.operation.lock.restore-transaction.json")
	writeTestTransaction(t, journal, installStatePrepared, entry)
	if err := RecoverTransactionJournals(parent); err != nil {
		t.Fatal(err)
	}
	assertTreeValue(t, entry.Destination, "old")
	assertPathMissing(t, journal)
}

func TestRecoverInstallFailsClosedWhenPinnedParentPathIsReplaced(t *testing.T) {
	container := t.TempDir()
	parent := filepath.Join(container, "parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	entry := makeInstallEntry(t, parent, "binding", "old", "new", true)
	journal := filepath.Join(t.TempDir(), "transaction.json")
	writeTestTransaction(t, journal, installStatePrepared, entry)
	moved := filepath.Join(container, "parent-moved")
	if err := os.Rename(parent, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTreeValue(t, filepath.Join(parent, "binding"), "external")
	service := &PlanService{plan: Plan{ID: "plan_test"}}
	if err := service.recoverInstallTransaction(journal); err == nil || !strings.Contains(err.Error(), "parent") {
		t.Fatalf("replaced parent recovery error = %v", err)
	}
	assertTreeValue(t, filepath.Join(parent, "binding"), "external")
	assertTreeValue(t, filepath.Join(moved, "binding"), "old")
	if _, err := os.Stat(journal); err != nil {
		t.Fatalf("failed-closed recovery removed journal: %v", err)
	}
}

func TestRecoverLegacyPathBasedInstallJournalRequiresPreviousRuntime(t *testing.T) {
	journal := filepath.Join(t.TempDir(), "transaction.json")
	data, err := json.Marshal(installTransaction{Version: 1, PlanID: "plan_test", State: installStatePrepared})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(journal, data, 0o600); err != nil {
		t.Fatal(err)
	}
	service := &PlanService{plan: Plan{ID: "plan_test"}}
	if err := service.recoverInstallTransaction(journal); err == nil || !strings.Contains(err.Error(), "previous MALT runtime") {
		t.Fatalf("legacy install journal error = %v", err)
	}
}

func makeInstallEntry(t *testing.T, parent, name, oldValue, newValue string, hadCurrent bool) installTransactionEntry {
	t.Helper()
	destination := filepath.Join(parent, name)
	entry, err := prepareInstallEntry(name, destination, ".malt-sync-")
	if err != nil {
		t.Fatal(err)
	}
	writeTreeValue(t, entry.Next, newValue)
	installedFingerprint, err := plaintextContentFingerprint(context.Background(), entry.Next)
	if err != nil {
		t.Fatal(err)
	}
	entry.HadCurrent = hadCurrent
	entry.InstalledFingerprint = installedFingerprint
	if hadCurrent {
		writeTreeValue(t, destination, oldValue)
		entry.OriginalFingerprint, err = plaintextContentFingerprint(context.Background(), destination)
		if err != nil {
			t.Fatal(err)
		}
	}
	return entry
}

func writeTestTransaction(t *testing.T, path, state string, entries ...installTransactionEntry) {
	t.Helper()
	if err := writeInstallTransaction(path, installTransaction{
		Version: installTransactionVersion, PlanID: "plan_test", State: state, Entries: entries,
	}); err != nil {
		t.Fatal(err)
	}
}

func writeTreeValue(t *testing.T, root, value string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "value.txt"), []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertTreeValue(t *testing.T, root, want string) {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(root, "value.txt"))
	if err != nil || string(body) != want {
		t.Fatalf("%s = %q, %v; want %q", root, body, err, want)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s still exists: %v", path, err)
	}
}

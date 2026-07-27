package backup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
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

func makeInstallEntry(t *testing.T, parent, name, oldValue, newValue string, hadCurrent bool) installTransactionEntry {
	t.Helper()
	destination := filepath.Join(parent, name)
	staging, err := os.MkdirTemp(parent, ".malt-sync-"+name+"-")
	if err != nil {
		t.Fatal(err)
	}
	next := filepath.Join(staging, "next")
	writeTreeValue(t, next, newValue)
	installedFingerprint, err := plaintextContentFingerprint(context.Background(), next)
	if err != nil {
		t.Fatal(err)
	}
	entry := installTransactionEntry{
		Name: name, Destination: destination, Staging: staging, Next: next,
		Rollback: filepath.Join(staging, "previous"), HadCurrent: hadCurrent,
		InstalledFingerprint: installedFingerprint, Phase: installPhasePrepared,
	}
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

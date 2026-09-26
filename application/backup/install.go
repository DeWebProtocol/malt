package backup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/dewebprotocol/malt-client/internal/durablefile"
	"github.com/dewebprotocol/malt-client/internal/securefile"
	"github.com/dewebprotocol/malt-client/internal/strictjson"
)

// installer executes and recovers durable local filesystem transactions. It
// needs only a plan identity and journal path, never network, keys, or trust.
type installer struct{ planID string }

const installTransactionVersion = 2

const (
	installStatePrepared  = "prepared"
	installStateCommitted = "committed"

	installPhasePrepared  = "prepared"
	installPhasePreserved = "preserved"
	installPhaseInstalled = "installed"
)

type installTransaction struct {
	Version int                       `json:"version"`
	PlanID  string                    `json:"plan_id"`
	State   string                    `json:"state"`
	Entries []installTransactionEntry `json:"entries"`
}

type installTransactionEntry struct {
	Name                 string `json:"name"`
	Destination          string `json:"destination"`
	Staging              string `json:"staging"`
	Next                 string `json:"next"`
	Rollback             string `json:"rollback"`
	ParentPin            string `json:"parent_pin"`
	ParentToken          string `json:"parent_token"`
	ExpectedFingerprint  string `json:"expected_fingerprint,omitempty"`
	OriginalFingerprint  string `json:"original_fingerprint,omitempty"`
	InstalledFingerprint string `json:"installed_fingerprint"`
	HadCurrent           bool   `json:"had_current"`
	Phase                string `json:"phase"`
}

type installRoot struct {
	root        *os.Root
	destination string
	staging     string
	next        string
	rollback    string
	quarantine  string
}

func prepareInstallEntry(name, destination, stagingPrefix string) (installTransactionEntry, error) {
	if destination == "" {
		return installTransactionEntry{}, fmt.Errorf("filesystem installation destination is empty")
	}
	destination, err := filepath.Abs(destination)
	if err != nil {
		return installTransactionEntry{}, fmt.Errorf("resolve filesystem installation destination: %w", err)
	}
	parent := filepath.Dir(destination)
	if err := mkdirAllDurable(parent, 0o700); err != nil {
		return installTransactionEntry{}, err
	}
	root, _, err := openPinnedSource(parent)
	if err != nil {
		return installTransactionEntry{}, fmt.Errorf("pin filesystem installation parent: %w", err)
	}
	defer root.Close()
	random := make([]byte, 24)
	if _, err := rand.Read(random); err != nil {
		return installTransactionEntry{}, err
	}
	id := hex.EncodeToString(random)
	stagingName := stagingPrefix + id[:24]
	parentPin := ".malt-install-pin-" + id[24:]
	if err := root.Mkdir(stagingName, 0o700); err != nil {
		return installTransactionEntry{}, err
	}
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		_ = root.RemoveAll(stagingName)
		return installTransactionEntry{}, err
	}
	token := hex.EncodeToString(tokenBytes)
	pin, err := root.OpenFile(parentPin, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = root.RemoveAll(stagingName)
		return installTransactionEntry{}, err
	}
	_, writeErr := pin.Write([]byte(token))
	syncErr := pin.Sync()
	closeErr := pin.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		_ = root.Remove(parentPin)
		_ = root.RemoveAll(stagingName)
		return installTransactionEntry{}, err
	}
	if err := syncRoot(root); err != nil {
		_ = root.Remove(parentPin)
		_ = root.RemoveAll(stagingName)
		return installTransactionEntry{}, err
	}
	staging := filepath.Join(parent, stagingName)
	return installTransactionEntry{
		Name: name, Destination: destination, Staging: staging,
		Next: filepath.Join(staging, "next"), Rollback: filepath.Join(staging, "previous"),
		ParentPin: parentPin, ParentToken: token, Phase: installPhasePrepared,
	}, nil
}

// mkdirAllDurable creates a directory chain and persists each newly reachable
// directory plus its parent entry before installation state is journaled.
func mkdirAllDurable(path string, perm os.FileMode) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	missing := make([]string, 0)
	current := abs
	for {
		info, statErr := os.Stat(current)
		if statErr == nil {
			if !info.IsDir() {
				return fmt.Errorf("filesystem installation parent is not a directory: %s", current)
			}
			break
		}
		if !errors.Is(statErr, os.ErrNotExist) {
			return statErr
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return statErr
		}
		current = parent
	}
	if len(missing) == 0 {
		return nil
	}
	if err := os.MkdirAll(abs, perm); err != nil {
		return err
	}
	for _, directory := range missing {
		if err := durablefile.SyncParent(filepath.Join(directory, ".malt-directory-sync")); err != nil {
			return fmt.Errorf("persist created filesystem directory %s: %w", directory, err)
		}
		if err := durablefile.SyncParent(directory); err != nil {
			return fmt.Errorf("persist created filesystem directory entry %s: %w", directory, err)
		}
	}
	return nil
}

func openInstallRoot(entry installTransactionEntry) (*installRoot, error) {
	parent := filepath.Dir(entry.Destination)
	root, _, err := openPinnedSource(parent)
	if err != nil {
		return nil, err
	}
	closeWith := func(err error) (*installRoot, error) {
		_ = root.Close()
		return nil, err
	}
	pin, err := root.ReadFile(entry.ParentPin)
	if err != nil {
		return closeWith(fmt.Errorf("filesystem installation parent pin is unavailable: %w", err))
	}
	if string(pin) != entry.ParentToken {
		return closeWith(fmt.Errorf("filesystem installation parent identity changed"))
	}
	staging := filepath.Base(entry.Staging)
	return &installRoot{
		root: root, destination: filepath.Base(entry.Destination), staging: staging,
		next: filepath.Join(staging, "next"), rollback: filepath.Join(staging, "previous"),
		quarantine: staging + "-recovery",
	}, nil
}

func (r *installRoot) Close() error {
	if r == nil || r.root == nil {
		return nil
	}
	return r.root.Close()
}

func syncRoot(root *os.Root) error {
	file, err := root.Open(".")
	if err != nil {
		return err
	}
	syncErr := file.Sync()
	closeErr := file.Close()
	return errors.Join(syncErr, closeErr)
}

// syncInstallTree persists a prepared installation subtree bottom-up. Symlink
// entries have no independently fsync-able handle; their containing directory
// is synced after every child has been visited.
func syncInstallTree(root *os.Root, name string) error {
	info, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil
	}
	if info.IsDir() {
		childRoot, err := root.OpenRoot(name)
		if err != nil {
			return err
		}
		opened, err := childRoot.Open(".")
		if err != nil {
			_ = childRoot.Close()
			return err
		}
		openedInfo, statErr := opened.Stat()
		if statErr == nil && (!openedInfo.IsDir() || !os.SameFile(info, openedInfo)) {
			statErr = fmt.Errorf("prepared installation directory changed before it was persisted: %s", name)
		}
		entries, readErr := opened.ReadDir(-1)
		closeErr := opened.Close()
		if err := errors.Join(statErr, readErr, closeErr); err != nil {
			_ = childRoot.Close()
			return err
		}
		for _, entry := range entries {
			if err := syncInstallTree(childRoot, entry.Name()); err != nil {
				_ = childRoot.Close()
				return err
			}
		}
		syncErr := syncRoot(childRoot)
		closeRootErr := childRoot.Close()
		return errors.Join(syncErr, closeRootErr)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("prepared installation contains unsupported file type: %s", name)
	}
	file, err := root.Open(name)
	if err != nil {
		return err
	}
	openedInfo, statErr := file.Stat()
	if statErr == nil && (!openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo)) {
		statErr = fmt.Errorf("prepared installation file changed before it was persisted: %s", name)
	}
	var syncErr error
	if statErr == nil {
		syncErr = file.Sync()
	}
	closeErr := file.Close()
	return errors.Join(statErr, syncErr, closeErr)
}

func (i installer) installPrepared(ctx context.Context, journalPath string, entries []installTransactionEntry) error {
	failBeforeJournal := func(operationErr error) error {
		return cleanupInstallError(operationErr, entries)
	}
	for i := range entries {
		parent, err := openInstallRoot(entries[i])
		if err != nil {
			return failBeforeJournal(fmt.Errorf("pin prepared installation %s: %w", entries[i].Name, err))
		}
		if entries[i].HadCurrent {
			fingerprint, err := plaintextContentFingerprintRoot(ctx, parent.root, parent.destination)
			if err != nil {
				_ = parent.Close()
				return failBeforeJournal(fmt.Errorf("fingerprint original %s: %w", entries[i].Name, err))
			}
			entries[i].OriginalFingerprint = fingerprint
		}
		fingerprint, err := plaintextContentFingerprintRoot(ctx, parent.root, parent.next)
		if err != nil {
			_ = parent.Close()
			return failBeforeJournal(fmt.Errorf("fingerprint prepared %s: %w", entries[i].Name, err))
		}
		entries[i].InstalledFingerprint = fingerprint
		if err := syncInstallTree(parent.root, parent.staging); err != nil {
			_ = parent.Close()
			return failBeforeJournal(fmt.Errorf("persist prepared %s: %w", entries[i].Name, err))
		}
		if err := syncRoot(parent.root); err != nil {
			_ = parent.Close()
			return failBeforeJournal(fmt.Errorf("persist prepared installation parent for %s: %w", entries[i].Name, err))
		}
		if err := parent.Close(); err != nil {
			return failBeforeJournal(err)
		}
	}
	transaction := installTransaction{
		Version: installTransactionVersion, PlanID: i.planID,
		State: installStatePrepared, Entries: append([]installTransactionEntry(nil), entries...),
	}
	if err := validateInstallTransaction(transaction); err != nil {
		return failBeforeJournal(err)
	}
	if err := writeInstallTransaction(journalPath, transaction); err != nil {
		return failBeforeJournal(fmt.Errorf("journal prepared filesystem installation: %w", err))
	}
	fail := func(operationErr error) error {
		recoveryErr := i.recoverInstallTransaction(journalPath)
		if recoveryErr != nil {
			return fmt.Errorf("%v; automatic rollback also failed: %w", operationErr, recoveryErr)
		}
		return operationErr
	}
	for i := range transaction.Entries {
		entry := &transaction.Entries[i]
		parent, err := openInstallRoot(*entry)
		if err != nil {
			return fail(fmt.Errorf("pin installation parent for %s: %w", entry.Name, err))
		}
		if entry.HadCurrent {
			if entry.ExpectedFingerprint != "" {
				actual, err := fingerprintRootedDirectory(ctx, parent.root, parent.destination, filepath.Base(entry.Destination))
				if err != nil {
					_ = parent.Close()
					return fail(fmt.Errorf("recheck %s before installation: %w", entry.Name, err))
				}
				if actual != entry.ExpectedFingerprint {
					_ = parent.Close()
					return fail(fmt.Errorf("%s changed after backup; its synchronized snapshot was not installed", entry.Name))
				}
			}
			currentFingerprint, err := plaintextContentFingerprintRoot(ctx, parent.root, parent.destination)
			if err != nil {
				_ = parent.Close()
				return fail(fmt.Errorf("fingerprint %s before installation: %w", entry.Name, err))
			}
			if currentFingerprint != entry.OriginalFingerprint {
				_ = parent.Close()
				return fail(fmt.Errorf("%s changed while synchronization was being prepared; its snapshot was not installed", entry.Name))
			}
			info, err := parent.root.Lstat(parent.destination)
			if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				_ = parent.Close()
				return fail(fmt.Errorf("binding destination changed during installation: %s", entry.Destination))
			}
			if err := parent.root.Rename(parent.destination, parent.rollback); err != nil {
				_ = parent.Close()
				return fail(fmt.Errorf("preserve %s before installation: %w", entry.Name, err))
			}
			if err := syncRoot(parent.root); err != nil {
				_ = parent.Close()
				return fail(fmt.Errorf("persist preserved %s: %w", entry.Name, err))
			}
			entry.Phase = installPhasePreserved
			if err := writeInstallTransaction(journalPath, transaction); err != nil {
				_ = parent.Close()
				return fail(fmt.Errorf("journal preserved %s: %w", entry.Name, err))
			}
		} else if _, err := parent.root.Lstat(parent.destination); !errors.Is(err, os.ErrNotExist) {
			_ = parent.Close()
			return fail(fmt.Errorf("binding destination appeared during installation: %s", entry.Destination))
		}
		if err := parent.root.Rename(parent.next, parent.destination); err != nil {
			_ = parent.Close()
			return fail(fmt.Errorf("install %s: %w", entry.Name, err))
		}
		if err := syncRoot(parent.root); err != nil {
			_ = parent.Close()
			return fail(fmt.Errorf("persist installed %s: %w", entry.Name, err))
		}
		if err := parent.Close(); err != nil {
			return fail(fmt.Errorf("close installation parent for %s: %w", entry.Name, err))
		}
		entry.Phase = installPhaseInstalled
		if err := writeInstallTransaction(journalPath, transaction); err != nil {
			return fail(fmt.Errorf("journal installed %s: %w", entry.Name, err))
		}
	}
	transaction.State = installStateCommitted
	if err := writeInstallTransaction(journalPath, transaction); err != nil {
		return fail(fmt.Errorf("commit filesystem installation journal: %w", err))
	}
	if err := finalizeCommittedInstall(journalPath, transaction); err != nil {
		return fmt.Errorf("finalize committed filesystem installation: %w", err)
	}
	return nil
}

func (i installer) recoverInstallTransaction(journalPath string) error {
	transaction, found, err := readInstallTransaction(journalPath)
	if err != nil || !found {
		return err
	}
	if transaction.PlanID != i.planID {
		return fmt.Errorf("filesystem installation journal belongs to another backup plan")
	}
	if transaction.Version != installTransactionVersion {
		return fmt.Errorf("unsupported filesystem installation journal version %d", transaction.Version)
	}
	if err := validateInstallTransaction(transaction); err != nil {
		return fmt.Errorf("unsafe filesystem installation journal: %w", err)
	}
	if transaction.State == installStateCommitted {
		return finalizeCommittedInstall(journalPath, transaction)
	}
	var quarantined []string
	for i := len(transaction.Entries) - 1; i >= 0; i-- {
		entry := transaction.Entries[i]
		parent, err := openInstallRoot(entry)
		if err != nil {
			return fmt.Errorf("pin recovery parent for %s: %w", entry.Name, err)
		}
		destinationExists, err := pathExistsRoot(parent.root, parent.destination)
		if err != nil {
			_ = parent.Close()
			return err
		}
		rollbackExists, err := pathExistsRoot(parent.root, parent.rollback)
		if err != nil {
			_ = parent.Close()
			return err
		}
		nextExists, err := pathExistsRoot(parent.root, parent.next)
		if err != nil {
			_ = parent.Close()
			return err
		}
		quarantinePath := entry.Staging + "-recovery"
		quarantineExists, err := pathExistsRoot(parent.root, parent.quarantine)
		if err != nil {
			_ = parent.Close()
			return err
		}
		if entry.HadCurrent {
			if rollbackExists {
				if quarantineExists {
					quarantined = append(quarantined, quarantinePath)
				}
				if destinationExists {
					quarantine, err := preserveChangedInstallation(entry, quarantinePath, parent)
					if err != nil {
						_ = parent.Close()
						return err
					}
					if quarantine != "" {
						quarantined = append(quarantined, quarantine)
					}
				}
				if err := parent.root.Rename(parent.rollback, parent.destination); err != nil {
					_ = parent.Close()
					return fmt.Errorf("restore preserved %s: %w", entry.Name, err)
				}
				if err := syncRoot(parent.root); err != nil {
					_ = parent.Close()
					return err
				}
				_ = parent.Close()
				continue
			}
			if destinationExists {
				fingerprint, err := plaintextContentFingerprintRoot(context.Background(), parent.root, parent.destination)
				if err != nil {
					_ = parent.Close()
					return err
				}
				if fingerprint == entry.OriginalFingerprint {
					if quarantineExists {
						quarantined = append(quarantined, quarantinePath)
					}
					_ = parent.Close()
					continue
				}
			}
			if quarantineExists {
				_ = parent.Close()
				return fmt.Errorf("cannot safely recover original %s while recovery quarantine exists at %s", entry.Name, quarantinePath)
			}
			if !destinationExists || !nextExists || entry.Phase != installPhasePrepared {
				_ = parent.Close()
				return fmt.Errorf("cannot safely recover original %s from interrupted installation", entry.Name)
			}
			_ = parent.Close()
			continue
		}
		if destinationExists {
			if nextExists && entry.Phase == installPhasePrepared {
				_ = parent.Close()
				return fmt.Errorf("cannot remove unexpected destination while recovering %s", entry.Name)
			}
			quarantine, err := preserveChangedInstallation(entry, quarantinePath, parent)
			if err != nil {
				_ = parent.Close()
				return err
			}
			if quarantine != "" {
				quarantined = append(quarantined, quarantine)
			}
		} else if quarantineExists {
			quarantined = append(quarantined, quarantinePath)
		}
		_ = parent.Close()
	}
	if err := removeInstallStaging(transaction.Entries); err != nil {
		return err
	}
	if err := removeInstallTransaction(journalPath); err != nil {
		return err
	}
	if err := removeInstallPins(transaction.Entries); err != nil {
		return err
	}
	if len(quarantined) != 0 {
		sort.Strings(quarantined)
		return &RecoveryQuarantineError{Paths: quarantined}
	}
	return nil
}

func preserveChangedInstallation(entry installTransactionEntry, quarantinePath string, parent *installRoot) (string, error) {
	fingerprint, err := plaintextContentFingerprintRoot(context.Background(), parent.root, parent.destination)
	if err != nil {
		return "", err
	}
	if fingerprint == entry.InstalledFingerprint {
		if err := parent.root.RemoveAll(parent.destination); err != nil {
			return "", fmt.Errorf("remove interrupted installation %s: %w", entry.Name, err)
		}
		return "", syncRoot(parent.root)
	}
	if _, err := parent.root.Lstat(parent.quarantine); err == nil {
		return "", fmt.Errorf("recovery quarantine already exists for %s: %s", entry.Name, quarantinePath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := parent.root.Rename(parent.destination, parent.quarantine); err != nil {
		return "", fmt.Errorf("quarantine edits made after interrupted installation of %s: %w", entry.Name, err)
	}
	if err := syncRoot(parent.root); err != nil {
		return "", err
	}
	return quarantinePath, nil
}

type RecoveryQuarantineError struct {
	Paths []string
}

func (e *RecoveryQuarantineError) Error() string {
	return fmt.Sprintf(
		"edits made after an interrupted sync were preserved for manual recovery at %s",
		strings.Join(e.Paths, ", "),
	)
}

func validateInstallTransaction(transaction installTransaction) error {
	if transaction.Version != installTransactionVersion || strings.TrimSpace(transaction.PlanID) == "" ||
		(transaction.State != installStatePrepared && transaction.State != installStateCommitted) ||
		len(transaction.Entries) == 0 {
		return fmt.Errorf("filesystem installation journal is incomplete")
	}
	destinations := map[string]struct{}{}
	stagingPaths := map[string]struct{}{}
	for _, entry := range transaction.Entries {
		if strings.TrimSpace(entry.Name) == "" || !filepath.IsAbs(entry.Destination) || !filepath.IsAbs(entry.Staging) ||
			entry.Next != filepath.Join(entry.Staging, "next") || entry.Rollback != filepath.Join(entry.Staging, "previous") ||
			filepath.Dir(entry.Destination) != filepath.Dir(entry.Staging) ||
			filepath.Base(entry.Destination) == "." || filepath.Base(entry.Destination) == string(filepath.Separator) ||
			filepath.Base(entry.ParentPin) != entry.ParentPin || !strings.HasPrefix(entry.ParentPin, ".malt-install-pin-") ||
			len(entry.ParentToken) != 64 || strings.Trim(entry.ParentToken, "0123456789abcdef") != "" ||
			(!strings.HasPrefix(filepath.Base(entry.Staging), ".malt-sync-") &&
				!strings.HasPrefix(filepath.Base(entry.Staging), ".malt-restore-")) ||
			(entry.Phase != installPhasePrepared && entry.Phase != installPhasePreserved && entry.Phase != installPhaseInstalled) {
			return fmt.Errorf("filesystem installation entry %q is invalid", entry.Name)
		}
		if entry.InstalledFingerprint == "" || (entry.HadCurrent && entry.OriginalFingerprint == "") ||
			(!entry.HadCurrent && entry.OriginalFingerprint != "") {
			return fmt.Errorf("filesystem installation entry %q lacks recovery fingerprints", entry.Name)
		}
		if _, ok := destinations[entry.Destination]; ok {
			return fmt.Errorf("duplicate filesystem installation destination")
		}
		if _, ok := stagingPaths[entry.Staging]; ok {
			return fmt.Errorf("duplicate filesystem installation staging path")
		}
		destinations[entry.Destination] = struct{}{}
		stagingPaths[entry.Staging] = struct{}{}
	}
	return nil
}

func writeInstallTransaction(path string, transaction installTransaction) error {
	if err := validateInstallTransaction(transaction); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(transaction, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	file, err := os.CreateTemp(filepath.Dir(path), ".install-transaction-*.json")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := securefile.Secure(name); err != nil {
		_ = file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	return durablefile.SyncParent(path)
}

func readInstallTransaction(path string) (installTransaction, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return installTransaction{}, false, nil
	}
	if err != nil {
		return installTransaction{}, false, err
	}
	if err := securefile.Secure(path); err != nil {
		return installTransaction{}, false, err
	}
	var transaction installTransaction
	if err := strictjson.Decode(data, &transaction); err != nil {
		return installTransaction{}, false, fmt.Errorf("decode filesystem installation journal: %w", err)
	}
	return transaction, true, nil
}

func finalizeCommittedInstall(journalPath string, transaction installTransaction) error {
	for _, entry := range transaction.Entries {
		parent, err := openInstallRoot(entry)
		if err != nil {
			return fmt.Errorf("pin committed installation parent for %s: %w", entry.Name, err)
		}
		info, err := parent.root.Lstat(parent.destination)
		closeErr := parent.Close()
		if err != nil || closeErr != nil {
			return fmt.Errorf("committed installation destination is unavailable: %s: %w", entry.Destination, errors.Join(err, closeErr))
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("committed installation destination is unavailable: %s", entry.Destination)
		}
	}
	if err := removeInstallStaging(transaction.Entries); err != nil {
		return err
	}
	if err := removeInstallTransaction(journalPath); err != nil {
		return err
	}
	return removeInstallPins(transaction.Entries)
}

func removeInstallTransaction(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return durablefile.SyncParent(path)
}

func cleanupInstallStaging(entries []installTransactionEntry) error {
	if err := removeInstallStaging(entries); err != nil {
		return err
	}
	return removeInstallPins(entries)
}

func cleanupInstallError(operationErr error, entries []installTransactionEntry) error {
	cleanupErr := cleanupInstallStaging(entries)
	if cleanupErr == nil {
		return operationErr
	}
	cleanupErr = fmt.Errorf("clean prepared filesystem installation: %w", cleanupErr)
	return errors.Join(operationErr, cleanupErr)
}

func removeInstallStaging(entries []installTransactionEntry) error {
	for _, entry := range entries {
		parent, err := openPinnedInstallParent(entry)
		if err != nil {
			return fmt.Errorf("pin installation cleanup parent for %s: %w", entry.Name, err)
		}
		removeErr := parent.RemoveAll(filepath.Base(entry.Staging))
		var syncErr error
		if removeErr == nil {
			syncErr = syncRoot(parent)
		}
		closeErr := parent.Close()
		if err := errors.Join(removeErr, syncErr, closeErr); err != nil {
			return fmt.Errorf("remove installation staging for %s: %w", entry.Name, err)
		}
	}
	return nil
}

func removeInstallPins(entries []installTransactionEntry) error {
	for _, entry := range entries {
		parent, err := openPinnedInstallParent(entry)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("pin installation marker parent for %s: %w", entry.Name, err)
		}
		removeErr := parent.Remove(entry.ParentPin)
		var syncErr error
		if removeErr == nil {
			syncErr = syncRoot(parent)
		}
		closeErr := parent.Close()
		if err := errors.Join(removeErr, syncErr, closeErr); err != nil {
			return fmt.Errorf("remove installation parent marker for %s: %w", entry.Name, err)
		}
	}
	return nil
}

func openPinnedInstallParent(entry installTransactionEntry) (*os.Root, error) {
	root, _, err := openPinnedSource(filepath.Dir(entry.Destination))
	if err != nil {
		return nil, err
	}
	pin, err := root.ReadFile(entry.ParentPin)
	if err != nil || string(pin) != entry.ParentToken {
		_ = root.Close()
		if err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("filesystem installation parent identity changed")
	}
	return root, nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func pathExistsRoot(root *os.Root, path string) (bool, error) {
	_, err := root.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

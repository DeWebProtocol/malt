package backup

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dewebprotocol/malt-client/internal/filelock"
	cid "github.com/ipfs/go-cid"
)

type PlanRootPolicy interface {
	AcceptedRoot(alias string) (cid.Cid, error)
	ObserveCandidate(alias string, candidateRoot, baseRoot cid.Cid, source string) error
	ObserveHead(alias, source, datasetID, branch, commitID string, root cid.Cid, revision uint64) error
	HasCandidate(alias string, candidateRoot, baseRoot cid.Cid) (bool, error)
}

type PlanServiceOptions struct {
	Plan             Plan
	LockPath         string
	Keys             KeySource
	Sync             Sync
	Filesystem       PlanFilesystem
	History          *History
	Roots            PlanRootPolicy
	Protected        []string
	RestoreProtected []string
}

// PlanService publishes and synchronizes one Bucket branch. Different
// bindings occupy different opaque MALT Map tokens, allowing the Gateway to
// merge independent binding updates without learning their path names while
// same-binding changes conflict.
type PlanService struct {
	mu               sync.Mutex
	plan             Plan
	lockPath         string
	keys             KeySource
	sync             Sync
	filesystem       PlanFilesystem
	history          *History
	roots            PlanRootPolicy
	protected        []string
	restoreProtected []string
	release          func() error
}

func NewPlanService(opts PlanServiceOptions) (*PlanService, error) {
	return newPlanService(opts, nil)
}

// NewPlanServiceWithRelease composes one plan service with an owned runtime
// resource release and gives the local runtime deterministic transport cleanup.
func NewPlanServiceWithRelease(opts PlanServiceOptions, release func() error) (*PlanService, error) {
	return newPlanService(opts, release)
}

func newPlanService(opts PlanServiceOptions, release func() error) (*PlanService, error) {
	if err := validatePlan(opts.Plan); err != nil {
		return nil, err
	}
	if opts.LockPath == "" {
		return nil, fmt.Errorf("backup plan lock is required")
	}
	if opts.Keys == nil || opts.Sync == nil || opts.Filesystem == nil || opts.History == nil || opts.Roots == nil {
		return nil, fmt.Errorf("backup plan keys, synchronization, encrypted filesystem, history, and trusted-root policy are required")
	}
	return &PlanService{
		plan: clonePlan(opts.Plan), lockPath: opts.LockPath,
		keys: opts.Keys, sync: opts.Sync, filesystem: opts.Filesystem, history: opts.History,
		roots:            opts.Roots,
		protected:        append([]string(nil), opts.Protected...),
		restoreProtected: append([]string(nil), opts.RestoreProtected...),
		release:          release,
	}, nil
}

// Close releases transport resources owned by this configured service after
// all plan operations have quiesced. It is idempotent.
func (s *PlanService) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.release == nil {
		return nil
	}
	if err := s.release(); err != nil {
		return err
	}
	s.release = nil
	return nil
}

// Recover completes or rolls back interrupted sync/restore transactions before
// the daemon begins accepting new work.
func (s *PlanService) Recover() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	unlock, err := filelock.Acquire(s.lockPath, 10*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	if err := (installer{planID: s.plan.ID}).recoverInstallTransaction(s.syncTransactionPath()); err != nil {
		return err
	}
	return (installer{planID: s.plan.ID}).recoverInstallTransaction(s.restoreTransactionPath())
}

// RecoverPlanTransactions performs startup recovery without constructing
// network, key, or materialization dependencies. Recovery needs only the
// authenticated local journal path and plan identity.
func RecoverPlanTransactions(plan Plan, lockPath string) error {
	if strings.TrimSpace(plan.ID) == "" || lockPath == "" {
		return fmt.Errorf("backup plan ID and operation lock path are required for recovery")
	}
	service := &PlanService{plan: clonePlan(plan), lockPath: lockPath}
	return service.Recover()
}

// RecoverTransactionJournals scans the owner-only Plan history directory so
// branch-only restores that crashed before Plan registration are recovered as
// well as already registered Plans.
func RecoverTransactionJournals(directory string) error {
	if directory == "" {
		return fmt.Errorf("backup transaction journal directory is empty")
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasSuffix(name, ".operation.lock.sync-transaction.json") ||
			strings.HasSuffix(name, ".operation.lock.restore-transaction.json") {
			paths = append(paths, filepath.Join(directory, name))
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := recoverTransactionJournal(path); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}

func recoverTransactionJournal(journalPath string) error {
	suffix := ""
	for _, candidate := range []string{".sync-transaction.json", ".restore-transaction.json"} {
		if strings.HasSuffix(journalPath, candidate) {
			suffix = candidate
			break
		}
	}
	if suffix == "" {
		return fmt.Errorf("unrecognized backup transaction journal")
	}
	transaction, found, err := readInstallTransaction(journalPath)
	if err != nil || !found {
		return err
	}
	lockPath := strings.TrimSuffix(journalPath, suffix)
	service := &PlanService{plan: Plan{ID: transaction.PlanID}, lockPath: lockPath}
	service.mu.Lock()
	defer service.mu.Unlock()
	unlock, err := filelock.Acquire(lockPath, 10*time.Second)
	if err != nil {
		return err
	}
	defer func() { _ = unlock() }()
	return (installer{planID: service.plan.ID}).recoverInstallTransaction(journalPath)
}

// PlanRootAlias is the deterministic cross-device trust-store alias for one
// complete Bucket branch. A Plan is unique per Bucket and writable branch.
func PlanRootAlias(bucketID, branch string) string {
	return "backup:" + strings.TrimSpace(bucketID) + ":" + strings.TrimSpace(branch)
}

func (s *PlanService) syncTransactionPath() string {
	return s.lockPath + ".sync-transaction.json"
}

func (s *PlanService) snapshotDirectory() string {
	return s.lockPath + ".encrypted-snapshots"
}

func (s *PlanService) restoreTransactionPath() string {
	return s.lockPath + ".restore-transaction.json"
}

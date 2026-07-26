package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	clientadd "github.com/dewebprotocol/malt-client/application/add"
	"github.com/dewebprotocol/malt-client/bucketsync"
	"github.com/dewebprotocol/malt-client/unixfs"
	cid "github.com/ipfs/go-cid"
)

type fixedKeys struct {
	epoch uint32
	key   [32]byte
}

func (k fixedKeys) ActiveEpoch() uint32 { return k.epoch }
func (k fixedKeys) BucketKey(uint32, string) ([32]byte, error) {
	return k.key, nil
}

type fakeSync struct {
	workspace bucketsync.Workspace
	order     *[]string
	onStatus  func()
	message   string
	pushErrs  []error
	nextStash int
}

func (s *fakeSync) Pull(context.Context) (bucketsync.Workspace, error) {
	*s.order = append(*s.order, "pull")
	s.workspace.Initialized = true
	return s.workspace, nil
}
func (s *fakeSync) Status() (bucketsync.Workspace, error) {
	*s.order = append(*s.order, "status")
	if s.onStatus != nil {
		s.onStatus()
	}
	return s.workspace, nil
}
func (s *fakeSync) CurrentBase(cid.Cid) (bucketsync.Head, error) {
	*s.order = append(*s.order, "current")
	return s.workspace.Base, nil
}
func (s *fakeSync) Stage(candidate cid.Cid, base bucketsync.Head, _ cid.Cid, message string) (bucketsync.Stash, error) {
	*s.order = append(*s.order, "stage")
	s.message = message
	for _, stash := range s.workspace.Stashes {
		if stash.CandidateRoot == candidate.String() && stash.Status == "pending" {
			return stash, nil
		}
	}
	s.nextStash++
	stash := bucketsync.Stash{
		ID: fmt.Sprintf("stash-%d", s.nextStash), PushID: fmt.Sprintf("push-%d", s.nextStash),
		CandidateRoot: candidate.String(), Base: base, Message: message, Status: "pending",
	}
	s.workspace.Stashes = append(s.workspace.Stashes, stash)
	return stash, nil
}
func (s *fakeSync) RestorePending(stash bucketsync.Stash) (bucketsync.Stash, error) {
	*s.order = append(*s.order, "restore-pending")
	if !s.workspace.Initialized {
		return bucketsync.Stash{}, bucketsync.ErrNotInitialized
	}
	for _, existing := range s.workspace.Stashes {
		if existing.ID == stash.ID {
			return existing, nil
		}
	}
	s.workspace.Stashes = append(s.workspace.Stashes, stash)
	return stash, nil
}
func (s *fakeSync) Push(_ context.Context, candidate cid.Cid, _ cid.Cid, _ string) (bucketsync.PushOutcome, error) {
	*s.order = append(*s.order, "push")
	if len(s.pushErrs) != 0 {
		err := s.pushErrs[0]
		s.pushErrs = s.pushErrs[1:]
		if err != nil {
			return bucketsync.PushOutcome{}, err
		}
	}
	for i, stash := range s.workspace.Stashes {
		if stash.CandidateRoot == candidate.String() && stash.Status == "pending" {
			s.workspace.Stashes = append(s.workspace.Stashes[:i], s.workspace.Stashes[i+1:]...)
			break
		}
	}
	return bucketsync.PushOutcome{Workspace: s.workspace}, nil
}

type inspectingMaterializer struct {
	order       *[]string
	key         [32]byte
	restoredDir string
}

func (m inspectingMaterializer) MaterializeBackup(ctx context.Context, archive string, _ cid.Cid) (*clientadd.Result, error) {
	*m.order = append(*m.order, "materialize")
	if filepath.Base(archive) != "snapshot" {
		return nil, os.ErrInvalid
	}
	if err := restoreArchive(ctx, archive, m.restoredDir, func(uint32) ([32]byte, error) {
		return m.key, nil
	}, false); err != nil {
		return nil, err
	}
	return &clientadd.Result{NewRoot: "bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"}, nil
}

func TestBackupSnapshotsBeforeRemoteObservationAndStagesBeforePush(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(source, []byte("before remote observation"), 0o600); err != nil {
		t.Fatal(err)
	}
	var order []string
	key := [32]byte{9, 8, 7}
	syncer := &fakeSync{
		workspace: bucketsync.Workspace{Initialized: true},
		order:     &order,
		onStatus: func() {
			if err := os.WriteFile(source, []byte("after remote observation"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	restored := t.TempDir()
	history, err := NewHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Options{
		BucketID: "bucket-a", TempDir: t.TempDir(), Keys: fixedKeys{epoch: 1, key: key},
		LockPath: filepath.Join(t.TempDir(), "backup.lock"),
		Sync:     syncer, History: history,
		Materializer: inspectingMaterializer{order: &order, key: key, restoredDir: restored},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Run(context.Background(), Request{Source: source})
	if err != nil {
		t.Fatal(err)
	}
	if result.CandidateRoot == "" || result.RemotePath != RemotePath {
		t.Fatalf("result = %#v", result)
	}
	got, err := os.ReadFile(filepath.Join(restored, "source.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "before remote observation" {
		t.Fatalf("snapshot body = %q", got)
	}
	if syncer.message != "encrypted backup snapshot" {
		t.Fatalf("default remote message leaks source information: %q", syncer.message)
	}
	wantOrder := []string{"status", "current", "materialize", "stage", "push"}
	if len(order) != len(wantOrder) {
		t.Fatalf("order = %v", order)
	}
	for i := range wantOrder {
		if order[i] != wantOrder[i] {
			t.Fatalf("order = %v, want %v", order, wantOrder)
		}
	}
}

func TestBackupFallsBackWhenConfiguredStagingIsInsideSource(t *testing.T) {
	source := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "document.txt"), []byte("home data"), 0o600); err != nil {
		t.Fatal(err)
	}
	var order []string
	key := [32]byte{8, 6, 7}
	history, err := NewHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	syncer := &fakeSync{workspace: bucketsync.Workspace{Initialized: true}, order: &order}
	restored := t.TempDir()
	service, err := NewService(Options{
		BucketID: "bucket-a", TempDir: filepath.Join(source, ".malt-client", "staging"),
		LockPath: filepath.Join(t.TempDir(), "backup.lock"), Keys: fixedKeys{epoch: 1, key: key},
		Sync: syncer, History: history,
		Materializer: inspectingMaterializer{order: &order, key: key, restoredDir: restored},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Run(context.Background(), Request{Source: source}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(restored, "home", "document.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "home data" {
		t.Fatalf("restored home data = %q", got)
	}
	if _, err := os.Stat(filepath.Join(restored, "home", ".malt-client")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("in-source staging leaked into snapshot: %v", err)
	}
}

func TestBackupRejectsSourceContainingClientKeysOrState(t *testing.T) {
	source := filepath.Join(t.TempDir(), "home")
	protected := filepath.Join(source, ".malt-client", "backup-keys.json")
	if err := os.MkdirAll(filepath.Dir(protected), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(protected, []byte("master key"), 0o600); err != nil {
		t.Fatal(err)
	}
	var order []string
	history, err := NewHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Options{
		BucketID: "bucket-a", TempDir: t.TempDir(), LockPath: filepath.Join(t.TempDir(), "backup.lock"),
		Keys: fixedKeys{epoch: 1}, Sync: &fakeSync{workspace: bucketsync.Workspace{Initialized: true}, order: &order},
		History: history, Protected: []string{protected},
		Materializer: inspectingMaterializer{order: &order, restoredDir: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Run(context.Background(), Request{Source: source}); !errors.Is(err, ErrProtectedSource) {
		t.Fatalf("protected source error = %v", err)
	}
	if len(order) != 0 {
		t.Fatalf("protected source reached Bucket operations: %v", order)
	}
}

func TestValidateSourceResolvesProtectedSymlinkIntoSource(t *testing.T) {
	source := filepath.Join(t.TempDir(), "home")
	if err := os.MkdirAll(source, 0o700); err != nil {
		t.Fatal(err)
	}
	keyring := filepath.Join(source, "backup-keys.json")
	if err := os.WriteFile(keyring, []byte("master key"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "keyring-link")
	if err := os.Symlink(keyring, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := ValidateSource(source, []string{alias}); !errors.Is(err, ErrProtectedSource) {
		t.Fatalf("resolved protected source error = %v", err)
	}
}

func TestSelectStagingRootRejectsSourceContainingSystemTemp(t *testing.T) {
	configured := filepath.Join(os.TempDir(), "configured")
	if _, err := selectStagingRoot(os.TempDir(), configured); err == nil {
		t.Fatal("staging selection accepted source containing all available staging roots")
	}
}

func TestSelectStagingRootResolvesSymlinkIntoSource(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source")
	actual := filepath.Join(source, "staging")
	if err := os.MkdirAll(actual, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "staging-link")
	if err := os.Symlink(actual, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	selected, err := selectStagingRoot(source, link)
	if err != nil {
		t.Fatal(err)
	}
	if selected != os.TempDir() {
		t.Fatalf("selected staging = %q, want system temp", selected)
	}
}

func TestBackupRetriesJournaledCandidateWithoutCreatingAnotherSnapshot(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(source, []byte("snapshot once"), 0o600); err != nil {
		t.Fatal(err)
	}
	var order []string
	key := [32]byte{5, 4, 3}
	syncer := &fakeSync{
		workspace: bucketsync.Workspace{Initialized: true},
		order:     &order,
		pushErrs:  []error{errors.New("response lost"), nil},
	}
	history, err := NewHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(Options{
		BucketID: "bucket-a", TempDir: t.TempDir(), Keys: fixedKeys{epoch: 1, key: key},
		LockPath: filepath.Join(t.TempDir(), "backup.lock"), Sync: syncer, History: history,
		Materializer: inspectingMaterializer{order: &order, key: key, restoredDir: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := service.Run(context.Background(), Request{Source: source, JobName: "daily"})
	if err == nil || first == nil {
		t.Fatalf("first backup = %#v, %v; want staged result and push error", first, err)
	}
	pending, err := history.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if pending == nil || pending.Result.CandidateRoot != first.CandidateRoot || pending.JobName != "daily" {
		t.Fatalf("pending = %#v, want first candidate", pending)
	}
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	second, err := service.Run(context.Background(), Request{Source: source, JobName: "daily"})
	if err != nil {
		t.Fatal(err)
	}
	if !second.RetriedPending || second.CandidateRoot != first.CandidateRoot {
		t.Fatalf("retry = %#v, want exact pending candidate", second)
	}
	pending, err = history.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if pending != nil {
		t.Fatalf("pending journal was not cleared: %#v", pending)
	}
	states, err := history.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if states["daily"].LastResult == nil || states["daily"].LastResult.CandidateRoot != first.CandidateRoot ||
		states["daily"].AttemptActive {
		t.Fatalf("atomic scheduled completion = %#v", states["daily"])
	}
	wantOrder := []string{"status", "current", "materialize", "stage", "push", "status", "push"}
	if len(order) != len(wantOrder) {
		t.Fatalf("order = %v, want %v", order, wantOrder)
	}
	for i := range wantOrder {
		if order[i] != wantOrder[i] {
			t.Fatalf("order = %v, want %v", order, wantOrder)
		}
	}
}

func TestBackupRestoresExactJournaledPushIdentityWhenWorkspaceStashIsMissing(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.txt")
	candidate := "bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"
	history, err := NewHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	pending := PendingBackup{
		BucketID: "bucket-a", Message: "encrypted backup snapshot",
		Result: Result{
			Source: source, CandidateRoot: candidate, SourceFingerprint: "sha256:original",
		},
		CreatedAt: time.Now().UTC(),
	}
	if err := history.SetPending(pending); err != nil {
		t.Fatal(err)
	}
	if err := history.MarkPendingStaged(candidate, bucketsync.Stash{
		ID: "stash-original", PushID: "push-original", CandidateRoot: candidate,
		Message: pending.Message, Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	var order []string
	syncer := &fakeSync{workspace: bucketsync.Workspace{Initialized: true}, order: &order}
	service, err := NewService(Options{
		BucketID: "bucket-a", TempDir: t.TempDir(), LockPath: filepath.Join(t.TempDir(), "backup.lock"),
		Keys: fixedKeys{epoch: 1}, Sync: syncer, History: history,
		Materializer: inspectingMaterializer{order: &order, restoredDir: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Run(context.Background(), Request{Source: source})
	if err != nil {
		t.Fatal(err)
	}
	if !result.RetriedPending || result.ReconciledPending || result.CandidateRoot != candidate {
		t.Fatalf("recovered result = %#v", result)
	}
	wantOrder := []string{"status", "restore-pending", "push"}
	if len(order) != len(wantOrder) {
		t.Fatalf("recovery order = %v, want %v", order, wantOrder)
	}
	for i := range wantOrder {
		if order[i] != wantOrder[i] {
			t.Fatalf("recovery order = %v, want %v", order, wantOrder)
		}
	}
	left, err := history.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if left != nil {
		t.Fatalf("recovered journal remains: %#v", left)
	}
}

func TestBackupDoesNotRestoreMissingStashIntoUninitializedWorkspace(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.txt")
	candidate := "bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"
	history, err := NewHistory(filepath.Join(t.TempDir(), "history.json"))
	if err != nil {
		t.Fatal(err)
	}
	pending := PendingBackup{
		BucketID: "bucket-a", Message: "encrypted backup snapshot",
		Result:    Result{Source: source, CandidateRoot: candidate},
		CreatedAt: time.Now().UTC(),
	}
	if err := history.SetPending(pending); err != nil {
		t.Fatal(err)
	}
	if err := history.MarkPendingStaged(candidate, bucketsync.Stash{
		ID: "stash-original", PushID: "push-original", CandidateRoot: candidate,
		Message: pending.Message, Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	var order []string
	service, err := NewService(Options{
		BucketID: "bucket-a", TempDir: t.TempDir(), LockPath: filepath.Join(t.TempDir(), "backup.lock"),
		Keys: fixedKeys{epoch: 1}, Sync: &fakeSync{order: &order}, History: history,
		Materializer: inspectingMaterializer{order: &order, restoredDir: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Run(context.Background(), Request{Source: source}); !errors.Is(err, ErrPendingWorkspace) {
		t.Fatalf("uninitialized recovery error = %v", err)
	}
	left, err := history.Pending()
	if err != nil {
		t.Fatal(err)
	}
	if left == nil {
		t.Fatal("uninitialized recovery cleared the pending journal")
	}
	if len(order) != 1 || order[0] != "status" {
		t.Fatalf("uninitialized recovery operations = %v", order)
	}
}

func TestPendingStashFailsClosedOnIdentityOrBaseConflict(t *testing.T) {
	candidate := "bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"
	base := bucketsync.Head{CommitID: "commit-1", Root: candidate, Revision: 1}
	pending := PendingBackup{
		Message: "encrypted backup snapshot", StashID: "stash-1", PushID: "push-1",
		Result: Result{CandidateRoot: candidate, Base: base},
	}
	if _, _, err := pendingStash(bucketsync.Workspace{Stashes: []bucketsync.Stash{{
		ID: "stash-1", PushID: "different", CandidateRoot: candidate,
		Base: base, Message: pending.Message, Status: "pending",
	}}}, pending, candidate); err == nil {
		t.Fatal("pending stash accepted a conflicting push identity")
	}
	pending.StashID, pending.PushID = "", ""
	if _, _, err := pendingStash(bucketsync.Workspace{Stashes: []bucketsync.Stash{{
		ID: "stash-2", PushID: "push-2", CandidateRoot: candidate,
		Base: bucketsync.Head{}, Message: pending.Message, Status: "pending",
	}}}, pending, candidate); err == nil {
		t.Fatal("pending journal adopted a same-root stash from a different base")
	}
}

type archiveReader struct {
	body []byte
	root cid.Cid
}

func (r *archiveReader) Resolve(context.Context, cid.Cid, string) (*unixfs.Resolution, error) {
	return nil, os.ErrInvalid
}
func (r *archiveReader) Stat(_ context.Context, root cid.Cid, path string) (*unixfs.Stat, error) {
	if !root.Equals(r.root) || path != RemotePath {
		return nil, os.ErrInvalid
	}
	return &unixfs.Stat{Kind: unixfs.StagedKindFile, Size: uint64(len(r.body))}, nil
}
func (r *archiveReader) ReadFile(context.Context, cid.Cid, string) (*unixfs.ReadResult, error) {
	return nil, os.ErrInvalid
}
func (r *archiveReader) ReadFileRange(_ context.Context, root cid.Cid, path string, offset, length uint64) (*unixfs.ReadResult, error) {
	if !root.Equals(r.root) || path != RemotePath || offset+length > uint64(len(r.body)) {
		return nil, os.ErrInvalid
	}
	return &unixfs.ReadResult{
		Body:   append([]byte(nil), r.body[offset:offset+length]...),
		Offset: offset, End: offset + length, TotalSize: uint64(len(r.body)),
	}, nil
}
func (r *archiveReader) ReadListPayloadRange(context.Context, cid.Cid, uint64, uint64) (*unixfs.ReadResult, error) {
	return nil, os.ErrInvalid
}

func TestRestoreDownloadsVerifiedRangesBeforeDecrypting(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(source, []byte("verified restore"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := [32]byte{3, 1, 4}
	archive := filepath.Join(t.TempDir(), "snapshot")
	if _, err := CreateArchive(context.Background(), source, archive, 2, key); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	root, err := cid.Parse("bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku")
	if err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := restoreVerified(context.Background(), &archiveReader{body: body, root: root}, RestoreOptions{
		TrustedRoot: root,
		Destination: destination, TempDir: t.TempDir(), BucketID: "bucket-a",
		Keys: fixedKeys{epoch: 2, key: key},
	}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(destination, "source.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "verified restore" {
		t.Fatalf("restored body = %q", got)
	}
}

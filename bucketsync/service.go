// Package bucketsync owns durable client-side Bucket synchronization state.
// It records local candidates before observing a remote head and never promotes
// a Gateway head into the separate trusted-root store.
package bucketsync

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dewebprotocol/malt-client/internal/securefile"
	"github.com/dewebprotocol/malt-client/transport"
	cid "github.com/ipfs/go-cid"
)

var (
	ErrNotInitialized = errors.New("Bucket workspace is not initialized; pull the Bucket head before producing local work")
	ErrNotStaged      = errors.New("Bucket candidate is not staged with its original base")
)

const bucketWorkspaceVersion = 3

type Gateway interface {
	BucketHead(context.Context) (*transport.BucketRef, error)
	PushBucket(context.Context, transport.BucketPushRequest) (*transport.BucketPushResult, error)
}

type Head struct {
	CommitID string `json:"commit_id,omitempty"`
	Root     string `json:"root,omitempty"`
	Revision uint64 `json:"revision"`
}

type Conflict struct {
	Coordinate string `json:"coordinate"`
	Base       string `json:"base,omitempty"`
	Local      string `json:"local,omitempty"`
	Remote     string `json:"remote,omitempty"`
}

type Stash struct {
	ID            string     `json:"id"`
	PushID        string     `json:"push_id"`
	CandidateRoot string     `json:"candidate_root"`
	Base          Head       `json:"base"`
	ChangeSetCID  string     `json:"change_set_cid,omitempty"`
	Message       string     `json:"message,omitempty"`
	RequestFrozen bool       `json:"request_frozen"`
	Status        string     `json:"status"`
	Branch        string     `json:"branch,omitempty"`
	Conflicts     []Conflict `json:"conflicts,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

type Workspace struct {
	BucketID    string    `json:"bucket_id"`
	Branch      string    `json:"branch"`
	Initialized bool      `json:"initialized"`
	Base        Head      `json:"base"`
	Remote      Head      `json:"remote"`
	Stashes     []Stash   `json:"stashes,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type PushOutcome struct {
	Result    transport.BucketPushResult `json:"result"`
	Workspace Workspace                  `json:"workspace"`
}

type persistedState struct {
	Version    int                  `json:"version"`
	Workspaces map[string]Workspace `json:"workspaces"`
}

type Service struct {
	mu       sync.Mutex
	path     string
	gateway  Gateway
	bucketID string
	branch   string
	stateKey string
	state    persistedState
}

func Open(path string, gateway Gateway, bucketID string) (*Service, error) {
	return OpenBranch(path, gateway, bucketID, "main")
}

// OpenBranch opens synchronization state for one writable Bucket branch.
func OpenBranch(path string, gateway Gateway, bucketID, branch string) (*Service, error) {
	path = strings.TrimSpace(path)
	bucketID = strings.TrimSpace(bucketID)
	branch, err := normalizeBranch(branch)
	if err != nil {
		return nil, err
	}
	if path == "" || gateway == nil || bucketID == "" {
		return nil, fmt.Errorf("Bucket sync path, Gateway, Bucket ID, and branch are required")
	}
	service := &Service{
		path: path, gateway: gateway, bucketID: bucketID, branch: branch,
		stateKey: workspaceKey(bucketID, branch),
	}
	if err := service.withState(false, func() error { return nil }); err != nil {
		return nil, err
	}
	return service, nil
}

// Pull observes the latest Gateway head. When pending local stashes exist it
// updates Remote only; their recorded Base is never overwritten.
func (s *Service) Pull(ctx context.Context) (Workspace, error) {
	head, err := s.gateway.BucketHead(ctx)
	if err != nil {
		return Workspace{}, err
	}
	if head == nil {
		return Workspace{}, fmt.Errorf("gateway returned an empty Bucket head response")
	}
	if err := transport.ValidateBucketHeadForBranch(s.bucketID, s.branch, *head); err != nil {
		return Workspace{}, err
	}
	remote, err := headFromRef(*head)
	if err != nil {
		return Workspace{}, err
	}
	var result Workspace
	err = s.withState(true, func() error {
		workspace := s.workspace()
		workspace.Initialized = true
		remote, err = monotonicHead(workspace.Remote, remote)
		if err != nil {
			return fmt.Errorf("merge observed Bucket head: %w", err)
		}
		workspace.Remote = remote
		if !hasPending(workspace.Stashes) {
			workspace.Base, err = monotonicHead(workspace.Base, remote)
			if err != nil {
				return fmt.Errorf("merge Bucket base: %w", err)
			}
		}
		workspace.UpdatedAt = time.Now().UTC()
		s.state.Workspaces[s.stateKey] = workspace
		result = cloneWorkspace(workspace)
		return nil
	})
	return result, err
}

// CurrentBase captures the base metadata before a caller starts materializing
// a candidate. The requested root must be the currently recorded base.
func (s *Service) CurrentBase(baseRoot cid.Cid) (Head, error) {
	want := ""
	if baseRoot.Defined() {
		want = baseRoot.String()
	}
	var result Head
	err := s.withState(false, func() error {
		workspace := s.workspace()
		if !workspace.Initialized {
			return ErrNotInitialized
		}
		if workspace.Base.Root != want {
			return fmt.Errorf("candidate base root %q does not match staged workspace base %q", want, workspace.Base.Root)
		}
		result = workspace.Base
		return nil
	})
	return result, err
}

// Stage durably binds a materialized candidate to the base captured before
// its creation. Pull never rewrites this record.
func (s *Service) Stage(candidateRoot cid.Cid, base Head, changeSet cid.Cid, message string) (Stash, error) {
	if !candidateRoot.Defined() {
		return Stash{}, fmt.Errorf("candidate root is undefined")
	}
	if err := validateHead(base); err != nil {
		return Stash{}, fmt.Errorf("candidate base: %w", err)
	}
	var stash Stash
	if err := s.withState(true, func() error {
		workspace := s.workspace()
		if !workspace.Initialized {
			return ErrNotInitialized
		}
		for _, existing := range workspace.Stashes {
			if existing.Status == "pending" && existing.CandidateRoot == candidateRoot.String() {
				if existing.Base != base {
					return fmt.Errorf("candidate is already staged against a different base")
				}
				stash = existing
				return nil
			}
		}
		id, err := randomID()
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		stash = Stash{
			ID: id, PushID: "push_" + id, CandidateRoot: candidateRoot.String(), Base: base,
			Message: strings.TrimSpace(message), Status: "pending", CreatedAt: now, UpdatedAt: now,
		}
		if changeSet.Defined() {
			stash.ChangeSetCID = changeSet.String()
		}
		workspace.Stashes = append(workspace.Stashes, stash)
		workspace.UpdatedAt = now
		s.state.Workspaces[s.stateKey] = workspace
		return nil
	}); err != nil {
		return Stash{}, err
	}
	return stash, nil
}

// RestorePending reinstates an exact frozen stash from a separate durable
// journal. It never generates a new stash or push ID and is intended only for
// crash recovery before retrying Push.
func (s *Service) RestorePending(stash Stash) (Stash, error) {
	if stash.ID == "" || stash.PushID == "" || stash.Status != "pending" || !stash.RequestFrozen {
		return Stash{}, fmt.Errorf("restored Bucket stash identity is incomplete")
	}
	if _, err := cid.Parse(stash.CandidateRoot); err != nil {
		return Stash{}, fmt.Errorf("restored Bucket stash candidate: %w", err)
	}
	if err := validateHead(stash.Base); err != nil {
		return Stash{}, fmt.Errorf("restored Bucket stash base: %w", err)
	}
	if stash.ChangeSetCID != "" {
		if _, err := cid.Parse(stash.ChangeSetCID); err != nil {
			return Stash{}, fmt.Errorf("restored Bucket stash change set: %w", err)
		}
	}
	stash.Message = strings.TrimSpace(stash.Message)
	if stash.CreatedAt.IsZero() {
		return Stash{}, fmt.Errorf("restored Bucket stash creation time is missing")
	}
	var result Stash
	if err := s.withState(true, func() error {
		workspace := s.workspace()
		if !workspace.Initialized {
			return ErrNotInitialized
		}
		for _, existing := range workspace.Stashes {
			if existing.ID == stash.ID {
				if !sameFrozenStash(existing, stash) {
					return fmt.Errorf("restored Bucket stash conflicts with local stash %s", stash.ID)
				}
				result = existing
				return nil
			}
			if existing.Status == "pending" && existing.CandidateRoot == stash.CandidateRoot {
				return fmt.Errorf("restored Bucket stash candidate already has a different pending identity")
			}
		}
		stash.UpdatedAt = time.Now().UTC()
		workspace.Stashes = append(workspace.Stashes, stash)
		workspace.UpdatedAt = stash.UpdatedAt
		s.state.Workspaces[s.stateKey] = workspace
		result = stash
		return nil
	}); err != nil {
		return Stash{}, err
	}
	return result, nil
}

func sameFrozenStash(existing, restored Stash) bool {
	return existing.ID == restored.ID &&
		existing.PushID == restored.PushID &&
		existing.CandidateRoot == restored.CandidateRoot &&
		existing.Base == restored.Base &&
		existing.ChangeSetCID == restored.ChangeSetCID &&
		existing.Message == restored.Message &&
		existing.RequestFrozen &&
		existing.Status == "pending"
}

// Push submits a previously staged candidate. It never infers a base from the
// workspace at push time. A failed fetch or push leaves the stash pending for
// a later retry with the same push ID.
func (s *Service) Push(ctx context.Context, candidateRoot cid.Cid, changeSet cid.Cid, message string) (PushOutcome, error) {
	if !candidateRoot.Defined() {
		return PushOutcome{}, fmt.Errorf("candidate root is undefined")
	}
	var stash Stash
	if err := s.withState(true, func() error {
		workspace := s.workspace()
		if !workspace.Initialized {
			return ErrNotInitialized
		}
		found := false
		for i := range workspace.Stashes {
			if workspace.Stashes[i].Status != "pending" || workspace.Stashes[i].CandidateRoot != candidateRoot.String() {
				continue
			}
			found = true
			requestedChangeSet := ""
			if changeSet.Defined() {
				requestedChangeSet = changeSet.String()
			}
			requestedMessage := strings.TrimSpace(message)
			if workspace.Stashes[i].RequestFrozen {
				if requestedChangeSet != "" && requestedChangeSet != workspace.Stashes[i].ChangeSetCID {
					return fmt.Errorf("Bucket retry change set differs from the persisted push request")
				}
				if requestedMessage != "" && requestedMessage != workspace.Stashes[i].Message {
					return fmt.Errorf("Bucket retry message differs from the persisted push request")
				}
			} else {
				if requestedChangeSet != "" {
					workspace.Stashes[i].ChangeSetCID = requestedChangeSet
				}
				if requestedMessage != "" {
					workspace.Stashes[i].Message = requestedMessage
				}
				workspace.Stashes[i].RequestFrozen = true
			}
			workspace.Stashes[i].UpdatedAt = time.Now().UTC()
			stash = workspace.Stashes[i]
			break
		}
		if !found {
			return ErrNotStaged
		}
		workspace.UpdatedAt = time.Now().UTC()
		s.state.Workspaces[s.stateKey] = workspace
		return nil
	}); err != nil {
		return PushOutcome{}, err
	}

	// The candidate and original base are already durable. Fetching cannot
	// erase its base even when another client has advanced main.
	if _, err := s.Pull(ctx); err != nil {
		return PushOutcome{}, err
	}
	request := transport.BucketPushRequest{
		PushID: stash.PushID, Branch: s.branch, BaseCommit: stash.Base.CommitID, BaseRoot: stash.Base.Root,
		CandidateRoot: stash.CandidateRoot, BaseRevision: stash.Base.Revision,
		ChangeSetCID: stash.ChangeSetCID, Message: stash.Message,
	}
	result, err := s.gateway.PushBucket(ctx, request)
	if err != nil {
		return PushOutcome{}, err
	}
	if result == nil {
		return PushOutcome{}, fmt.Errorf("gateway returned an empty Bucket push response")
	}
	if err := transport.ValidateBucketPushResult(s.bucketID, request, *result); err != nil {
		return PushOutcome{}, err
	}
	var workspace Workspace
	if err := s.withState(true, func() error {
		current := s.workspace()
		for i := range current.Stashes {
			if current.Stashes[i].ID != stash.ID {
				continue
			}
			if result.Status == "branched" {
				current.Stashes[i].Status = "branched"
				if result.Branch != nil {
					current.Stashes[i].Branch = result.Branch.Name
				}
				current.Stashes[i].Conflicts = conflictsFromTransport(result.Conflicts)
				current.Stashes[i].UpdatedAt = time.Now().UTC()
			} else {
				current.Stashes = append(current.Stashes[:i], current.Stashes[i+1:]...)
			}
			break
		}
		head, err := headFromRef(result.Head)
		if err != nil {
			return err
		}
		current.Remote, err = monotonicHead(current.Remote, head)
		if err != nil {
			return fmt.Errorf("merge pushed Bucket head: %w", err)
		}
		if !hasPending(current.Stashes) {
			current.Base, err = monotonicHead(current.Base, current.Remote)
			if err != nil {
				return fmt.Errorf("merge pushed Bucket base: %w", err)
			}
		}
		current.UpdatedAt = time.Now().UTC()
		s.state.Workspaces[s.stateKey] = current
		workspace = cloneWorkspace(current)
		return nil
	}); err != nil {
		return PushOutcome{}, err
	}
	return PushOutcome{Result: *result, Workspace: workspace}, nil
}

func (s *Service) Status() (Workspace, error) {
	var result Workspace
	err := s.withState(false, func() error {
		result = cloneWorkspace(s.workspace())
		return nil
	})
	return result, err
}

// ResolveBranched removes one exact conflict stash after the trusted client has
// preserved or explicitly resolved its local candidate. The conflict branch
// remains on the Gateway; this only unlocks the local workspace and advances
// its materialization base to the latest observed target head.
func (s *Service) ResolveBranched(stashID, candidateRoot string) (Workspace, error) {
	stashID = strings.TrimSpace(stashID)
	candidateRoot = strings.TrimSpace(candidateRoot)
	if stashID == "" {
		return Workspace{}, fmt.Errorf("branched stash ID is empty")
	}
	if _, err := cid.Parse(candidateRoot); err != nil {
		return Workspace{}, fmt.Errorf("branched stash candidate: %w", err)
	}
	var result Workspace
	err := s.withState(true, func() error {
		workspace := s.workspace()
		found := false
		for i, stash := range workspace.Stashes {
			if stash.ID != stashID {
				continue
			}
			if stash.Status != "branched" || stash.CandidateRoot != candidateRoot {
				return fmt.Errorf("Bucket stash %s is not the selected branched candidate", stashID)
			}
			workspace.Stashes = append(workspace.Stashes[:i], workspace.Stashes[i+1:]...)
			found = true
			break
		}
		if !found {
			return fmt.Errorf("branched Bucket stash %s was not found", stashID)
		}
		if !hasPending(workspace.Stashes) {
			var err error
			workspace.Base, err = monotonicHead(workspace.Base, workspace.Remote)
			if err != nil {
				return fmt.Errorf("advance resolved Bucket base: %w", err)
			}
		}
		workspace.UpdatedAt = time.Now().UTC()
		s.state.Workspaces[s.stateKey] = workspace
		result = cloneWorkspace(workspace)
		return nil
	})
	return result, err
}

func (s *Service) workspace() Workspace {
	value := s.state.Workspaces[s.stateKey]
	value.BucketID = s.bucketID
	value.Branch = s.branch
	return value
}

func (s *Service) withState(write bool, operation func() error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	unlock, err := acquireLock(s.path + ".lock")
	if err != nil {
		return fmt.Errorf("lock Bucket workspace: %w", err)
	}
	defer func() { _ = unlock() }()
	migrated, err := s.reload()
	if err != nil {
		return err
	}
	// Version 1 could not distinguish a never-sent stash from one whose
	// response was lost. Persist the conservative freeze before any operation
	// can retry or alter its request fingerprint.
	if migrated {
		if err := s.write(); err != nil {
			return fmt.Errorf("migrate Bucket workspace: %w", err)
		}
	}
	if err := operation(); err != nil {
		return err
	}
	if !write {
		return nil
	}
	return s.write()
}

func (s *Service) reload() (bool, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.state = persistedState{Version: bucketWorkspaceVersion, Workspaces: map[string]Workspace{}}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := securefile.Secure(s.path); err != nil {
		return false, fmt.Errorf("protect Bucket workspace: %w", err)
	}
	var state persistedState
	if err := json.Unmarshal(data, &state); err != nil {
		return false, fmt.Errorf("decode Bucket workspace: %w", err)
	}
	if state.Workspaces == nil {
		state.Workspaces = map[string]Workspace{}
	}
	migrated := false
	switch state.Version {
	case 1:
		// Version 1 omitted RequestFrozen. Treat every pending request as if
		// it may already have reached Gateway, preserving its exact fingerprint.
		for id, workspace := range state.Workspaces {
			for i := range workspace.Stashes {
				if workspace.Stashes[i].Status == "pending" {
					workspace.Stashes[i].RequestFrozen = true
				}
			}
			state.Workspaces[id] = workspace
		}
		state.Workspaces = migrateMainWorkspaces(state.Workspaces)
		state.Version = bucketWorkspaceVersion
		migrated = true
	case 2:
		if err := validateVersionTwoFreezeFields(data); err != nil {
			return false, err
		}
		state.Workspaces = migrateMainWorkspaces(state.Workspaces)
		state.Version = bucketWorkspaceVersion
		migrated = true
	case bucketWorkspaceVersion:
	default:
		return false, fmt.Errorf("unsupported Bucket workspace version %d", state.Version)
	}
	for id, workspace := range state.Workspaces {
		branch, err := normalizeBranch(workspace.Branch)
		if err != nil || workspaceKey(workspace.BucketID, branch) != id {
			return false, fmt.Errorf("Bucket workspace key does not match record")
		}
		workspace.Branch = branch
		if err := validateHead(workspace.Base); err != nil {
			return false, fmt.Errorf("Bucket %s base: %w", id, err)
		}
		if err := validateHead(workspace.Remote); err != nil {
			return false, fmt.Errorf("Bucket %s remote: %w", id, err)
		}
		for _, stash := range workspace.Stashes {
			if stash.ID == "" || stash.PushID == "" || (stash.Status != "pending" && stash.Status != "branched") {
				return false, fmt.Errorf("Bucket %s has an invalid stash", id)
			}
			if _, err := cid.Parse(stash.CandidateRoot); err != nil {
				return false, fmt.Errorf("Bucket %s stash root: %w", id, err)
			}
			if err := validateHead(stash.Base); err != nil {
				return false, fmt.Errorf("Bucket %s stash base: %w", id, err)
			}
		}
	}
	s.state = state
	return migrated, nil
}

func workspaceKey(bucketID, branch string) string {
	return bucketID + "@" + base64.RawURLEncoding.EncodeToString([]byte(branch))
}

func migrateMainWorkspaces(values map[string]Workspace) map[string]Workspace {
	next := make(map[string]Workspace, len(values))
	for _, workspace := range values {
		workspace.Branch = "main"
		next[workspaceKey(workspace.BucketID, workspace.Branch)] = workspace
	}
	return next
}

func normalizeBranch(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "main" {
		return "main", nil
	}
	raw = strings.TrimPrefix(raw, "heads/")
	if raw == "" || raw == "main" || strings.HasPrefix(raw, "conflicts/") ||
		strings.Contains(raw, "/") || strings.Contains(raw, "\\") ||
		strings.ContainsAny(raw, " \t\r\n") {
		return "", fmt.Errorf("invalid Bucket branch %q", raw)
	}
	return raw, nil
}

func validateVersionTwoFreezeFields(data []byte) error {
	var raw struct {
		Workspaces map[string]struct {
			Stashes []json.RawMessage `json:"stashes"`
		} `json:"workspaces"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("decode Bucket workspace freeze metadata: %w", err)
	}
	for id, workspace := range raw.Workspaces {
		for i, stash := range workspace.Stashes {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(stash, &fields); err != nil {
				return fmt.Errorf("Bucket %s stash %d freeze metadata: %w", id, i, err)
			}
			encoded, ok := fields["request_frozen"]
			if !ok {
				return fmt.Errorf("Bucket %s stash %d lacks explicit request_frozen", id, i)
			}
			var frozen *bool
			if err := json.Unmarshal(encoded, &frozen); err != nil || frozen == nil {
				return fmt.Errorf("Bucket %s stash %d has invalid request_frozen", id, i)
			}
		}
	}
	return nil
}

func (s *Service) write() error {
	data, err := json.MarshalIndent(s.state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".buckets-*.json")
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
	if err := os.Rename(name, s.path); err != nil {
		return err
	}
	if err := securefile.Secure(s.path); err != nil {
		return fmt.Errorf("protect Bucket workspace: %w", err)
	}
	// The rename is already committed from the caller's perspective. Directory
	// sync is best-effort because some supported filesystems reject it, and an
	// error after rename would make a successful state transition ambiguous.
	_ = syncParentDirectory(filepath.Dir(s.path))
	return nil
}

func headFromRef(value transport.BucketRef) (Head, error) {
	head := Head{CommitID: value.CommitID, Root: value.Root, Revision: value.Revision}
	return head, validateHead(head)
}

func validateHead(value Head) error {
	if value.CommitID == "" {
		if value.Root != "" || value.Revision != 0 {
			return fmt.Errorf("empty head has root or revision")
		}
		return nil
	}
	if value.Root == "" || value.Revision == 0 {
		return fmt.Errorf("non-empty head lacks root or revision")
	}
	if _, err := cid.Parse(value.Root); err != nil {
		return err
	}
	return nil
}

func monotonicHead(current, incoming Head) (Head, error) {
	if incoming.Revision < current.Revision {
		return current, nil
	}
	if incoming.Revision > current.Revision {
		return incoming, nil
	}
	if current != incoming {
		return current, fmt.Errorf("revision %d identifies different commits or roots", current.Revision)
	}
	return current, nil
}

func hasPending(values []Stash) bool {
	for _, value := range values {
		if value.Status == "pending" || value.Status == "branched" {
			return true
		}
	}
	return false
}

func conflictsFromTransport(values []transport.BucketConflict) []Conflict {
	result := make([]Conflict, len(values))
	for i, value := range values {
		result[i] = Conflict{Coordinate: value.Coordinate, Base: value.Base, Local: value.Local, Remote: value.Remote}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Coordinate < result[j].Coordinate })
	return result
}

func cloneWorkspace(value Workspace) Workspace {
	value.Stashes = append([]Stash(nil), value.Stashes...)
	for i := range value.Stashes {
		value.Stashes[i].Conflicts = append([]Conflict(nil), value.Stashes[i].Conflicts...)
	}
	return value
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

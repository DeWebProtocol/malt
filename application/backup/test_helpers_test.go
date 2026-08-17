package backup

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt-client/bucketsync"
	cid "github.com/ipfs/go-cid"
)

type fixedKeys struct {
	epoch uint32
	key   [32]byte
}

type fixedRootPolicy struct {
	root cid.Cid
}

func (p fixedRootPolicy) AcceptedRoot(string) (cid.Cid, error) {
	return p.root, nil
}

func (fixedRootPolicy) ObserveCandidate(string, cid.Cid, cid.Cid, string) error {
	return nil
}

func (fixedRootPolicy) ObserveHead(string, string, string, string, string, cid.Cid, uint64) error {
	return nil
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

func (s *fakeSync) record(operation string) {
	if s.order != nil {
		*s.order = append(*s.order, operation)
	}
}

func (s *fakeSync) Pull(context.Context) (bucketsync.Workspace, error) {
	s.record("pull")
	s.workspace.Initialized = true
	return s.workspace, nil
}

func (s *fakeSync) Status() (bucketsync.Workspace, error) {
	s.record("status")
	if s.onStatus != nil {
		s.onStatus()
	}
	return s.workspace, nil
}

func (s *fakeSync) CurrentBase(cid.Cid) (bucketsync.Head, error) {
	s.record("current")
	return s.workspace.Base, nil
}

func (s *fakeSync) Stage(candidate cid.Cid, base bucketsync.Head, _ cid.Cid, message string) (bucketsync.Stash, error) {
	s.record("stage")
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
	s.record("restore-pending")
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
	s.record("push")
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

func (s *fakeSync) ResolveBranched(stashID, candidateRoot string) (bucketsync.Workspace, error) {
	s.record("resolve-branched")
	for i, stash := range s.workspace.Stashes {
		if stash.ID == stashID && stash.CandidateRoot == candidateRoot && stash.Status == "branched" {
			s.workspace.Stashes = append(s.workspace.Stashes[:i], s.workspace.Stashes[i+1:]...)
			return s.workspace, nil
		}
	}
	return bucketsync.Workspace{}, fmt.Errorf("branched stash not found")
}

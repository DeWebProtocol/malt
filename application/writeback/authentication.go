package writeback

import (
	"context"
	"fmt"

	filesystemservice "github.com/dewebprotocol/malt-client/filesystem/service"
	"github.com/dewebprotocol/malt-client/filesystem/staging"
	"github.com/dewebprotocol/malt-client/journal"
	"github.com/dewebprotocol/malt-core/protocol"
	cid "github.com/ipfs/go-cid"
)

type AuthenticationPlanner interface {
	PlanRooted(context.Context, cid.Cid, []journal.Operation) ([]protocol.AuthenticationCandidate, []cid.Cid, cid.Cid, error)
}
type AuthenticationOptions struct {
	Planner AuthenticationPlanner
	Remote  AuthenticationMaterializer
}

func (s *Service) replayAuthentication(ctx context.Context, view filesystemservice.View, batch staging.UploadBatch, result Result) (Result, error) {
	candidates, required, root, err := s.authentication.Planner.PlanRooted(ctx, view.Root, append([]journal.Operation(nil), batch.Operations...))
	if err != nil {
		return result, err
	}
	if !root.Defined() {
		return result, fmt.Errorf("typed planner returned no Root")
	}
	if root.Equals(view.Root) {
		matched, err := s.roots.(acceptedRootCompleter).CompleteIfAccepted(s.trustAlias, view.Root, func() error { var err error; result.Completed, err = s.queue.CompleteNoChange(ctx, batch); return err })
		if err != nil {
			return result, err
		}
		if !matched {
			current, err := s.roots.AcceptedRoot(s.trustAlias)
			if err != nil {
				return result, err
			}
			if _, err := s.queue.MarkUploadConflicted(ctx, batch, acceptedRootConflictID(view.Root, current, root)); err != nil {
				return result, err
			}
			return result, ErrStaleAcceptedView
		}
		result.NoAuthenticatedChange = true
		return result, nil
	}
	available := map[string]staging.UploadPayload{}
	for _, p := range batch.Payloads {
		available[p.CID.KeyString()] = p
	}
	staged := map[string]bool{}
	for _, op := range batch.Operations {
		if op.Kind == journal.KindWrite {
			c, err := cid.Parse(op.PayloadCID)
			if err != nil {
				return result, err
			}
			staged[c.KeyString()] = true
		}
	}
	selected := map[string]staging.UploadPayload{}
	for _, key := range required {
		if staged[key.KeyString()] {
			p, ok := available[key.KeyString()]
			if !ok {
				return result, fmt.Errorf("typed plan requires unavailable payload %s", key)
			}
			selected[key.KeyString()] = p
		}
	}
	// All requirements are checked before publishing any staged body.
	for _, p := range selected {
		got, err := s.payloads.Put(ctx, p.Body)
		if err != nil {
			return result, err
		}
		if !got.Equals(p.CID) {
			return result, fmt.Errorf("payload receipt substituted CID")
		}
	}
	if len(candidates) == 0 {
		return result, fmt.Errorf("typed plan omitted changed Root")
	}
	last, err := cid.Decode(candidates[len(candidates)-1].Root)
	if err != nil || !last.Equals(root) {
		return result, fmt.Errorf("typed plan is not children-before-parent")
	}
	for _, candidate := range candidates {
		expected, err := cid.Decode(candidate.Root)
		if err != nil {
			return result, err
		}
		got, err := s.authentication.Remote.MaterializeAuthentication(ctx, candidate)
		if err != nil {
			return result, err
		}
		if !got.Equals(expected) {
			return result, fmt.Errorf("authentication receipt substituted Root")
		}
	}
	result.CandidateRoot = root
	result.RemotePersisted = true
	return s.completeCandidate(ctx, view, batch, result)
}

// AuthenticationMaterializer persists an exact candidate and returns an
// untrusted receipt CID. It owns neither publication nor accepted-root policy.
type AuthenticationMaterializer interface {
	MaterializeAuthentication(context.Context, protocol.AuthenticationCandidate) (cid.Cid, error)
}

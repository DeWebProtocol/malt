package writeback

import (
	"context"
	"fmt"

	filesystemservice "github.com/dewebprotocol/malt-client/filesystem/service"
	"github.com/dewebprotocol/malt-client/filesystem/staging"
	"github.com/dewebprotocol/malt-client/journal"
	"github.com/dewebprotocol/malt-client/writeplan"
	"github.com/dewebprotocol/malt-core/protocol"
	cid "github.com/ipfs/go-cid"
)

type Planner interface {
	Prepare(context.Context, cid.Cid, []journal.Operation) (writeplan.Plan, error)
}

func (s *Service) replayAuthentication(ctx context.Context, view filesystemservice.View, batch staging.UploadBatch, result Result) (Result, error) {
	plan, err := s.planner.Prepare(ctx, view.Root, append([]journal.Operation(nil), batch.Operations...))
	if err != nil {
		return result, err
	}
	root := plan.Root
	if !root.Defined() {
		return result, fmt.Errorf("typed planner returned no Root")
	}
	if !plan.Base.Equals(view.Root) {
		return result, fmt.Errorf("typed plan substituted base Root")
	}
	if root.Equals(view.Root) {
		matched, err := s.roots.CompleteIfAccepted(s.trustAlias, view.Root, func() error { var err error; result.Completed, err = s.queue.CompleteNoChange(ctx, batch); return err })
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
	plan.TransactionID = batch.TransactionID
	available := map[string]staging.UploadPayload{}
	for _, p := range batch.Payloads {
		available[p.CID.KeyString()] = p
	}
	staged := map[string]bool{}
	for _, op := range batch.Operations {
		if op.Kind == journal.KindWrite {
			c, err := cid.Parse(op.PayloadCID)
			if err != nil || c.Prefix().Codec != cid.Raw {
				return result, fmt.Errorf("staged write requires a raw payload CID")
			}
			staged[c.KeyString()] = true
		}
	}
	selected := map[string]staging.UploadPayload{}
	for _, key := range plan.Required {
		if staged[key.KeyString()] {
			p, ok := available[key.KeyString()]
			if !ok {
				return result, fmt.Errorf("typed plan requires unavailable payload %s", key)
			}
			selected[key.KeyString()] = p
		}
	}
	// Plan includes local manifests; append only the frozen journal bodies
	// actually consumed by the final filesystem projection.
	for _, p := range selected {
		plan.Blocks = append(plan.Blocks, writeplan.Bytes(p.CID, p.Body))
	}
	receipt, err := plan.PersistBatch(ctx, s.payloads, s.remote)
	if err != nil {
		return result, err
	}
	result.Receipt = receipt
	result.CandidateRoot = root
	result.RemotePersisted = true
	return s.completeCandidate(ctx, view, batch, result)
}

// AuthenticationMaterializer persists an exact batch and returns an untrusted
// operational receipt. It owns neither publication nor accepted-root policy.
type AuthenticationMaterializer interface {
	MaterializeAuthenticationBatch(context.Context, protocol.AuthenticationBatch) (protocol.AuthenticationReceipt, error)
}

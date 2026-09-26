package writeback

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	filesystemservice "github.com/dewebprotocol/malt-client/filesystem/service"
	"github.com/dewebprotocol/malt-client/filesystem/staging"
	"github.com/dewebprotocol/malt-client/journal"
	"github.com/dewebprotocol/malt-core/protocol"
	cid "github.com/ipfs/go-cid"
)

const ResultProfile = "malt.filesystem-verified-writeback/v1"

var ErrStaleAcceptedView = errors.New("filesystem write-back selected accepted root is stale")

// Queue is the durable staging boundary. Implementations freeze retry
// identities before network I/O and record completion without accepting roots.
type Queue interface {
	PrepareUpload(context.Context, filesystemservice.View) (staging.UploadBatch, error)
	CompleteUpload(context.Context, staging.UploadBatch, cid.Cid) ([]journal.Operation, error)
	CompleteNoChange(context.Context, staging.UploadBatch) ([]journal.Operation, error)
	MarkUploadConflicted(context.Context, staging.UploadBatch, string) ([]journal.Operation, error)
}

// PayloadStore persists immutable local file bodies. Returned CIDs are
// untrusted and must equal the exact locally staged CID.
type PayloadStore interface {
	PutWithCodec(context.Context, []byte, uint64) (cid.Cid, error)
}

// RootPolicy exposes accepted-root selection, candidate recording and fenced
// completion. It deliberately has no accepted-root promotion method.
type RootPolicy interface {
	AcceptedRoot(string) (cid.Cid, error)
	ObserveCandidate(string, cid.Cid, cid.Cid, string) error
	CompleteIfAccepted(string, cid.Cid, func() error) (bool, error)
}

type Options struct {
	Queue      Queue
	Payloads   PayloadStore
	Remote     AuthenticationMaterializer
	Planner    Planner
	Roots      RootPolicy
	TrustAlias string
	Source     string
}

type Service struct {
	gate       chan struct{}
	queue      Queue
	payloads   PayloadStore
	remote     AuthenticationMaterializer
	planner    Planner
	roots      RootPolicy
	trustAlias string
	source     string
}

// Result distinguishes an exact durable remote materialization from local
// trusted-root acceptance. RootAccepted is always false here.
type Result struct {
	Profile               string
	TransactionID         string
	BaseRoot              cid.Cid
	CandidateRoot         cid.Cid
	Completed             []journal.Operation
	Receipt               protocol.AuthenticationReceipt
	NoAuthenticatedChange bool
	RemotePersisted       bool
	CandidateStored       bool
	RootAccepted          bool
}

func New(opts Options) (*Service, error) {
	if opts.Queue == nil || opts.Payloads == nil || opts.Remote == nil || opts.Planner == nil || opts.Roots == nil {
		return nil, fmt.Errorf("filesystem write-back requires queue, payload, batch materializer, planner, and root-policy capabilities")
	}
	alias := strings.TrimSpace(opts.TrustAlias)
	if alias == "" {
		return nil, fmt.Errorf("filesystem write-back trust alias is empty")
	}
	source := strings.TrimSpace(opts.Source)
	if source == "" {
		source = "filesystem verified write-back"
	}
	service := &Service{gate: make(chan struct{}, 1), queue: opts.Queue, payloads: opts.Payloads,
		remote: opts.Remote, planner: opts.Planner, roots: opts.Roots, trustAlias: alias, source: source}
	service.gate <- struct{}{}
	return service, nil
}

// Replay computes and durably submits one exact candidate batch. Success
// records a candidate and never accepts a trusted root.
func (s *Service) Replay(ctx context.Context, view filesystemservice.View) (Result, error) {
	if s == nil {
		return Result{}, fmt.Errorf("filesystem write-back service is nil")
	}
	if ctx == nil {
		return Result{}, fmt.Errorf("filesystem write-back context is nil")
	}
	select {
	case <-ctx.Done():
		return Result{}, ctx.Err()
	case <-s.gate:
	}
	defer func() { s.gate <- struct{}{} }()
	accepted, err := s.roots.AcceptedRoot(s.trustAlias)
	if err != nil {
		return Result{}, fmt.Errorf("select local accepted root: %w", err)
	}
	if !accepted.Equals(view.Root) {
		return Result{}, fmt.Errorf("%w: selected %s, current %s", ErrStaleAcceptedView, view.Root, accepted)
	}
	batch, err := s.queue.PrepareUpload(ctx, view)
	if err != nil {
		return Result{}, err
	}
	result := Result{Profile: ResultProfile, TransactionID: batch.TransactionID, BaseRoot: view.Root}
	if err := validateAvailablePayloads(batch.Payloads); err != nil {
		return result, err
	}
	return s.replayAuthentication(ctx, view, batch, result)
}

func (s *Service) completeCandidate(ctx context.Context, view filesystemservice.View, batch staging.UploadBatch, result Result) (Result, error) {
	if err := s.roots.ObserveCandidate(s.trustAlias, result.CandidateRoot, view.Root, s.source); err != nil {
		current, currentErr := s.roots.AcceptedRoot(s.trustAlias)
		if currentErr == nil && !current.Equals(view.Root) {
			conflictID := acceptedRootConflictID(view.Root, current, result.CandidateRoot)
			if _, conflictErr := s.queue.MarkUploadConflicted(ctx, batch, conflictID); conflictErr != nil {
				return result, errors.Join(
					fmt.Errorf("%w: accepted root advanced to %s", ErrStaleAcceptedView, current),
					fmt.Errorf("preserve write-back conflict: %w", conflictErr),
				)
			}
			return result, fmt.Errorf("%w: accepted root advanced to %s", ErrStaleAcceptedView, current)
		}
		return result, fmt.Errorf("record verified filesystem candidate: %w", err)
	}
	result.CandidateStored = true
	roots := s.roots
	var completed []journal.Operation
	matched, completeErr := roots.CompleteIfAccepted(s.trustAlias, view.Root, func() error {
		var err error
		completed, err = s.queue.CompleteUpload(ctx, batch, result.CandidateRoot)
		return err
	})
	if completeErr != nil {
		return result, fmt.Errorf("complete verified write-back under accepted-root fence: %w", completeErr)
	}
	if !matched {
		current, currentErr := s.roots.AcceptedRoot(s.trustAlias)
		if currentErr != nil {
			return result, fmt.Errorf("read advanced accepted root: %w", currentErr)
		}
		conflictID := acceptedRootConflictID(view.Root, current, result.CandidateRoot)
		if _, conflictErr := s.queue.MarkUploadConflicted(ctx, batch, conflictID); conflictErr != nil {
			return result, errors.Join(
				fmt.Errorf("%w: accepted root advanced to %s", ErrStaleAcceptedView, current),
				fmt.Errorf("preserve verified write-back conflict: %w", conflictErr),
			)
		}
		return result, fmt.Errorf("%w: accepted root advanced to %s", ErrStaleAcceptedView, current)
	}
	result.Completed = completed
	return result, nil
}

func acceptedRootConflictID(base, current, candidate cid.Cid) string {
	digest := sha256.Sum256([]byte(base.String() + "\x00" + current.String() + "\x00" + candidate.String()))
	return "accepted-root-advanced-" + hex.EncodeToString(digest[:12])
}

func validateAvailablePayloads(payloads []staging.UploadPayload) error {
	for _, payload := range payloads {
		if !payload.CID.Defined() {
			return fmt.Errorf("staged payload CID is undefined")
		}
		computed, err := payload.CID.Prefix().Sum(payload.Body)
		if err != nil {
			return fmt.Errorf("compute staged payload CID %s: %w", payload.CID, err)
		}
		if !computed.Equals(payload.CID) {
			return fmt.Errorf("staged payload bytes do not match CID %s", payload.CID)
		}
	}
	return nil
}

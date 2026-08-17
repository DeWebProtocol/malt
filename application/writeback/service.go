package writeback

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	clientrootapp "github.com/dewebprotocol/malt-client/application/clientroot"
	filesystemservice "github.com/dewebprotocol/malt-client/filesystem/service"
	"github.com/dewebprotocol/malt-client/filesystem/staging"
	"github.com/dewebprotocol/malt-client/journal"
	"github.com/dewebprotocol/malt-core/auth/arcset"
	"github.com/dewebprotocol/malt-core/mutation"
	"github.com/dewebprotocol/malt-core/protocol"
	clientwriter "github.com/dewebprotocol/malt-core/sdk/writer"
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
	Put(context.Context, []byte) (cid.Cid, error)
}

// Planner converts the verified complete old state plus the exact filesystem
// intent snapshot into one output-free canonical MALT semantic intent. Changed
// is false only when replaying the complete batch leaves the authenticated
// projection exactly equal to the verified base View. The write-back service
// derives the minimal staged-payload upload set from the normalized final
// intent rather than publishing every intermediate filesystem write.
type Planner interface {
	Plan(context.Context, mutation.UpdateView, []journal.Operation) (intent mutation.SemanticIntent, changed bool, err error)
}

type PlannerFunc func(context.Context, mutation.UpdateView, []journal.Operation) (mutation.SemanticIntent, bool, error)

func (f PlannerFunc) Plan(ctx context.Context, view mutation.UpdateView, operations []journal.Operation) (mutation.SemanticIntent, bool, error) {
	return f(ctx, view, operations)
}

// RootPolicy supplies only accepted-root selection and candidate recording.
// It deliberately exposes no acceptance method to this service. A production
// policy must additionally implement the private acceptedRootCompleter fence
// before a verified no-change batch can be completed.
type RootPolicy interface {
	AcceptedRoot(string) (cid.Cid, error)
	ObserveCandidate(string, cid.Cid, cid.Cid, string) error
}

type acceptedRootCompleter interface {
	CompleteIfAccepted(string, cid.Cid, func() error) (bool, error)
}

type Options struct {
	Queue      Queue
	Payloads   PayloadStore
	Remote     clientrootapp.Remote
	Writer     *clientwriter.Runtime
	Planner    Planner
	Roots      RootPolicy
	TrustAlias string
	Source     string
	ViewBounds protocol.UpdateViewBounds
}

type Service struct {
	gate       chan struct{}
	queue      Queue
	payloads   PayloadStore
	remote     clientrootapp.Remote
	writer     *clientwriter.Runtime
	planner    Planner
	roots      RootPolicy
	trustAlias string
	source     string
	bounds     protocol.UpdateViewBounds
}

// Result distinguishes an exact durable remote materialization from local
// trusted-root acceptance. RootAccepted is always false here.
type Result struct {
	Profile               string
	OperationID           string
	BaseRoot              cid.Cid
	CandidateRoot         cid.Cid
	Completed             []journal.Operation
	Receipt               mutation.MaterializationReceipt
	NoAuthenticatedChange bool
	RemotePersisted       bool
	CandidateStored       bool
	RootAccepted          bool
}

func New(opts Options) (*Service, error) {
	if opts.Queue == nil || opts.Payloads == nil || opts.Remote == nil || opts.Writer == nil || opts.Planner == nil || opts.Roots == nil {
		return nil, fmt.Errorf("filesystem write-back requires queue, payload, remote, writer, planner, and root-policy capabilities")
	}
	alias := strings.TrimSpace(opts.TrustAlias)
	if alias == "" {
		return nil, fmt.Errorf("filesystem write-back trust alias is empty")
	}
	source := strings.TrimSpace(opts.Source)
	if source == "" {
		source = "filesystem verified write-back"
	}
	bounds := opts.ViewBounds
	if bounds.MaxObjects == 0 && bounds.MaxTotalEntries == 0 && bounds.MaxDepth == 0 {
		bounds = protocol.UpdateViewBounds{MaxObjects: 4096, MaxTotalEntries: 65536, MaxDepth: 256}
	}
	if bounds.MaxObjects == 0 || bounds.MaxTotalEntries == 0 || bounds.MaxDepth == 0 {
		return nil, fmt.Errorf("filesystem write-back view bounds must all be positive or all omitted")
	}
	service := &Service{
		gate:  make(chan struct{}, 1),
		queue: opts.Queue, payloads: opts.Payloads, remote: opts.Remote,
		writer: opts.Writer, planner: opts.Planner, roots: opts.Roots,
		trustAlias: alias, source: source, bounds: bounds,
	}
	service.gate <- struct{}{}
	return service, nil
}

// Replay computes and durably submits one exact candidate for the current
// durable overlay. Success records a candidate but never accepts it.
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
	result := Result{Profile: ResultProfile, OperationID: batch.OperationID, BaseRoot: view.Root}
	if err := validateAvailablePayloads(batch.Payloads); err != nil {
		return result, err
	}
	session, err := clientrootapp.New(s.remote, s.writer)
	if err != nil {
		return result, err
	}
	if _, err := session.Load(ctx, view.Root, &s.bounds); err != nil {
		return result, fmt.Errorf("load verified client-root view: %w", err)
	}
	verifiedView, err := session.SnapshotView()
	if err != nil {
		return result, err
	}
	intent, changed, err := s.planner.Plan(ctx, verifiedView, append([]journal.Operation(nil), batch.Operations...))
	if err != nil {
		return result, fmt.Errorf("plan filesystem semantic intent: %w", err)
	}
	if !changed {
		roots, ok := s.roots.(acceptedRootCompleter)
		if !ok {
			return result, fmt.Errorf("root policy does not support accepted-root fenced completion")
		}
		var completed []journal.Operation
		matched, completeErr := roots.CompleteIfAccepted(s.trustAlias, view.Root, func() error {
			var err error
			completed, err = s.queue.CompleteNoChange(ctx, batch)
			return err
		})
		if completeErr != nil {
			return result, fmt.Errorf("complete no-change batch under accepted-root fence: %w", completeErr)
		}
		if !matched {
			current, currentErr := s.roots.AcceptedRoot(s.trustAlias)
			if currentErr != nil {
				return result, fmt.Errorf("read advanced accepted root: %w", currentErr)
			}
			conflictID := acceptedRootConflictID(view.Root, current, view.Root)
			if _, conflictErr := s.queue.MarkUploadConflicted(ctx, batch, conflictID); conflictErr != nil {
				return result, errors.Join(
					fmt.Errorf("%w: accepted root advanced to %s", ErrStaleAcceptedView, current),
					fmt.Errorf("preserve no-change write-back conflict: %w", conflictErr),
				)
			}
			return result, fmt.Errorf("%w: accepted root advanced to %s", ErrStaleAcceptedView, current)
		}
		result.Completed = completed
		result.NoAuthenticatedChange = true
		return result, nil
	}
	intent, err = mutation.NormalizeSemanticIntent(verifiedView, intent)
	if err != nil {
		return result, fmt.Errorf("normalize filesystem semantic intent: %w", err)
	}
	selectedPayloads, err := selectRequiredPayloads(batch, intent)
	if err != nil {
		return result, err
	}
	for _, payload := range selectedPayloads {
		if !payload.CID.Defined() {
			return result, fmt.Errorf("staged payload CID is undefined")
		}
		computed, err := payload.CID.Prefix().Sum(payload.Body)
		if err != nil {
			return result, fmt.Errorf("compute staged payload CID %s: %w", payload.CID, err)
		}
		if !computed.Equals(payload.CID) {
			return result, fmt.Errorf("staged payload bytes do not match CID %s", payload.CID)
		}
		stored, err := s.payloads.Put(ctx, payload.Body)
		if err != nil {
			return result, fmt.Errorf("persist staged payload %s: %w", payload.CID, err)
		}
		if !stored.Equals(payload.CID) {
			return result, fmt.Errorf("payload store substituted CID %s for %s", stored, payload.CID)
		}
	}
	executed, err := session.Execute(ctx, batch.OperationID, intent)
	if err != nil {
		return result, fmt.Errorf("execute verified client-root write-back: %w", err)
	}
	result.CandidateRoot = executed.Candidate
	result.Receipt = executed.Receipt
	result.RemotePersisted = true
	if err := s.roots.ObserveCandidate(s.trustAlias, executed.Candidate, view.Root, s.source); err != nil {
		current, currentErr := s.roots.AcceptedRoot(s.trustAlias)
		if currentErr == nil && !current.Equals(view.Root) {
			conflictID := acceptedRootConflictID(view.Root, current, executed.Candidate)
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
	completed, err := s.queue.CompleteUpload(ctx, batch, executed.Candidate)
	if err != nil {
		return result, err
	}
	result.Completed = completed
	return result, nil
}

func acceptedRootConflictID(base, current, candidate cid.Cid) string {
	digest := sha256.Sum256([]byte(base.String() + "\x00" + current.String() + "\x00" + candidate.String()))
	return "accepted-root-advanced-" + hex.EncodeToString(digest[:12])
}

func selectRequiredPayloads(batch staging.UploadBatch, intent mutation.SemanticIntent) ([]staging.UploadPayload, error) {
	available := make(map[string]staging.UploadPayload, len(batch.Payloads))
	for _, payload := range batch.Payloads {
		if !payload.CID.Defined() {
			return nil, fmt.Errorf("staged payload CID is undefined")
		}
		available[payload.CID.KeyString()] = payload
	}
	stagedWrites := make(map[string]cid.Cid)
	for _, operation := range batch.Operations {
		if operation.Kind != journal.KindWrite {
			continue
		}
		key, err := cid.Parse(operation.PayloadCID)
		if err != nil || key.Prefix().Codec != cid.Raw {
			return nil, fmt.Errorf("filesystem write %s has invalid raw payload CID", operation.OperationID)
		}
		stagedWrites[key.KeyString()] = key
	}
	required := make(map[string]cid.Cid)
	for _, transition := range intent.Transitions {
		for _, change := range transition.Changes {
			if change.After == nil || change.After.Kind() != arcset.TargetKindCAS || change.After.CID().Prefix().Codec != cid.Raw {
				continue
			}
			identity := change.After.CID().KeyString()
			if key, staged := stagedWrites[identity]; staged {
				required[identity] = key
			}
		}
	}
	keys := make([]cid.Cid, 0, len(required))
	for _, key := range required {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(left, right cid.Cid) int { return strings.Compare(left.String(), right.String()) })
	selected := make([]staging.UploadPayload, 0, len(keys))
	for _, key := range keys {
		payload, ok := available[key.KeyString()]
		if !ok {
			return nil, fmt.Errorf("semantic intent requires unavailable staged payload %s", key)
		}
		selected = append(selected, payload)
	}
	return selected, nil
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

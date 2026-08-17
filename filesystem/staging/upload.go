package staging

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/dewebprotocol/malt-client/cache"
	filesystemservice "github.com/dewebprotocol/malt-client/filesystem/service"
	"github.com/dewebprotocol/malt-client/journal"
	cid "github.com/ipfs/go-cid"
)

var (
	ErrNoPendingUpload = errors.New("filesystem staging has no pending upload")
	ErrUploadConflict  = errors.New("filesystem staging contains an unresolved conflict")
	ErrUploadBatch     = errors.New("filesystem staging upload batch is stale or invalid")
)

// UploadPayload is one exact locally staged raw-CID body. Body is a private
// copy and is not remotely verified data.
type UploadPayload struct {
	CID  cid.Cid
	Body []byte
}

// UploadBatch freezes the replay identity for unfinished operations while
// retaining completed operations needed to rebuild the complete overlay from
// the same accepted base. OperationID is deterministic for this exact intent
// snapshot and is suitable for the MALT Core client-root workflow.
type UploadBatch struct {
	View        filesystemservice.View
	OperationID string
	Operations  []journal.Operation
	Pending     []journal.Operation
	Payloads    []UploadPayload
}

// PrepareUpload atomically freezes every replayable operation for view before
// returning any payload bytes. A pending request is never demoted after this
// point because a previous attempt may already have reached a remote executor.
func (s *Service) PrepareUpload(ctx context.Context, view filesystemservice.View) (UploadBatch, error) {
	if err := s.enter(); err != nil {
		return UploadBatch{}, err
	}
	defer s.leave()
	if err := ctx.Err(); err != nil {
		return UploadBatch{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	operations, err := s.operations(view)
	if err != nil {
		return UploadBatch{}, err
	}
	ids := make([]string, 0, len(operations))
	for _, operation := range operations {
		switch operation.Status {
		case journal.StatusConflicted:
			return UploadBatch{}, fmt.Errorf("%w: operation %s", ErrUploadConflict, operation.OperationID)
		case journal.StatusLocalDirty, journal.StatusOfflineOnly, journal.StatusPendingUpload:
			ids = append(ids, operation.OperationID)
		}
	}
	if len(ids) == 0 {
		return UploadBatch{}, ErrNoPendingUpload
	}
	if _, err := s.journal.FreezeBatchForUpload(ids); err != nil {
		return UploadBatch{}, fmt.Errorf("freeze filesystem upload batch: %w", err)
	}
	operations, err = s.operations(view)
	if err != nil {
		return UploadBatch{}, err
	}
	if err := s.reconcileCacheStates(operations); err != nil {
		return UploadBatch{}, fmt.Errorf("reconcile pending upload cache state: %w", err)
	}
	payloads, err := s.uploadPayloads(operations)
	if err != nil {
		return UploadBatch{}, err
	}
	pendingByID := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		pendingByID[id] = struct{}{}
	}
	pending := make([]journal.Operation, 0, len(ids))
	for _, operation := range operations {
		if _, ok := pendingByID[operation.OperationID]; ok {
			pending = append(pending, operation)
		}
	}
	return UploadBatch{
		View: view, OperationID: uploadOperationID(view, operations),
		Operations: cloneJournalOperations(operations), Pending: cloneJournalOperations(pending),
		Payloads: payloads,
	}, nil
}

// CompleteUpload records candidate for every pending record in batch as one
// atomic journal transition. The caller must first compute candidate locally
// and verify the exact durable receipt. This method has no trust capability and
// cannot promote the candidate to an accepted root.
func (s *Service) CompleteUpload(ctx context.Context, batch UploadBatch, candidate cid.Cid) ([]journal.Operation, error) {
	if err := s.enter(); err != nil {
		return nil, err
	}
	defer s.leave()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !candidate.Defined() || candidate.Equals(batch.View.Root) {
		return nil, fmt.Errorf("%w: candidate is undefined or equal to its base", ErrUploadBatch)
	}
	if err := validateUploadBatch(batch); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.operations(batch.View)
	if err != nil {
		return nil, err
	}
	if err := requireCompletionSnapshot(current, batch.Pending, candidate); err != nil {
		return nil, err
	}
	ids := operationIDs(batch.Pending)
	completed, err := s.journal.CompleteBatch(ids, candidate.String())
	if err != nil {
		return nil, fmt.Errorf("complete filesystem upload batch: %w", err)
	}
	current, err = s.operations(batch.View)
	if err != nil {
		return nil, err
	}
	if err := s.reconcileCacheStates(current); err != nil {
		return nil, fmt.Errorf("reconcile completed upload cache state: %w", err)
	}
	return cloneJournalOperations(completed), nil
}

// MarkUploadConflicted atomically preserves a conflict identity for every
// pending record in batch. Conflict resolution must create replacement journal
// identities; automatic replay excludes these records.
func (s *Service) MarkUploadConflicted(ctx context.Context, batch UploadBatch, conflictID string) ([]journal.Operation, error) {
	if err := s.enter(); err != nil {
		return nil, err
	}
	defer s.leave()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateUploadBatch(batch); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.operations(batch.View)
	if err != nil {
		return nil, err
	}
	canonicalConflictID := strings.TrimSpace(conflictID)
	if err := requireConflictSnapshot(current, batch.Pending, canonicalConflictID); err != nil {
		return nil, err
	}
	conflicted, err := s.journal.MarkBatchConflicted(operationIDs(batch.Pending), canonicalConflictID)
	if err != nil {
		return nil, fmt.Errorf("conflict filesystem upload batch: %w", err)
	}
	current, err = s.operations(batch.View)
	if err != nil {
		return nil, err
	}
	if err := s.reconcileCacheStates(current); err != nil {
		return nil, fmt.Errorf("reconcile conflicted upload cache state: %w", err)
	}
	return cloneJournalOperations(conflicted), nil
}

func (s *Service) uploadPayloads(operations []journal.Operation) ([]UploadPayload, error) {
	byCID := make(map[string]UploadPayload)
	for _, operation := range operations {
		if operation.Kind != journal.KindWrite {
			continue
		}
		binding := bindingFromOperation(operation)
		body, _, err := s.cache.ReadLocal(binding)
		if err != nil {
			return nil, fmt.Errorf("read upload payload for operation %s: %w", operation.OperationID, err)
		}
		byCID[binding.CID.KeyString()] = UploadPayload{CID: binding.CID, Body: append([]byte(nil), body...)}
	}
	payloads := make([]UploadPayload, 0, len(byCID))
	for _, payload := range byCID {
		payloads = append(payloads, payload)
	}
	slices.SortFunc(payloads, func(left, right UploadPayload) int { return strings.Compare(left.CID.String(), right.CID.String()) })
	return payloads, nil
}

func (s *Service) reconcileCacheStates(operations []journal.Operation) error {
	type desired struct {
		state cache.State
		rank  int
	}
	desiredByBinding := make(map[cache.Binding]desired)
	for _, operation := range operations {
		if operation.Kind != journal.KindWrite || operation.Status == journal.StatusSuperseded {
			continue
		}
		state, rank, ok := cacheStateForOperation(operation.Status)
		if !ok {
			continue
		}
		binding := bindingFromOperation(operation)
		if current, exists := desiredByBinding[binding]; !exists || rank > current.rank {
			desiredByBinding[binding] = desired{state: state, rank: rank}
		}
	}
	for binding, value := range desiredByBinding {
		if _, err := s.cache.ReconcileLocalState(binding, value.state); err != nil {
			return fmt.Errorf("reconcile local cache payload %s as %s: %w", binding.CID, value.state, err)
		}
	}
	return nil
}

func cacheStateForOperation(status journal.Status) (cache.State, int, bool) {
	switch status {
	case journal.StatusConflicted:
		return cache.StateConflicted, 5, true
	case journal.StatusPendingUpload:
		return cache.StatePendingUpload, 4, true
	case journal.StatusLocalDirty:
		return cache.StateLocalDirty, 3, true
	case journal.StatusOfflineOnly:
		return cache.StateOfflineOnly, 2, true
	case journal.StatusCompleted:
		return cache.StateCandidate, 1, true
	default:
		return "", 0, false
	}
}

func validateUploadBatch(batch UploadBatch) error {
	if err := validateView(batch.View); err != nil {
		return fmt.Errorf("%w: %v", ErrUploadBatch, err)
	}
	if len(batch.Operations) == 0 || len(batch.Pending) == 0 || batch.OperationID != uploadOperationID(batch.View, batch.Operations) {
		return ErrUploadBatch
	}
	byID := make(map[string]journal.Operation, len(batch.Operations))
	expectedPending := make(map[string]journal.Operation)
	for _, operation := range batch.Operations {
		if _, exists := byID[operation.OperationID]; exists {
			return ErrUploadBatch
		}
		switch operation.Status {
		case journal.StatusPendingUpload:
			expectedPending[operation.OperationID] = operation
		case journal.StatusCompleted:
			if _, err := cid.Parse(operation.ResultRoot); err != nil {
				return ErrUploadBatch
			}
		default:
			return ErrUploadBatch
		}
		byID[operation.OperationID] = operation
	}
	seenPending := make(map[string]struct{}, len(batch.Pending))
	for _, operation := range batch.Pending {
		selected, ok := byID[operation.OperationID]
		_, expected := expectedPending[operation.OperationID]
		if _, duplicate := seenPending[operation.OperationID]; duplicate || !ok || !expected || selected != operation {
			return ErrUploadBatch
		}
		seenPending[operation.OperationID] = struct{}{}
	}
	if len(seenPending) != len(expectedPending) {
		return ErrUploadBatch
	}
	return nil
}

func requireCompletionSnapshot(current, pending []journal.Operation, candidate cid.Cid) error {
	return requireTargetSnapshot(current, pending, func(operation journal.Operation) bool {
		return operation.Status == journal.StatusPendingUpload ||
			(operation.Status == journal.StatusCompleted && operation.ResultRoot == candidate.String())
	})
}

func requireConflictSnapshot(current, pending []journal.Operation, conflictID string) error {
	return requireTargetSnapshot(current, pending, func(operation journal.Operation) bool {
		return operation.Status == journal.StatusPendingUpload ||
			(operation.Status == journal.StatusConflicted && operation.ConflictID == conflictID)
	})
}

func requireTargetSnapshot(current, pending []journal.Operation, validTarget func(journal.Operation) bool) error {
	byID := make(map[string]journal.Operation, len(current))
	for _, operation := range current {
		byID[operation.OperationID] = operation
	}
	for _, expected := range pending {
		operation, ok := byID[expected.OperationID]
		if !ok || operation.Sequence != expected.Sequence || operation.Intent != expected.Intent || !validTarget(operation) {
			return fmt.Errorf("%w: pending operation %s changed", ErrUploadBatch, expected.OperationID)
		}
	}
	return nil
}

func operationIDs(operations []journal.Operation) []string {
	ids := make([]string, len(operations))
	for index, operation := range operations {
		ids[index] = operation.OperationID
	}
	return ids
}

func uploadOperationID(view filesystemservice.View, operations []journal.Operation) string {
	hash := sha256.New()
	writeUploadString(hash, view.DatasetID)
	writeUploadString(hash, view.Branch)
	writeUploadString(hash, view.Root.String())
	writeUploadUint64(hash, view.Revision)
	writeUploadUint64(hash, uint64(view.EncryptionEpoch))
	for _, operation := range operations {
		writeUploadUint64(hash, operation.Sequence)
		writeUploadString(hash, operation.OperationID)
		writeUploadString(hash, operation.RetryID)
		writeUploadString(hash, operation.DatasetID)
		writeUploadString(hash, operation.Branch)
		writeUploadString(hash, operation.BaseRoot)
		writeUploadUint64(hash, operation.BaseRevision)
		writeUploadString(hash, string(operation.Kind))
		writeUploadString(hash, operation.Path)
		writeUploadString(hash, operation.Destination)
		writeUploadString(hash, operation.PayloadCID)
		writeUploadUint64(hash, uint64(operation.EncryptionEpoch))
	}
	return "fs-" + hex.EncodeToString(hash.Sum(nil)[:16])
}

type uploadHashWriter interface {
	Write([]byte) (int, error)
}

func writeUploadString(writer uploadHashWriter, value string) {
	writeUploadUint64(writer, uint64(len(value)))
	_, _ = writer.Write([]byte(value))
}

func writeUploadUint64(writer uploadHashWriter, value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = writer.Write(encoded[:])
}

func cloneJournalOperations(operations []journal.Operation) []journal.Operation {
	return append([]journal.Operation(nil), operations...)
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	materializermemory "github.com/dewebprotocol/malt/auth/arcset/materializer/memory"
	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/mutation"
	"github.com/dewebprotocol/malt/protocol"
	clientwriter "github.com/dewebprotocol/malt/sdk/writer"
	"github.com/dewebprotocol/malt/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

type computer struct {
	schemes map[maltcid.BackendKind]commitment.IndexCommitment
}

type sessionComputer struct {
	mu                    sync.Mutex
	computer              *computer
	session               *clientwriter.Session
	store                 *materializermemory.Store
	view                  mutation.UpdateView
	prepared              map[string]preparedCandidate
	preparedResponseBytes int
}

type preparedCandidate struct {
	result        clientwriter.ComputeResult
	responseBytes int
}

const (
	maxPreparedCandidates    = 64
	maxPreparedResponseBytes = 64 << 20
	writerPrepareSummaryV1   = "malt.writer-prepare-summary/v1"
)

type writerPrepareSummary struct {
	Profile     string                       `json:"profile"`
	OperationID string                       `json:"operation_id"`
	Candidate   string                       `json:"candidate"`
	Outputs     []writerPrepareSummaryOutput `json:"outputs"`
	PayloadCIDs []string                     `json:"payload_cids"`
}

type writerPrepareSummaryOutput struct {
	TransitionID string `json:"transition_id"`
	Root         string `json:"root"`
}

func newSessionComputer(computer *computer) (*sessionComputer, error) {
	if computer == nil || len(computer.schemes) == 0 {
		return nil, fmt.Errorf("client writer is not initialized")
	}
	return &sessionComputer{computer: computer}, nil
}

func (c *computer) newRuntime() (*clientwriter.Runtime, error) {
	if c == nil || len(c.schemes) == 0 {
		return nil, fmt.Errorf("client writer is not initialized")
	}
	return clientwriter.NewRuntime(materializermemory.New(true), c.schemes)
}

func (c *computer) newSessionRuntime() (*clientwriter.Runtime, *materializermemory.Store, error) {
	if c == nil || len(c.schemes) == 0 {
		return nil, nil, fmt.Errorf("client writer is not initialized")
	}
	store := materializermemory.New(true)
	runtime, err := clientwriter.NewRuntime(store, c.schemes)
	if err != nil {
		return nil, nil, err
	}
	return runtime, store, nil
}

func (c *computer) compute(ctx context.Context, operationID string, updateViewJSON, semanticIntentJSON []byte) ([]byte, error) {
	if c == nil || len(c.schemes) == 0 {
		return nil, fmt.Errorf("client writer is not initialized")
	}
	wireView, err := protocol.DecodeUpdateView(updateViewJSON)
	if err != nil {
		return nil, err
	}
	view, err := wireView.Core()
	if err != nil {
		return nil, err
	}
	wireIntent, err := protocol.DecodeSemanticIntent(semanticIntentJSON, view)
	if err != nil {
		return nil, err
	}
	intent, err := wireIntent.Core(view)
	if err != nil {
		return nil, err
	}
	runtime, err := c.newRuntime()
	if err != nil {
		return nil, err
	}
	verified, err := runtime.VerifyUpdateView(ctx, view)
	if err != nil {
		return nil, fmt.Errorf("verify update view: %w", err)
	}
	result, err := runtime.ComputeBundle(ctx, operationID, verified, intent)
	if err != nil {
		return nil, fmt.Errorf("compute client root: %w", err)
	}
	return encodeComputeResult(result)
}

func (s *sessionComputer) load(ctx context.Context, updateViewJSON []byte) (string, error) {
	if s == nil || s.computer == nil {
		return "", fmt.Errorf("client writer session is not initialized")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	wireView, err := protocol.DecodeUpdateView(updateViewJSON)
	if err != nil {
		return "", err
	}
	view, err := wireView.Core()
	if err != nil {
		return "", err
	}
	runtime, store, err := s.computer.newSessionRuntime()
	if err != nil {
		return "", err
	}
	session, err := clientwriter.NewSession(runtime)
	if err != nil {
		return "", err
	}
	if err := session.Load(ctx, view); err != nil {
		return "", fmt.Errorf("load client writer session: %w", err)
	}

	s.session = session
	s.store = store
	s.view = view
	s.prepared = make(map[string]preparedCandidate)
	s.preparedResponseBytes = 0
	return view.BaseRoot.String(), nil
}

func (s *sessionComputer) prepare(ctx context.Context, operationID string, semanticIntentJSON []byte) ([]byte, error) {
	return s.prepareResponse(ctx, operationID, semanticIntentJSON, false)
}

func (s *sessionComputer) prepareCompact(ctx context.Context, operationID string, semanticIntentJSON []byte) ([]byte, error) {
	return s.prepareResponse(ctx, operationID, semanticIntentJSON, true)
}

func (s *sessionComputer) prepareResponse(
	ctx context.Context,
	operationID string,
	semanticIntentJSON []byte,
	compact bool,
) ([]byte, error) {
	if s == nil {
		return nil, fmt.Errorf("client writer session is not initialized")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil {
		return nil, fmt.Errorf("client writer session has no update view")
	}
	if _, exists := s.prepared[operationID]; exists {
		return nil, fmt.Errorf("operation %q is already prepared", operationID)
	}
	if len(s.prepared) >= maxPreparedCandidates {
		return nil, fmt.Errorf("client writer session already retains %d prepared candidates", maxPreparedCandidates)
	}
	wireIntent, err := protocol.DecodeSemanticIntent(semanticIntentJSON, s.view)
	if err != nil {
		return nil, err
	}
	intent, err := wireIntent.Core(s.view)
	if err != nil {
		return nil, err
	}
	result, err := s.session.Prepare(ctx, operationID, intent)
	if err != nil {
		s.retainMaterializedRoots()
		return nil, fmt.Errorf("prepare client writer session: %w", err)
	}
	fullResponse, err := encodeComputeResult(result)
	if err != nil {
		s.retainMaterializedRoots()
		return nil, err
	}
	response := fullResponse
	if compact {
		response, err = encodePrepareSummary(result)
		if err != nil {
			s.retainMaterializedRoots()
			return nil, err
		}
	}
	if len(fullResponse) > maxPreparedResponseBytes-s.preparedResponseBytes {
		s.retainMaterializedRoots()
		return nil, fmt.Errorf(
			"prepared client writer responses exceed %d retained bytes",
			maxPreparedResponseBytes,
		)
	}
	s.prepared[operationID] = preparedCandidate{result: result, responseBytes: len(fullResponse)}
	s.preparedResponseBytes += len(fullResponse)
	return response, nil
}

func (s *sessionComputer) acceptReceipt(operationID string, receiptJSON []byte) (string, error) {
	if s == nil {
		return "", fmt.Errorf("client writer session is not initialized")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil {
		return "", fmt.Errorf("client writer session has no update view")
	}
	candidate, ok := s.prepared[operationID]
	if !ok {
		return "", fmt.Errorf("operation %q is not prepared", operationID)
	}
	prepared := candidate.result
	wireReceipt, err := protocol.DecodeMaterializationReceipt(receiptJSON, prepared.Bundle)
	if err != nil {
		return "", err
	}
	receipt, err := wireReceipt.Core(prepared.Bundle)
	if err != nil {
		return "", err
	}
	if err := s.session.AcceptReceipt(receipt, prepared); err != nil {
		return "", fmt.Errorf("accept client writer receipt: %w", err)
	}
	next, err := mutation.NormalizeUpdateView(prepared.NextView)
	if err != nil {
		return "", fmt.Errorf("retain client writer next view: %w", err)
	}
	s.view = next
	s.prepared = make(map[string]preparedCandidate)
	s.preparedResponseBytes = 0
	s.retainMaterializedRoots()
	return next.BaseRoot.String(), nil
}

func (s *sessionComputer) discard(operationID string) error {
	if s == nil {
		return fmt.Errorf("client writer session is not initialized")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil {
		return fmt.Errorf("client writer session has no update view")
	}
	if _, ok := s.prepared[operationID]; !ok {
		return fmt.Errorf("operation %q is not prepared", operationID)
	}
	candidate := s.prepared[operationID]
	delete(s.prepared, operationID)
	s.preparedResponseBytes -= candidate.responseBytes
	s.retainMaterializedRoots()
	return nil
}

func (s *sessionComputer) retainMaterializedRoots() {
	if s.store == nil {
		return
	}
	retain := make(map[string][]cid.Cid)
	addViewRoots(retain, s.view)
	for _, candidate := range s.prepared {
		addViewRoots(retain, candidate.result.NextView)
	}
	s.store.RetainRoots(retain)
}

func addViewRoots(retain map[string][]cid.Cid, view mutation.UpdateView) {
	for _, object := range view.Objects {
		scope := "client-root/v1/" + object.ObjectID
		retain[scope] = append(retain[scope], object.Root)
	}
}

func encodeComputeResult(result clientwriter.ComputeResult) ([]byte, error) {
	response, err := protocol.NewWriterComputeResult(result.Bundle, result.NextView, protocol.WriterComputeMetrics{
		ViewNormalizationNS:    result.Metrics.ViewNormalizationNS,
		IntentNormalizationNS:  result.Metrics.IntentNormalizationNS,
		DigestNS:               result.Metrics.DigestNS,
		CommitmentUpdateNS:     result.Metrics.CommitmentUpdateNS,
		RootComputationNS:      result.Metrics.RootComputationNS,
		ExpectedRootEncodingNS: result.Metrics.ExpectedRootEncodingNS,
		BundleValidationNS:     result.Metrics.BundleValidationNS,
		NextViewNS:             result.Metrics.NextViewNS,
		TotalNS:                result.Metrics.TotalNS,
	})
	if err != nil {
		return nil, fmt.Errorf("encode writer compute result: %w", err)
	}
	return json.Marshal(response)
}

func encodePrepareSummary(result clientwriter.ComputeResult) ([]byte, error) {
	outputs := make([]writerPrepareSummaryOutput, len(result.Bundle.Outputs))
	for index, output := range result.Bundle.Outputs {
		outputs[index] = writerPrepareSummaryOutput{
			TransitionID: output.TransitionID,
			Root:         output.Root.String(),
		}
	}
	payloadCIDs := make([]string, len(result.Bundle.PayloadCIDs))
	for index, payload := range result.Bundle.PayloadCIDs {
		payloadCIDs[index] = payload.String()
	}
	return json.Marshal(writerPrepareSummary{
		Profile:     writerPrepareSummaryV1,
		OperationID: result.Bundle.OperationID,
		Candidate:   result.Bundle.Candidate.String(),
		Outputs:     outputs,
		PayloadCIDs: payloadCIDs,
	})
}

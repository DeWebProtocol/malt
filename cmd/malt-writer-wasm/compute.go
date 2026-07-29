package main

import (
	"context"
	"encoding/json"
	"fmt"

	materializermemory "github.com/dewebprotocol/malt/auth/arcset/materializer/memory"
	"github.com/dewebprotocol/malt/auth/commitment"
	"github.com/dewebprotocol/malt/auth/commitment/ipa"
	"github.com/dewebprotocol/malt/auth/commitment/kzg"
	"github.com/dewebprotocol/malt/protocol"
	clientwriter "github.com/dewebprotocol/malt/sdk/writer"
	"github.com/dewebprotocol/malt/wire/maltcid"
)

type computer struct {
	schemes map[maltcid.BackendKind]commitment.IndexCommitment
}

func newComputer(backend string) (*computer, error) {
	schemes := make(map[maltcid.BackendKind]commitment.IndexCommitment)
	if backend == "all" || backend == string(maltcid.BackendKindKZG) {
		scheme, err := kzg.NewScheme()
		if err != nil {
			return nil, fmt.Errorf("initialize KZG writer: %w", err)
		}
		schemes[maltcid.BackendKindKZG] = scheme
	}
	if backend == "all" || backend == string(maltcid.BackendKindIPA) {
		scheme, err := ipa.NewScheme()
		if err != nil {
			return nil, fmt.Errorf("initialize IPA writer: %w", err)
		}
		schemes[maltcid.BackendKindIPA] = scheme
	}
	if len(schemes) == 0 {
		return nil, fmt.Errorf("unsupported writer backend %q", backend)
	}
	return &computer{schemes: schemes}, nil
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
	runtime, err := clientwriter.NewRuntime(materializermemory.New(true), c.schemes)
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

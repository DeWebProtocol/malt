package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/dewebprotocol/malt-client/internal/evaluation/gatewaytransport"
)

func TestValidateHealthRequiresSeparatedFilesystemCAS(t *testing.T) {
	token := strings.Repeat("a", 64)
	health := gatewaytransport.Health{
		Status: "ok", EvaluationInstanceToken: token,
		KVBackend: "fs", BlobBackend: "filesystem", ArcTableMode: "versioned",
		CommitmentProfile: "kzg", CommitmentBackends: "ipa,kzg",
		EvaluationCASWriteAccounting: healthCASAccounting, EvaluationCASWriteIsolation: healthCASIsolation,
		ClientRootExactAcceptance:        "false",
		EvaluationRQ3FlatMap:             gatewaytransport.FlatMapProfile,
		EvaluationRQ3FlatMapStorageScope: "arctable-arcset-key-plus-value-only/v1",
		EvaluationRQ3FlatMapCheckpoint:   "false", EvaluationRQ3FlatMapMaterializationCache: "none",
	}
	worker := &campaignWorker{
		config:     workerConfig{instanceToken: token, requestTimeout: time.Second},
		evaluation: fixedHealthGateway{health: &health},
	}
	if err := worker.validateHealth(t.Context()); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*gatewaytransport.Health)
	}{
		{name: "embedded-payload-cas", mutate: func(value *gatewaytransport.Health) { value.BlobBackend = "embedded" }},
		{name: "exact-client-root-acceptance", mutate: func(value *gatewaytransport.Health) { value.ClientRootExactAcceptance = "true" }},
		{name: "client-root-write-accounting", mutate: func(value *gatewaytransport.Health) { value.ClientRootWriteAccounting = gatewayAccountingProfile }},
		{name: "bootstrap-capability", mutate: func(value *gatewaytransport.Health) {
			value.EvaluationClientRootBootstrap = "gateway.evaluation-client-root-bootstrap-object/v1"
		}},
		{name: "missing-flat-map", mutate: func(value *gatewaytransport.Health) { value.EvaluationRQ3FlatMap = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invalid := health
			test.mutate(&invalid)
			worker.evaluation = fixedHealthGateway{health: &invalid}
			if err := worker.validateHealth(t.Context()); err == nil {
				t.Fatal("worker accepted an invalid Gateway evaluation boundary")
			}
		})
	}
}

type fixedHealthGateway struct {
	health *gatewaytransport.Health
}

func (g fixedHealthGateway) Health(context.Context) (*gatewaytransport.Health, error) {
	if g.health == nil {
		return nil, errors.New("health unavailable")
	}
	copy := *g.health
	return &copy, nil
}

func (fixedHealthGateway) BootstrapEvaluationObject(context.Context, string, gatewaytransport.BootstrapObject) (gatewaytransport.BootstrapResult, error) {
	return gatewaytransport.BootstrapResult{}, errors.New("unexpected bootstrap")
}

func (fixedHealthGateway) ApplyEvaluationFlatMap(context.Context, string, gatewaytransport.FlatMapMutation) (gatewaytransport.FlatMapResult, error) {
	return gatewaytransport.FlatMapResult{}, errors.New("unexpected flat map")
}

var _ evaluationGateway = fixedHealthGateway{}

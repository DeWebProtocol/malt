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
		AuthenticationExactAcceptance:              "false",
		EvaluationRQ3FlatPrefix:                    gatewaytransport.FlatPrefixProfile,
		EvaluationRQ3FlatPrefixLayout:              healthFlatLayout,
		EvaluationRQ3FlatPrefixStorageScope:        healthFlatStorageScope,
		EvaluationRQ3FlatPrefixLookupIndex:         healthFlatLookupIndex,
		EvaluationRQ3FlatPrefixCommitmentTreatment: healthCommitmentTreatment,
		EvaluationRQ3FlatPrefixFSKVMode:            healthFlatFSKVMode,
		EvaluationRQ3FlatPrefixCheckpoint:          "false", EvaluationRQ3FlatPrefixMaterializationCache: "none",
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
		{name: "exact-client-root-acceptance", mutate: func(value *gatewaytransport.Health) { value.AuthenticationExactAcceptance = "true" }},
		{name: "client-root-write-accounting", mutate: func(value *gatewaytransport.Health) { value.AuthenticationWriteAccounting = gatewayAccountingProfile }},
		{name: "bootstrap-capability", mutate: func(value *gatewaytransport.Health) {
			value.EvaluationAuthenticationBootstrap = "gateway.evaluation-client-root-bootstrap-object/v1"
		}},
		{name: "missing-flat-prefix", mutate: func(value *gatewaytransport.Health) { value.EvaluationRQ3FlatPrefix = "" }},
		{name: "wrong-layout", mutate: func(value *gatewaytransport.Health) { value.EvaluationRQ3FlatPrefixLayout = "malt-hamt/v1" }},
		{name: "unbounded-lookup-index", mutate: func(value *gatewaytransport.Health) { value.EvaluationRQ3FlatPrefixLookupIndex = "in-memory-unbounded" }},
		{name: "variable-commitment", mutate: func(value *gatewaytransport.Health) { value.EvaluationRQ3FlatPrefixCommitmentTreatment = "variable" }},
		{name: "legacy-fskv", mutate: func(value *gatewaytransport.Health) {
			value.EvaluationRQ3FlatPrefixFSKVMode = "evaluation-incremental-generations/v1"
		}},
		{name: "checkpoint-enabled", mutate: func(value *gatewaytransport.Health) { value.EvaluationRQ3FlatPrefixCheckpoint = "true" }},
		{name: "materialization-cache", mutate: func(value *gatewaytransport.Health) { value.EvaluationRQ3FlatPrefixMaterializationCache = "disk" }},
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

func (fixedHealthGateway) ApplyEvaluationFlatPrefix(context.Context, string, gatewaytransport.FlatPrefixMutation) (gatewaytransport.FlatPrefixResult, error) {
	return gatewaytransport.FlatPrefixResult{}, errors.New("unexpected flat map")
}

var _ evaluationGateway = fixedHealthGateway{}

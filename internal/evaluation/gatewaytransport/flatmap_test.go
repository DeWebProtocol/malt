package gatewaytransport_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt-client/internal/evaluation/gatewaytransport"
	"github.com/dewebprotocol/malt-core/auth/input"
	cid "github.com/ipfs/go-cid"
)

func TestApplyEvaluationFlatPrefixUsesSecretOnlyAndRequiresArcSetOnlyAccounting(t *testing.T) {
	root := cid.MustParse(validBootstrapCandidate(t).Candidate.Root)
	target := mustRawCID(t, "flat-target")
	secret := strings.Repeat("b", 64)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/evaluation/rq3/flat-prefix" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get(gatewaytransport.BootstrapAuthorizationTokenHeader) != secret ||
			request.Header.Get(gatewaytransport.InstanceTokenHeader) != "" {
			t.Fatalf("flat-prefix authorization headers = %#v", request.Header)
		}
		var body struct {
			Profile     string `json:"profile"`
			OperationID string `json:"operation_id"`
			Initial     bool   `json:"initial"`
			Changes     []struct {
				Input input.Value `json:"input"`
				After string      `json:"after"`
			} `json:"changes"`
		}
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Profile != gatewaytransport.FlatPrefixProfile || body.OperationID != "snapshot" ||
			!body.Initial || len(body.Changes) != 1 || string(body.Changes[0].Input.Data) != "rq3/file-sha256/abc" ||
			body.Changes[0].After != target.String() {
			t.Fatalf("flat-prefix request = %#v", body)
		}
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Cache-Control", "private, no-store")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"profile": gatewaytransport.FlatPrefixProfile, "root": root.String(),
			"validation_and_stage_nanos": uint64(11), "persist_nanos": uint64(13),
			"write_accounting": flatWriteAccounting(),
		})
	}))
	defer server.Close()

	result, err := newEvaluationClient(t, server.URL, 0).ApplyEvaluationFlatPrefix(t.Context(), secret, gatewaytransport.FlatPrefixMutation{
		OperationID: "snapshot", Initial: true,
		Changes: []gatewaytransport.FlatPrefixChange{{Input: input.LabelValue([]byte("rq3/file-sha256/abc")), After: target}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Root.Equals(root) || result.ValidationAndStageNanos != 11 || result.PersistNanos != 13 ||
		result.WriteAccounting.Categories[0].GrossNewBytes == 0 {
		t.Fatalf("flat-prefix result = %#v", result)
	}
}

func flatWriteAccounting() gatewaytransport.WriteAccounting {
	value := validWriteAccounting()
	value.Categories[1] = gatewaytransport.WriteCategoryAccounting{Category: "arctable-lineage-metadata"}
	value.Categories[2] = gatewaytransport.WriteCategoryAccounting{Category: "root-version-metadata"}
	return value
}

func TestApplyEvaluationFlatPrefixRejectsNonzeroDisabledCategoryCounter(t *testing.T) {
	root := cid.MustParse(validBootstrapCandidate(t).Candidate.Root)
	target := mustRawCID(t, "flat-target")
	secret := strings.Repeat("c", 64)
	accounting := flatWriteAccounting()
	accounting.Categories[1].AttemptedWrites = 1
	accounting.Categories[1].AttemptedSameValueWrites = 1
	accounting.Categories[1].AttemptedBytes = 17
	accounting.Categories[1].AttemptedSameValueBytes = 17
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"profile": gatewaytransport.FlatPrefixProfile, "root": root.String(),
			"validation_and_stage_nanos": uint64(1), "persist_nanos": uint64(1), "write_accounting": accounting,
		})
	}))
	defer server.Close()

	_, err := newEvaluationClient(t, server.URL, 0).ApplyEvaluationFlatPrefix(t.Context(), secret, gatewaytransport.FlatPrefixMutation{
		OperationID: "snapshot", Initial: true,
		Changes: []gatewaytransport.FlatPrefixChange{{Input: input.LabelValue([]byte("rq3/file-sha256/abc")), After: target}},
	})
	if err == nil || !strings.Contains(err.Error(), "non-ArcSet metadata") {
		t.Fatalf("disabled-category attempted same-value counter error = %v", err)
	}
}

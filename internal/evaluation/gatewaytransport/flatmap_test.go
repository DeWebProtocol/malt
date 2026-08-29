package gatewaytransport_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt-client/internal/evaluation/gatewaytransport"
	"github.com/dewebprotocol/malt-client/transport"
	"github.com/dewebprotocol/malt-core/auth/arcset"
)

func TestApplyEvaluationFlatMapUsesSecretOnlyAndRequiresArcSetOnlyAccounting(t *testing.T) {
	root := validBootstrapMapObject(t).ExpectedRoot
	target := mustRawCID(t, "flat-target")
	secret := strings.Repeat("b", 64)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/evaluation/rq3/flat-map" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get(gatewaytransport.BootstrapAuthorizationTokenHeader) != secret ||
			request.Header.Get(gatewaytransport.InstanceTokenHeader) != "" {
			t.Fatalf("flat-map authorization headers = %#v", request.Header)
		}
		var body struct {
			Profile     string `json:"profile"`
			OperationID string `json:"operation_id"`
			Initial     bool   `json:"initial"`
			Changes     []struct {
				Path  string `json:"path"`
				After string `json:"after"`
			} `json:"changes"`
		}
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Profile != gatewaytransport.FlatMapProfile || body.OperationID != "snapshot" ||
			!body.Initial || len(body.Changes) != 1 || body.Changes[0].Path != "rq3/file-sha256/abc" ||
			body.Changes[0].After != target.String() {
			t.Fatalf("flat-map request = %#v", body)
		}
		response.Header().Set("Content-Type", "application/json")
		response.Header().Set("Cache-Control", "private, no-store")
		_ = json.NewEncoder(response).Encode(map[string]any{
			"profile": gatewaytransport.FlatMapProfile, "root": root.String(),
			"replay_nanos": uint64(11), "persist_nanos": uint64(13),
			"write_accounting": flatWriteAccounting(),
		})
	}))
	defer server.Close()

	result, err := newEvaluationClient(t, server.URL, 0).ApplyEvaluationFlatMap(t.Context(), secret, gatewaytransport.FlatMapMutation{
		OperationID: "snapshot", Initial: true,
		Changes: []gatewaytransport.FlatMapChange{{Path: arcset.CanonicalizePath("rq3/file-sha256/abc"), After: target}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Root.Equals(root) || result.ReplayNanos != 11 || result.PersistNanos != 13 ||
		result.WriteAccounting.Categories[0].GrossNewBytes == 0 {
		t.Fatalf("flat-map result = %#v", result)
	}
}

func flatWriteAccounting() transport.ClientRootWriteAccounting {
	value := validWriteAccounting()
	value.Categories[1] = transport.ClientRootWriteCategoryAccounting{Category: "arctable-lineage-metadata"}
	value.Categories[2] = transport.ClientRootWriteCategoryAccounting{Category: "root-version-metadata"}
	return value
}

func TestApplyEvaluationFlatMapRejectsNonzeroDisabledCategoryCounter(t *testing.T) {
	root := validBootstrapMapObject(t).ExpectedRoot
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
			"profile": gatewaytransport.FlatMapProfile, "root": root.String(),
			"replay_nanos": uint64(1), "persist_nanos": uint64(1), "write_accounting": accounting,
		})
	}))
	defer server.Close()

	_, err := newEvaluationClient(t, server.URL, 0).ApplyEvaluationFlatMap(t.Context(), secret, gatewaytransport.FlatMapMutation{
		OperationID: "snapshot", Initial: true,
		Changes: []gatewaytransport.FlatMapChange{{Path: arcset.CanonicalizePath("rq3/file-sha256/abc"), After: target}},
	})
	if err == nil || !strings.Contains(err.Error(), "non-ArcSet metadata") {
		t.Fatalf("disabled-category attempted same-value counter error = %v", err)
	}
}

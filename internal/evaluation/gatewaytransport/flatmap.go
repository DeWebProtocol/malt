package gatewaytransport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/dewebprotocol/malt-client/transport"
	"github.com/dewebprotocol/malt-core/auth/arcset"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

const FlatMapProfile = "gateway.evaluation-flat-map-mutation/v1"

type FlatMapMutation struct {
	OperationID string
	BaseRoot    cid.Cid
	Initial     bool
	Changes     []FlatMapChange
}

type FlatMapChange struct {
	Path   arcset.Path
	Before cid.Cid
	After  cid.Cid
}

type FlatMapResult struct {
	Root            cid.Cid
	ReplayNanos     uint64
	PersistNanos    uint64
	WriteAccounting transport.ClientRootWriteAccounting
}

func (c *Client) ApplyEvaluationFlatMap(ctx context.Context, authorizationToken string, value FlatMapMutation) (FlatMapResult, error) {
	if !canonicalLowerSHA256(authorizationToken) {
		return FlatMapResult{}, fmt.Errorf("evaluation flat-map authorization token must be a canonical SHA-256")
	}
	if !validFlatMapOperationID(value.OperationID) || len(value.Changes) == 0 || len(value.Changes) > 65_536 {
		return FlatMapResult{}, fmt.Errorf("evaluation flat-map mutation is incomplete")
	}
	if value.Initial == value.BaseRoot.Defined() {
		return FlatMapResult{}, fmt.Errorf("evaluation flat-map base/initial relationship is invalid")
	}
	if value.BaseRoot.Defined() && (maltcid.SemanticKindOf(value.BaseRoot) != maltcid.SemanticKindMap ||
		maltcid.BackendKindOf(value.BaseRoot) != maltcid.BackendKindKZG) {
		return FlatMapResult{}, fmt.Errorf("evaluation flat-map base is not a KZG Map")
	}
	type wireChange struct {
		Path   string `json:"path"`
		Before string `json:"before,omitempty"`
		After  string `json:"after,omitempty"`
	}
	body := struct {
		Profile     string       `json:"profile"`
		OperationID string       `json:"operation_id"`
		BaseRoot    string       `json:"base_root,omitempty"`
		Initial     bool         `json:"initial"`
		Changes     []wireChange `json:"changes"`
	}{
		Profile: FlatMapProfile, OperationID: value.OperationID, Initial: value.Initial,
		Changes: make([]wireChange, len(value.Changes)),
	}
	if value.BaseRoot.Defined() {
		body.BaseRoot = value.BaseRoot.String()
	}
	paths := make([]string, len(value.Changes))
	for index, change := range value.Changes {
		if change.Path.IsEmpty() || (!change.Before.Defined() && !change.After.Defined()) ||
			(change.Before.Defined() && change.After.Defined() && change.Before.Equals(change.After)) ||
			value.Initial && (change.Before.Defined() || !change.After.Defined()) {
			return FlatMapResult{}, fmt.Errorf("evaluation flat-map change %d is invalid", index)
		}
		paths[index] = change.Path.String()
		body.Changes[index].Path = paths[index]
		if change.Before.Defined() {
			body.Changes[index].Before = change.Before.String()
		}
		if change.After.Defined() {
			body.Changes[index].After = change.After.String()
		}
	}
	if !slices.IsSorted(paths) {
		return FlatMapResult{}, fmt.Errorf("evaluation flat-map changes are not sorted")
	}
	for index := 1; index < len(paths); index++ {
		if paths[index] == paths[index-1] {
			return FlatMapResult{}, fmt.Errorf("evaluation flat-map changes repeat a path")
		}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return FlatMapResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/v1/evaluation/rq3/flat-map"), bytes.NewReader(encoded))
	if err != nil {
		return FlatMapResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(BootstrapAuthorizationTokenHeader, authorizationToken)
	response, err := c.plainHTTP.Do(request)
	if err != nil {
		return FlatMapResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return FlatMapResult{}, c.responseError(response)
	}
	if err := requireJSONNoStore(response); err != nil {
		return FlatMapResult{}, err
	}
	raw, err := readBounded(response.Body, c.maxJSONResponseBytes, "Gateway evaluation flat-map response")
	if err != nil {
		return FlatMapResult{}, err
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return FlatMapResult{}, fmt.Errorf("Gateway evaluation flat-map response: %w", err)
	}
	var wire struct {
		Profile         string                              `json:"profile"`
		Root            string                              `json:"root"`
		ReplayNanos     uint64                              `json:"replay_nanos"`
		PersistNanos    uint64                              `json:"persist_nanos"`
		WriteAccounting transport.ClientRootWriteAccounting `json:"write_accounting"`
	}
	if err := decodeStrict(raw, &wire); err != nil {
		return FlatMapResult{}, fmt.Errorf("decode Gateway evaluation flat-map response: %w", err)
	}
	root, err := cid.Parse(wire.Root)
	if err != nil || wire.Profile != FlatMapProfile || !root.Defined() ||
		maltcid.SemanticKindOf(root) != maltcid.SemanticKindMap || maltcid.BackendKindOf(root) != maltcid.BackendKindKZG {
		return FlatMapResult{}, fmt.Errorf("Gateway evaluation flat-map returned an invalid root")
	}
	if err := validateWriteAccounting(wire.WriteAccounting); err != nil || !wire.WriteAccounting.Available ||
		strings.TrimSpace(wire.WriteAccounting.UnavailableReason) != "" {
		return FlatMapResult{}, fmt.Errorf("Gateway evaluation flat-map returned invalid exact write accounting")
	}
	if len(wire.WriteAccounting.Categories) != 3 ||
		wire.WriteAccounting.Categories[1] != (transport.ClientRootWriteCategoryAccounting{Category: "arctable-lineage-metadata"}) ||
		wire.WriteAccounting.Categories[2] != (transport.ClientRootWriteCategoryAccounting{Category: "root-version-metadata"}) {
		return FlatMapResult{}, fmt.Errorf("Gateway evaluation flat-map persisted non-ArcSet metadata")
	}
	return FlatMapResult{
		Root: root, ReplayNanos: wire.ReplayNanos, PersistNanos: wire.PersistNanos,
		WriteAccounting: wire.WriteAccounting,
	}, nil
}

func validFlatMapOperationID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for index, character := range value {
		if character >= 0x61 && character <= 0x7a || character >= 0x30 && character <= 0x39 ||
			index > 0 && (character == 0x2e || character == 0x5f || character == 0x2d) {
			continue
		}
		return false
	}
	return true
}

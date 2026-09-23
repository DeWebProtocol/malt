package gatewaytransport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/dewebprotocol/malt-core/derivation"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

const FlatPrefixProfile = "gateway.evaluation-flat-prefix-mutation/1"

type FlatPrefixMutation struct {
	OperationID string
	BaseRoot    cid.Cid
	Initial     bool
	Changes     []FlatPrefixChange
}

type FlatPrefixChange struct {
	Label  []byte
	Before cid.Cid
	After  cid.Cid
}

type FlatPrefixResult struct {
	Root                    cid.Cid
	ValidationAndStageNanos uint64
	PersistNanos            uint64
	WriteAccounting         WriteAccounting
}

func (c *Client) ApplyEvaluationFlatPrefix(ctx context.Context, authorizationToken string, value FlatPrefixMutation) (FlatPrefixResult, error) {
	if !canonicalLowerSHA256(authorizationToken) {
		return FlatPrefixResult{}, fmt.Errorf("evaluation flat-prefix authorization token must be a canonical SHA-256")
	}
	if !validFlatPrefixOperationID(value.OperationID) || len(value.Changes) == 0 || len(value.Changes) > 65_536 {
		return FlatPrefixResult{}, fmt.Errorf("evaluation flat-prefix mutation is incomplete")
	}
	if value.Initial == value.BaseRoot.Defined() {
		return FlatPrefixResult{}, fmt.Errorf("evaluation flat-prefix base/initial relationship is invalid")
	}
	if value.BaseRoot.Defined() && (!currentFlatPrefixRoot(value.BaseRoot)) {
		return FlatPrefixResult{}, fmt.Errorf("evaluation flat-prefix base is not a KZG Prefix/AA=1 Root")
	}
	type wireChange struct {
		Label  []byte `json:"label"`
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
		Profile: FlatPrefixProfile, OperationID: value.OperationID, Initial: value.Initial,
		Changes: make([]wireChange, len(value.Changes)),
	}
	if value.BaseRoot.Defined() {
		body.BaseRoot = value.BaseRoot.String()
	}
	paths := make([]string, len(value.Changes))
	for index, change := range value.Changes {
		if len(change.Label) == 0 || (!change.Before.Defined() && !change.After.Defined()) ||
			(change.Before.Defined() && change.After.Defined() && change.Before.Equals(change.After)) ||
			value.Initial && (change.Before.Defined() || !change.After.Defined()) {
			return FlatPrefixResult{}, fmt.Errorf("evaluation flat-prefix change %d is invalid", index)
		}
		paths[index] = string(change.Label)
		body.Changes[index].Label = change.Label
		if change.Before.Defined() {
			body.Changes[index].Before = change.Before.String()
		}
		if change.After.Defined() {
			body.Changes[index].After = change.After.String()
		}
	}
	if !slices.IsSorted(paths) {
		return FlatPrefixResult{}, fmt.Errorf("evaluation flat-prefix changes are not sorted")
	}
	for index := 1; index < len(paths); index++ {
		if paths[index] == paths[index-1] {
			return FlatPrefixResult{}, fmt.Errorf("evaluation flat-prefix changes repeat a path")
		}
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		return FlatPrefixResult{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/v1/evaluation/rq3/flat-prefix"), bytes.NewReader(encoded))
	if err != nil {
		return FlatPrefixResult{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(BootstrapAuthorizationTokenHeader, authorizationToken)
	response, err := c.plainHTTP.Do(request)
	if err != nil {
		return FlatPrefixResult{}, err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return FlatPrefixResult{}, c.responseError(response)
	}
	if err := requireJSONNoStore(response); err != nil {
		return FlatPrefixResult{}, err
	}
	raw, err := readBounded(response.Body, c.maxJSONResponseBytes, "Gateway evaluation flat-prefix response")
	if err != nil {
		return FlatPrefixResult{}, err
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return FlatPrefixResult{}, fmt.Errorf("Gateway evaluation flat-prefix response: %w", err)
	}
	var wire struct {
		Profile                 string          `json:"profile"`
		Root                    string          `json:"root"`
		ValidationAndStageNanos uint64          `json:"validation_and_stage_nanos"`
		PersistNanos            uint64          `json:"persist_nanos"`
		WriteAccounting         WriteAccounting `json:"write_accounting"`
	}
	if err := decodeStrict(raw, &wire); err != nil {
		return FlatPrefixResult{}, fmt.Errorf("decode Gateway evaluation flat-prefix response: %w", err)
	}
	root, err := cid.Parse(wire.Root)
	if err != nil || wire.Profile != FlatPrefixProfile || !root.Defined() ||
		!currentFlatPrefixRoot(root) {
		return FlatPrefixResult{}, fmt.Errorf("Gateway evaluation flat-prefix returned an invalid root")
	}
	if err := validateWriteAccounting(wire.WriteAccounting); err != nil || !wire.WriteAccounting.Available ||
		strings.TrimSpace(wire.WriteAccounting.UnavailableReason) != "" {
		return FlatPrefixResult{}, fmt.Errorf("Gateway evaluation flat-prefix returned invalid exact write accounting")
	}
	if len(wire.WriteAccounting.Categories) != 3 ||
		wire.WriteAccounting.Categories[1] != (WriteCategoryAccounting{Category: "arctable-lineage-metadata"}) ||
		wire.WriteAccounting.Categories[2] != (WriteCategoryAccounting{Category: "root-version-metadata"}) {
		return FlatPrefixResult{}, fmt.Errorf("Gateway evaluation flat-prefix persisted non-ArcSet metadata")
	}
	return FlatPrefixResult{
		Root: root, ValidationAndStageNanos: wire.ValidationAndStageNanos, PersistNanos: wire.PersistNanos,
		WriteAccounting: wire.WriteAccounting,
	}, nil
}

func validFlatPrefixOperationID(value string) bool {
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

func currentFlatPrefixRoot(root cid.Cid) bool {
	d, _, err := maltcid.ParseRoot(root)
	return err == nil && d.Layout == maltcid.Prefix && d.DerivationProfile == uint8(derivation.SHA256) && d.Profile == maltcid.KZG4096
}

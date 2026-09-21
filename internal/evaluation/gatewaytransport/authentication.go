package gatewaytransport

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

const (
	AuthenticationPhaseProfile    = "gateway.authentication-phases/0"
	AuthenticationDurableBoundary = "gateway.evaluation-authentication-kv-atomic/0"
	authenticationHeaderPrefix    = "X-Malt-Authentication-"
)

// These observations describe one evaluation request. They are neither
// authentication evidence nor a root-publication or local trust decision.
type PhaseMetrics struct {
	ValidationAndStageNS uint64
	PersistNS            uint64
	ReceiptNS            uint64
}
type CandidateResponse struct {
	Candidate protocol.AuthenticationCandidate
	WireBytes uint64
}
type BatchResponse struct {
	Receipt                  protocol.AuthenticationReceipt
	RequestWireBytes         uint64
	ResponseWireBytes        uint64
	RequestEncodingNS        uint64
	ResponseVerifyNS         uint64
	Idempotent               bool
	Gateway                  PhaseMetrics
	WriteAccounting          WriteAccounting
	WriteAccountingWireBytes uint64
}

func (c *Client) AuthenticationCandidate(ctx context.Context, root cid.Cid) (CandidateResponse, error) {
	if _, _, err := maltcid.ParseRoot(root); err != nil {
		return CandidateResponse{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.endpoint("/v1/evaluation/authentication/candidates/"+url.PathEscape(root.String())), nil)
	if err != nil {
		return CandidateResponse{}, err
	}
	response, err := c.instanceHTTP.Do(req)
	if err != nil {
		return CandidateResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return CandidateResponse{}, c.responseError(response)
	}
	if err := requireJSONNoStore(response); err != nil {
		return CandidateResponse{}, err
	}
	raw, err := readBounded(response.Body, min(c.maxJSONResponseBytes, int64(protocol.MaxVerificationJSONBytes)), "evaluation authentication candidate")
	if err != nil {
		return CandidateResponse{}, err
	}
	candidate, err := protocol.DecodeAuthenticationCandidate(raw)
	if err != nil {
		return CandidateResponse{}, err
	}
	if candidate.Root != root.String() {
		return CandidateResponse{}, fmt.Errorf("candidate differs from caller-selected Root")
	}
	return CandidateResponse{Candidate: candidate, WireBytes: uint64(len(raw))}, nil
}

// SubmitAuthenticationBatch validates the operational receipt against the
// exact submitted bytes/Roots/transaction. Local acceptance remains separate.
func (c *Client) SubmitAuthenticationBatch(ctx context.Context, batch protocol.AuthenticationBatch) (BatchResponse, error) {
	started := time.Now()
	if err := batch.Validate(); err != nil {
		return BatchResponse{}, err
	}
	raw, err := json.Marshal(batch)
	if err != nil {
		return BatchResponse{}, err
	}
	if len(raw) > protocol.MaxVerificationJSONBytes {
		return BatchResponse{}, fmt.Errorf("authentication batch exceeds protocol limit")
	}
	encodingNS := uint64(time.Since(started).Nanoseconds())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint("/v1/evaluation/authentication/batches"), bytes.NewReader(raw))
	if err != nil {
		return BatchResponse{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.instanceHTTP.Do(request)
	if err != nil {
		return BatchResponse{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusCreated {
		return BatchResponse{}, c.responseError(response)
	}
	if err := requireJSONNoStore(response); err != nil {
		return BatchResponse{}, err
	}
	for name, want := range map[string]string{"Phase-Profile": AuthenticationPhaseProfile, "Publication": "separate", "Trust": "client-owned", "Durable-Boundary": AuthenticationDurableBoundary} {
		if value, err := oneHeader(response.Header, authenticationHeaderPrefix+name); err != nil || value != want {
			return BatchResponse{}, fmt.Errorf("unexpected authentication %s boundary", name)
		}
	}
	phases, err := parseAuthenticationPhases(response.Header)
	if err != nil {
		return BatchResponse{}, err
	}
	accounting, accountingBytes, err := parseAuthenticationAccounting(response.Header)
	if err != nil {
		return BatchResponse{}, err
	}
	idempotent, err := oneHeader(response.Header, authenticationHeaderPrefix+"Idempotent")
	if err != nil || idempotent != "true" && idempotent != "false" {
		return BatchResponse{}, fmt.Errorf("missing canonical authentication idempotency classification")
	}
	encodedReceipt, err := readBounded(response.Body, min(c.maxJSONResponseBytes, int64(protocol.MaxVerificationJSONBytes)), "evaluation authentication receipt")
	if err != nil {
		return BatchResponse{}, err
	}
	verifyStarted := time.Now()
	receipt, err := protocol.DecodeAuthenticationReceipt(encodedReceipt)
	if err != nil {
		return BatchResponse{}, err
	}
	if err := receipt.Validate(batch); err != nil {
		return BatchResponse{}, err
	}
	if receipt.DurableBoundary != AuthenticationDurableBoundary {
		return BatchResponse{}, fmt.Errorf("receipt differs from evaluation durable boundary")
	}
	return BatchResponse{Receipt: receipt, RequestWireBytes: uint64(len(raw)), ResponseWireBytes: uint64(len(encodedReceipt)), RequestEncodingNS: encodingNS, ResponseVerifyNS: uint64(time.Since(verifyStarted).Nanoseconds()), Idempotent: idempotent == "true", Gateway: phases, WriteAccounting: accounting, WriteAccountingWireBytes: accountingBytes}, nil
}

func oneHeader(header http.Header, name string) (string, error) {
	values := header.Values(name)
	if len(values) != 1 || values[0] == "" || strings.TrimSpace(values[0]) != values[0] {
		return "", fmt.Errorf("missing single canonical %s header", name)
	}
	return values[0], nil
}
func parseAuthenticationPhases(header http.Header) (PhaseMetrics, error) {
	var result PhaseMetrics
	for name, out := range map[string]*uint64{"Validation-And-Stage-Nanos": &result.ValidationAndStageNS, "Persist-Nanos": &result.PersistNS, "Receipt-Nanos": &result.ReceiptNS} {
		raw, err := oneHeader(header, authenticationHeaderPrefix+name)
		if err != nil {
			return result, err
		}
		n, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || strconv.FormatUint(n, 10) != raw {
			return result, fmt.Errorf("invalid canonical %s", name)
		}
		*out = n
	}
	names := map[string]bool{}
	for _, value := range header.Values("Server-Timing") {
		for _, item := range strings.Split(value, ",") {
			names[strings.TrimSpace(strings.SplitN(item, ";", 2)[0])] = true
		}
	}
	for _, name := range []string{"authentication-validate-stage", "persist", "receipt"} {
		if !names[name] {
			return result, fmt.Errorf("Server-Timing omits %s", name)
		}
	}
	return result, nil
}
func parseAuthenticationAccounting(header http.Header) (WriteAccounting, uint64, error) {
	var value WriteAccounting
	encoded, err := oneHeader(header, authenticationHeaderPrefix+"Write-Accounting")
	if err != nil {
		return value, 0, err
	}
	if len(encoded) > base64.RawURLEncoding.EncodedLen(4096) {
		return value, 0, fmt.Errorf("authentication accounting header exceeds limit")
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil || len(raw) == 0 || len(raw) > 4096 {
		return value, 0, fmt.Errorf("accounting is not bounded raw URL base64")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return value, 0, err
	}
	if err := decodeStrict(raw, &value); err != nil {
		return value, 0, err
	}
	if err := validateWriteAccounting(value); err != nil {
		return value, 0, err
	}
	// Availability is explicit. A measurement consumer must require it; the
	// writer correctness oracle intentionally makes no durable byte claim.
	return value, uint64(len(raw)), nil
}

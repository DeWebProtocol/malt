package gatewaytransport_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt-client/internal/evaluation/gatewaytransport"
	"github.com/dewebprotocol/malt-core/protocol"
	cid "github.com/ipfs/go-cid"
)

func TestEvaluationAuthenticationCandidateBindsSelectedRootAndCurrentWire(t *testing.T) {
	candidate := validBootstrapCandidate(t).Candidate
	for _, mode := range []string{"valid", "wrong-root", "old-profile", "duplicate-key", "cacheable"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/v1/evaluation/authentication/candidates/"+candidate.Root || r.Header.Get(gatewaytransport.InstanceTokenHeader) != testInstanceToken || r.Header.Get(gatewaytransport.BootstrapAuthorizationTokenHeader) != "" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				value := candidate
				switch mode {
				case "wrong-root":
					value.Root = mustRawCID(t, "wrong-root").String()
				case "old-profile":
					value.Profile = "malt.update-view/v2"
				case "cacheable":
					w.Header().Set("Cache-Control", "public, max-age=3600")
				}
				raw, err := json.Marshal(value)
				if err != nil {
					t.Error(err)
					return
				}
				if mode == "duplicate-key" {
					raw = bytes.Replace(raw, []byte(`"profile":`), []byte(`"profile":"malt.authentication/2","profile":`), 1)
				}
				_, _ = w.Write(raw)
			}))
			defer server.Close()
			result, err := newEvaluationClient(t, server.URL, 0).AuthenticationCandidate(t.Context(), cid.MustParse(candidate.Root))
			if mode == "valid" {
				if err != nil || result.Candidate.Root != candidate.Root || result.WireBytes == 0 {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else if err == nil {
				t.Fatal("hostile candidate accepted")
			}
		})
	}
}

func TestEvaluationAuthenticationBatchRequiresExactReceiptAndDiagnostics(t *testing.T) {
	candidate := validBootstrapCandidate(t).Candidate
	batch := protocol.AuthenticationBatch{Profile: protocol.AuthenticationBatchProfile, TransactionID: "typed-write-1", Base: candidate.Root, Root: candidate.Root, Candidates: []protocol.AuthenticationCandidate{candidate}}
	digest, err := batch.Digest()
	if err != nil {
		t.Fatal(err)
	}
	baseReceipt := protocol.AuthenticationReceipt{Profile: protocol.AuthenticationReceiptProfile, TransactionID: batch.TransactionID, Base: batch.Base, Root: batch.Root, Digest: digest, DurableBoundary: gatewaytransport.AuthenticationDurableBoundary}
	for _, mode := range []string{"valid", "retry", "receipt-root", "receipt-base", "receipt-id", "receipt-digest", "receipt-profile", "receipt-boundary", "duplicate-receipt", "header-boundary", "missing-phase", "duplicate-phase", "noncanonical-nanos", "missing-timing", "accounting-profile", "accounting-overflow", "duplicate-accounting", "cacheable"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/v1/evaluation/authentication/batches" || r.Header.Get(gatewaytransport.InstanceTokenHeader) != testInstanceToken || r.Header.Get(gatewaytransport.BootstrapAuthorizationTokenHeader) != "" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
				}
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
					return
				}
				submitted, err := protocol.DecodeAuthenticationBatch(raw)
				if err != nil {
					t.Error(err)
					return
				}
				if submittedDigest, err := submitted.Digest(); err != nil || submittedDigest != digest {
					t.Errorf("changed batch digest=%s err=%v", submittedDigest, err)
				}
				receipt := baseReceipt
				accounting := validWriteAccounting()
				prefix := "X-Malt-Authentication-"
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				for k, v := range map[string]string{"Phase-Profile": gatewaytransport.AuthenticationPhaseProfile, "Publication": "separate", "Trust": "client-owned", "Durable-Boundary": gatewaytransport.AuthenticationDurableBoundary, "Validation-And-Stage-Nanos": "11", "Persist-Nanos": "12", "Receipt-Nanos": "13", "Idempotent": "false"} {
					w.Header().Set(prefix+k, v)
				}
				w.Header().Set("Server-Timing", "authentication-validate-stage;dur=0.001, persist;dur=0.001, receipt;dur=0.001")
				switch mode {
				case "retry":
					w.Header().Set(prefix+"Idempotent", "true")
				case "receipt-root":
					receipt.Root = mustRawCID(t, "root").String()
				case "receipt-base":
					receipt.Base = mustRawCID(t, "base").String()
				case "receipt-id":
					receipt.TransactionID = "another-write"
				case "receipt-digest":
					receipt.Digest = strings.Repeat("0", 64)
				case "receipt-profile":
					receipt.Profile = "malt.materialization-receipt/v2"
				case "receipt-boundary":
					receipt.DurableBoundary = "volatile"
				case "header-boundary":
					w.Header().Set(prefix+"Durable-Boundary", "volatile")
				case "missing-phase":
					w.Header().Del(prefix + "Persist-Nanos")
				case "duplicate-phase":
					w.Header().Add(prefix+"Persist-Nanos", "12")
				case "noncanonical-nanos":
					w.Header().Set(prefix+"Persist-Nanos", "012")
				case "missing-timing":
					w.Header().Del("Server-Timing")
				case "accounting-profile":
					accounting.Profile = "gateway.client-root-write-accounting/v2"
				case "accounting-overflow":
					accounting.Categories[0].AttemptedReplacementWrites = math.MaxUint64
					accounting.Categories[0].AttemptedWrites = 0
				case "cacheable":
					w.Header().Set("Cache-Control", "public")
				}
				accountingJSON, err := json.Marshal(accounting)
				if err != nil {
					t.Error(err)
					return
				}
				if mode == "duplicate-accounting" {
					accountingJSON = bytes.Replace(accountingJSON, []byte(`"available":`), []byte(`"available":true,"available":`), 1)
				}
				w.Header().Set(prefix+"Write-Accounting", base64.RawURLEncoding.EncodeToString(accountingJSON))
				receiptJSON, err := json.Marshal(receipt)
				if err != nil {
					t.Error(err)
					return
				}
				if mode == "duplicate-receipt" {
					receiptJSON = bytes.Replace(receiptJSON, []byte(`"root":`), []byte(`"root":"ignored","root":`), 1)
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write(receiptJSON)
			}))
			defer server.Close()
			result, err := newEvaluationClient(t, server.URL, 0).SubmitAuthenticationBatch(t.Context(), batch)
			if mode == "valid" || mode == "retry" {
				if err != nil || result.Receipt != baseReceipt || result.Gateway.ValidationAndStageNS != 11 || result.Gateway.PersistNS != 12 || result.Gateway.ReceiptNS != 13 || result.WriteAccountingWireBytes == 0 || result.RequestWireBytes == 0 || result.ResponseWireBytes == 0 || result.Idempotent != (mode == "retry") {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else if err == nil {
				t.Fatal("hostile receipt/diagnostics accepted")
			}
		})
	}
}

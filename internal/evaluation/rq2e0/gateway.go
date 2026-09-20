package rq2e0

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"time"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	"github.com/dewebprotocol/malt-client/internal/evaluation/gatewaytransport"
	"github.com/dewebprotocol/malt-client/internal/evaluation/rq2fixture"
	clienttransport "github.com/dewebprotocol/malt-client/transport"
	"github.com/dewebprotocol/malt-core/auth/engine"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

// ConformanceGateway is an in-memory HTTP oracle for writer correctness only.
// Actual durability, byte accounting and snapshot E0 use the separately tested
// Gateway executable. This oracle emits unavailable write accounting and may
// never be used as a paper performance or persistence measurement source.
type ConformanceGateway struct {
	mu         sync.Mutex
	token      string
	candidates map[string]protocol.AuthenticationCandidate
	payloads   map[string]bool
	receipts   map[string]protocol.AuthenticationReceipt
	engine     *engine.Engine
	server     *httptest.Server
	operations uint64
}

func NewConformanceGateway(fixture *rq2fixture.Fixture, backend, token string) (*ConformanceGateway, cid.Cid, error) {
	if fixture == nil || len(token) != 64 {
		return nil, cid.Undef, errors.New("invalid RQ2 writer conformance input")
	}
	e, err := fixtureEngine(backend)
	if err != nil {
		return nil, cid.Undef, err
	}
	candidates, err := fixture.Candidates(context.Background(), e, backend)
	if err != nil {
		return nil, cid.Undef, err
	}
	g := &ConformanceGateway{token: token, engine: e, candidates: map[string]protocol.AuthenticationCandidate{}, payloads: map[string]bool{}, receipts: map[string]protocol.AuthenticationReceipt{}}
	for _, c := range candidates {
		g.candidates[c.Root] = c
	}
	for _, f := range fixture.DirectFiles {
		g.payloads[f.CID] = true
	}
	for _, f := range fixture.ListFiles {
		for _, chunk := range f.Chunks {
			g.payloads[chunk.CID] = true
		}
	}
	g.server = httptest.NewServer(g)
	root, _ := cid.Parse(candidates[len(candidates)-1].Root)
	return g, root, nil
}
func (g *ConformanceGateway) URL() string { return g.server.URL }

func (g *ConformanceGateway) Close() {
	if g != nil && g.server != nil {
		g.server.Close()
		g.server = nil
	}
}

func (g *ConformanceGateway) Operations() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.operations
}

func (g *ConformanceGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	if r.Header.Get(gatewaytransport.InstanceTokenHeader) != g.token {
		http.Error(w, "incorrect instance token", http.StatusUnauthorized)
		return
	}
	switch {
	case r.URL.Path == "/healthz" && r.Method == http.MethodGet:
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "evaluation_instance_token": g.token})
	case strings.HasPrefix(r.URL.Path, "/v1/evaluation/authentication/candidates/") && r.Method == http.MethodGet:
		root := strings.TrimPrefix(r.URL.Path, "/v1/evaluation/authentication/candidates/")
		g.mu.Lock()
		candidate, ok := g.candidates[root]
		g.mu.Unlock()
		if !ok {
			http.Error(w, "unknown Root", http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(candidate)
	case r.URL.Path == "/v1/cas/batch" && r.Method == http.MethodPost:
		g.handleCASBatch(w, r)
	case r.URL.Path == "/v1/evaluation/authentication/batches" && r.Method == http.MethodPost:
		g.handleBatch(w, r)
	default:
		http.NotFound(w, r)
	}
}
func (g *ConformanceGateway) handleCASBatch(w http.ResponseWriter, r *http.Request) {
	var submitted struct {
		Profile string `json:"profile"`
		Blocks  []struct {
			Codec uint64 `json:"codec"`
			Data  []byte `json:"data"`
		} `json:"blocks"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, protocol.MaxVerificationJSONBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&submitted); err != nil || submitted.Profile != clienttransport.CASPutBatchProfile || len(submitted.Blocks) == 0 || len(submitted.Blocks) > 64 {
		http.Error(w, "invalid CAS batch", http.StatusBadRequest)
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		http.Error(w, "trailing JSON", http.StatusBadRequest)
		return
	}
	keys := make([]cid.Cid, len(submitted.Blocks))
	results := make([]map[string]string, len(keys))
	for i, b := range submitted.Blocks {
		key, err := clientcas.CIDForBlock(clientcas.Block{Codec: b.Codec, Data: b.Data})
		if err != nil || b.Codec != cid.Raw {
			http.Error(w, "invalid raw payload", http.StatusBadRequest)
			return
		}
		keys[i] = key
		results[i] = map[string]string{"cid": key.String(), "status": "stored"}
	}
	g.mu.Lock()
	for _, key := range keys {
		g.payloads[key.String()] = true
	}
	g.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"profile": clienttransport.CASPutBatchProfile, "results": results})
}
func (g *ConformanceGateway) handleBatch(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, protocol.MaxVerificationJSONBytes))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	batch, err := protocol.DecodeAuthenticationBatch(raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	digest, err := batch.Digest()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if receipt, exists := g.receipts[batch.TransactionID]; exists {
		if receipt.Digest != digest {
			http.Error(w, "transaction substitution", http.StatusConflict)
			return
		}
		g.writeReceipt(w, receipt, true, uint64(time.Since(started)), 0, 0)
		return
	}
	if _, ok := g.candidates[batch.Base]; !ok {
		http.Error(w, "unknown batch base", http.StatusConflict)
		return
	}
	if err := authentication.ValidateBatch(r.Context(), g.engine, batch); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	staged := map[string]bool{}
	for _, candidate := range batch.Candidates {
		if candidate.Previous != "" {
			if _, ok := g.candidates[candidate.Previous]; !ok && !staged[candidate.Previous] {
				http.Error(w, "missing previous Root", http.StatusBadRequest)
				return
			}
		}
		for _, entry := range candidate.State.Entries {
			key := entry.Target.String()
			if _, _, err := maltcid.ParseRoot(entry.Target); err == nil {
				if _, ok := g.candidates[key]; !ok && !staged[key] {
					http.Error(w, "missing child Root", http.StatusBadRequest)
					return
				}
			} else if !g.payloads[key] {
				http.Error(w, "missing payload", http.StatusBadRequest)
				return
			}
		}
		staged[candidate.Root] = true
	}
	validation := uint64(time.Since(started))
	started = time.Now()
	for _, candidate := range batch.Candidates {
		g.candidates[candidate.Root] = candidate
	}
	g.operations++
	persist := uint64(time.Since(started))
	started = time.Now()
	receipt := protocol.AuthenticationReceipt{Profile: protocol.AuthenticationReceiptProfile, TransactionID: batch.TransactionID, Base: batch.Base, Root: batch.Root, Digest: digest, DurableBoundary: gatewaytransport.AuthenticationDurableBoundary}
	g.receipts[batch.TransactionID] = receipt
	g.writeReceipt(w, receipt, false, validation, persist, uint64(time.Since(started)))
}
func (g *ConformanceGateway) writeReceipt(w http.ResponseWriter, receipt protocol.AuthenticationReceipt, retry bool, validation, persist, receiptNS uint64) {
	prefix := "X-Malt-Authentication-"
	for name, value := range map[string]string{"Phase-Profile": gatewaytransport.AuthenticationPhaseProfile, "Publication": "separate", "Trust": "client-owned", "Durable-Boundary": receipt.DurableBoundary, "Idempotent": strconv.FormatBool(retry), "Validation-And-Stage-Nanos": strconv.FormatUint(validation, 10), "Persist-Nanos": strconv.FormatUint(persist, 10), "Receipt-Nanos": strconv.FormatUint(receiptNS, 10)} {
		w.Header().Set(prefix+name, value)
	}
	w.Header().Set("Server-Timing", fmt.Sprintf("authentication-validate-stage;dur=%.6f, persist;dur=%.6f, receipt;dur=%.6f", float64(validation)/1e6, float64(persist)/1e6, float64(receiptNS)/1e6))
	accounting, _ := json.Marshal(gatewaytransport.WriteAccounting{Profile: gatewaytransport.WriteAccountingProfile, Available: false, UnavailableReason: "writer-conformance-e0-no-durable-accounting", ByteMethod: gatewaytransport.WriteByteMethod, Categories: []gatewaytransport.WriteCategoryAccounting{}})
	w.Header().Set(prefix+"Write-Accounting", base64.RawURLEncoding.EncodeToString(accounting))
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(receipt)
}

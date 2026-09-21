//go:build js && wasm

// Command malt-eval-rq2-browser-wasm runs retained typed authentication Writers
// inside a browser. Its Promise-based JSON RPC keeps candidate verification,
// payload CID binding, exact receipt validation, and retained-root state live
// inside WebAssembly rather than in the native browser driver.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall/js"
	"time"

	"github.com/dewebprotocol/malt-client/internal/evaluation/authenticationgraph"
	"github.com/dewebprotocol/malt-client/internal/evaluation/gatewaytransport"
	"github.com/dewebprotocol/malt-client/internal/evaluation/rq2fixture"
	"github.com/dewebprotocol/malt-client/internal/evaluation/rq2metrics"
	"github.com/dewebprotocol/malt-client/internal/evaluation/rq2wire"
	"github.com/dewebprotocol/malt-client/internal/evaluation/rq2write"
	"github.com/dewebprotocol/malt-client/transport"
	"github.com/dewebprotocol/malt-core/auth/commitment/ipa"
	"github.com/dewebprotocol/malt-core/auth/commitment/kzg"
	"github.com/dewebprotocol/malt-core/auth/engine"
	"github.com/dewebprotocol/malt-core/auth/input"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

const (
	maxBrowserFixtureBytes = 32 << 20
)

var (
	registeredFunctions []js.Func
	state               browserWriter
)

type initializeRequest struct {
	GatewayBaseURL       string `json:"gateway_base_url"`
	GatewayInstanceToken string `json:"gateway_instance_token"`
	FixtureURL           string `json:"fixture_url"`
	Backend              string `json:"backend"`
}

type initializeResponse struct {
	ParameterLoadNS    uint64 `json:"parameter_load_ns"`
	ParameterLoadBytes uint64 `json:"parameter_load_bytes"`
	ParameterProfile   string `json:"parameter_profile"`
	ParameterSHA256    string `json:"parameter_sha256"`
	FixtureBytes       uint64 `json:"fixture_bytes"`
	FixtureSHA256      string `json:"fixture_sha256"`
}

type browserWriter struct {
	initialized  bool
	backend      string
	remote       *transport.Client
	app          *authenticationgraph.Session
	fixture      *rq2fixture.Fixture
	source       map[string][]byte
	root         cid.Cid
	serial       uint64
	receiptCount uint64
	sessionID    string
}

func main() {
	initialize := js.FuncOf(func(_ js.Value, args []js.Value) any {
		return newPromise(args, func(raw string) (string, error) {
			var request initializeRequest
			if err := strictJSON([]byte(raw), &request); err != nil {
				return "", err
			}
			result, err := state.initialize(request)
			if err != nil {
				return "", err
			}
			return marshalJSON(result)
		})
	})
	exchange := js.FuncOf(func(_ js.Value, args []js.Value) any {
		return newPromise(args, func(raw string) (string, error) {
			started := time.Now()
			var request rq2wire.WorkerRequest
			if err := strictJSON([]byte(raw), &request); err != nil {
				return "", err
			}
			if err := request.Validate(); err != nil {
				return "", err
			}
			record := state.exchange(request)
			result, err := marshalJSON(record)
			js.Global().Set("maltRQ2LastExecutionNS", durationNS(time.Since(started)))
			return result, err
		})
	})
	registeredFunctions = append(registeredFunctions, initialize, exchange)
	js.Global().Set("maltRQ2Initialize", initialize)
	js.Global().Set("maltRQ2Exchange", exchange)
	js.Global().Set("maltRQ2LastExecutionNS", uint64(0))
	js.Global().Set("maltRQ2WASMReady", true)
	select {}
}

func newPromise(args []js.Value, run func(string) (string, error)) js.Value {
	promise := js.Global().Get("Promise")
	if len(args) != 1 || args[0].Type() != js.TypeString {
		return promise.Call("reject", "RQ2 WASM RPC requires one JSON string")
	}
	raw := args[0].String()
	var executor js.Func
	executor = js.FuncOf(func(_ js.Value, callbacks []js.Value) any {
		resolve, reject := callbacks[0], callbacks[1]
		go func() {
			result, err := run(raw)
			if err != nil {
				reject.Invoke(err.Error())
				return
			}
			resolve.Invoke(result)
		}()
		return nil
	})
	value := promise.New(executor)
	executor.Release()
	return value
}

func (w *browserWriter) initialize(request initializeRequest) (initializeResponse, error) {
	if w.initialized {
		return initializeResponse{}, fmt.Errorf("RQ2 WASM writer is already initialized")
	}
	if request.Backend != "kzg" && request.Backend != "ipa" || request.GatewayBaseURL == "" || request.FixtureURL == "" || !canonicalSHA256(request.GatewayInstanceToken) {
		return initializeResponse{}, fmt.Errorf("RQ2 WASM initialization is incomplete")
	}
	fixture, err := fetchFixture(request.FixtureURL)
	if err != nil {
		return initializeResponse{}, err
	}
	sourceFixture, err := rq2fixture.Decode(fixture)
	if err != nil {
		return initializeResponse{}, err
	}
	operations := make([]string, 0, len(browserOperations))
	for operation := range browserOperations {
		operations = append(operations, operation)
	}
	if _, err := sourceFixture.Root(request.Backend); err != nil {
		return initializeResponse{}, err
	}
	if err := sourceFixture.RequireOperations(operations); err != nil {
		return initializeResponse{}, err
	}
	started := time.Now()
	var scheme engine.Profile
	if request.Backend == "kzg" {
		scheme, err = kzg.NewScheme()
	} else {
		scheme, err = ipa.NewScheme()
	}
	parameterLoadNS := durationNS(time.Since(started))
	if err != nil {
		return initializeResponse{}, fmt.Errorf("initialize %s parameters: %w", request.Backend, err)
	}
	parameterProfile, parameterSHA256, parameterBytes, ok := rq2wire.ParameterEvidence(request.Backend)
	if !ok {
		return initializeResponse{}, fmt.Errorf("commitment parameter provenance is unavailable")
	}
	evaluation, err := gatewaytransport.New(gatewaytransport.Options{
		BaseURL: request.GatewayBaseURL, InstanceToken: request.GatewayInstanceToken,
		HTTPClient: &http.Client{Timeout: 24 * time.Hour},
	})
	if err != nil {
		return initializeResponse{}, err
	}
	remote, err := transport.New(transport.Options{
		BaseURL: request.GatewayBaseURL, HTTPClient: evaluation.InstanceHTTPClient(),
	})
	if err != nil {
		return initializeResponse{}, err
	}
	health, err := evaluation.Health(context.Background())
	if err != nil || health.Status != "ok" || health.EvaluationInstanceToken != request.GatewayInstanceToken {
		return initializeResponse{}, fmt.Errorf("browser Gateway health did not echo the exact disposable instance token")
	}
	registry := engine.NewRegistry()
	if err := registry.Register(scheme); err != nil {
		return initializeResponse{}, err
	}
	app, err := authenticationgraph.New(evaluation, engine.New(input.DefaultRegistry(), registry))
	if err != nil {
		return initializeResponse{}, err
	}
	digest := sha256.Sum256(fixture)
	*w = browserWriter{
		initialized: true, backend: request.Backend, remote: remote, app: app,
		fixture: sourceFixture,
	}
	return initializeResponse{
		ParameterLoadNS: parameterLoadNS, ParameterLoadBytes: parameterBytes,
		ParameterProfile: parameterProfile, ParameterSHA256: parameterSHA256,
		FixtureBytes: uint64(len(fixture)), FixtureSHA256: hex.EncodeToString(digest[:]),
	}, nil
}

func fetchFixture(url string) ([]byte, error) {
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Cache-Control", "no-store")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch browser fixture: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(response.Header.Get("Cache-Control"), "no-store") {
		return nil, fmt.Errorf("browser fixture response is not a non-cacheable success")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxBrowserFixtureBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > maxBrowserFixtureBytes {
		return nil, fmt.Errorf("browser fixture size is outside 1..%d", maxBrowserFixtureBytes)
	}
	return data, nil
}

func (w *browserWriter) exchange(request rq2wire.WorkerRequest) rq2wire.WorkerRecord {
	if !w.initialized || request.ClientKind != rq2wire.ClientBrowserWASM || request.Backend != w.backend {
		return rq2wire.FailedRecord(request, "capability_unavailable", fmt.Errorf("WASM writer coordinate was not initialized"))
	}
	switch request.RecordKind {
	case rq2wire.RecordSessionStart:
		return w.start(request)
	case rq2wire.RecordMutation:
		return w.mutate(request)
	case rq2wire.RecordSessionEnd:
		return w.end(request)
	default:
		return rq2wire.FailedRecord(request, "input_invalid", fmt.Errorf("preflight is owned by the browser host"))
	}
}

func (w *browserWriter) start(request rq2wire.WorkerRequest) rq2wire.WorkerRecord {
	if w.root.Defined() || w.sessionID != "" {
		return rq2wire.FailedRecord(request, "session_state", fmt.Errorf("browser session already started"))
	}
	root, err := cid.Parse(request.ExpectedAcceptedRoot)
	if err != nil || !rq2wire.ValidTypedRoot(root.String(), w.backend) {
		return rq2wire.FailedRecord(request, "input_invalid", fmt.Errorf("browser root is not a typed %s root", w.backend))
	}
	if w.fixture == nil || request.FixtureID != w.fixture.FixtureID {
		return rq2wire.FailedRecord(request, "fixture_incompatible", fmt.Errorf("browser request fixture identity does not match the pinned source fixture"))
	}
	load, err := w.app.Load(context.Background(), root)
	if err != nil {
		return rq2wire.FailedRecord(request, classifyFailure(err), err)
	}
	view, err := w.app.Snapshot()
	if err != nil || w.fixture.ValidateInitialGraph(view, w.backend) != nil {
		if err == nil {
			err = w.fixture.ValidateInitialGraph(view, w.backend)
		}
		return rq2wire.FailedRecord(request, "fixture_incompatible", err)
	}
	w.root, w.sessionID, w.source = root, request.SessionID, w.fixture.InitialSource()
	record := rq2wire.BaseRecord(request)
	record.Success = true
	record.Session = &rq2wire.SessionEvidence{AcceptedRoot: root.String(), GraphFetch: measured(load.FetchNS, load.WireBytes, load.Objects), GraphImport: measured(load.ImportNS, load.WireBytes, load.Objects)}
	return record
}

var browserOperations = map[string]struct{}{
	"document-edit-cid-binding-submit": {}, "list-append": {}, "list-replace": {}, "map-insert": {}, "map-replace": {},
}

func (w *browserWriter) mutate(request rq2wire.WorkerRequest) rq2wire.WorkerRecord {
	if _, ok := browserOperations[request.Operation]; !ok {
		return rq2wire.FailedRecord(request, "operation_unsupported", fmt.Errorf("unsupported browser operation %q", request.Operation))
	}
	if request.SessionID != w.sessionID || !w.root.Defined() || request.ExpectedAcceptedRoot != w.root.String() {
		return rq2wire.FailedRecord(request, "root_continuity", fmt.Errorf("browser request does not match retained root/session"))
	}
	mutationStarted := time.Now()
	viewSnapshotStarted := time.Now()
	view, err := w.app.Snapshot()
	viewSnapshotNS := durationNS(time.Since(viewSnapshotStarted))
	if err != nil {
		return rq2wire.FailedRecord(request, "verification_failed", err)
	}
	if err := w.fixture.ValidateGraphAgainstSource(view, w.backend, w.source); err != nil {
		return rq2wire.FailedRecord(request, "fixture_incompatible", fmt.Errorf("browser source pre-image binding: %w", err))
	}
	operation, err := w.fixture.Operation(request.Operation)
	if err != nil {
		return rq2wire.FailedRecord(request, "fixture_incompatible", err)
	}
	prepared, err := w.prepare(operation)
	if err != nil {
		return rq2wire.FailedRecord(request, "fixture_incompatible", err)
	}
	payloadUpload, err := w.remote.PutBatchMeasured(context.Background(), []transport.Block{{Codec: cid.Raw, Data: prepared.payload}})
	results := payloadUpload.Results
	if err != nil || len(results) != 1 || !results[0].CID.Equals(prepared.payloadCID) {
		if err == nil {
			err = fmt.Errorf("browser CAS upload did not return the exact local payload CID")
		}
		return rq2wire.FailedRecord(request, classifyFailure(err), err)
	}
	projectionStarted := time.Now()
	edit, err := w.app.Begin()
	if err != nil {
		return rq2wire.FailedRecord(request, "verification_failed", err)
	}
	candidate, err := rq2write.Apply(context.Background(), edit, w.root, operation, []cid.Cid{prepared.payloadCID})
	projectionNS := durationNS(time.Since(projectionStarted))
	if err != nil {
		return rq2wire.FailedRecord(request, "projection_invalid", err)
	}
	result, err := w.app.Submit(context.Background(), operationID(request), edit, candidate)
	if err != nil {
		return rq2wire.FailedRecord(request, classifyFailure(err), err)
	}
	if result.Idempotent {
		return rq2wire.FailedRecord(request, "gateway_instance_reused", fmt.Errorf("disposable Gateway returned an idempotent authentication batch replay"))
	}
	mutationTotalNS := durationNS(time.Since(mutationStarted))
	postView, err := w.app.Snapshot()
	if err != nil {
		return rq2wire.FailedRecord(request, "verification_failed", fmt.Errorf("snapshot browser post-image: %w", err))
	}
	if err := w.fixture.ValidateGraphAgainstSource(postView, w.backend, prepared.postSource); err != nil {
		return rq2wire.FailedRecord(request, "verification_failed", fmt.Errorf("browser full source post-image oracle: %w", err))
	}
	encodedCandidate := result.Root.String()
	parsedCandidate, err := cid.Parse(encodedCandidate)
	if err != nil || !parsedCandidate.Equals(result.Root) {
		return rq2wire.FailedRecord(request, "verification_failed", fmt.Errorf("expected-root CID encoding did not round-trip"))
	}
	record := rq2wire.BaseRecord(request)
	record.Success = true
	metrics, err := browserMetrics(prepared, result, viewSnapshotNS, projectionNS, payloadUpload, mutationTotalNS)
	if err != nil {
		return rq2wire.FailedRecord(request, "measurement_invalid", err)
	}
	record.Mutation = &rq2wire.MutationEvidence{
		Operation: request.Operation, PriorRoot: result.Base.String(), CandidateRoot: encodedCandidate,
		ReceiptRoot: result.Receipt.Root, ReceiptAccepted: result.Receipt.Root == result.Root.String(),
		BatchSHA256: result.Receipt.Digest,
		Metrics:     metrics,
	}
	w.root = result.Root
	w.source = prepared.postSource
	w.serial++
	w.receiptCount++
	return record
}

func (w *browserWriter) end(request rq2wire.WorkerRequest) rq2wire.WorkerRecord {
	if request.SessionID != w.sessionID || !w.root.Defined() || request.ExpectedAcceptedRoot != w.root.String() {
		return rq2wire.FailedRecord(request, "root_continuity", fmt.Errorf("browser session-end root does not match retained root"))
	}
	if err := w.app.Audit(context.Background()); err != nil {
		return rq2wire.FailedRecord(request, "audit_failed", err)
	}
	record := rq2wire.BaseRecord(request)
	record.Success = true
	record.Session = &rq2wire.SessionEvidence{AcceptedRoot: w.root.String(), ReceiptCount: w.receiptCount, AuditPassed: true, GraphFetch: rq2wire.NotApplicablePhase(), GraphImport: rq2wire.NotApplicablePhase()}
	return record
}

type browserPayload struct {
	payload    []byte
	payloadCID cid.Cid
	postSource map[string][]byte
	scan       rq2wire.PhaseMeasurement
	chunk      rq2wire.PhaseMeasurement
	hash       rq2wire.PhaseMeasurement
}

func (w *browserWriter) prepare(operation rq2fixture.Operation) (browserPayload, error) {
	prepared := browserPayload{
		scan: rq2wire.NotApplicablePhase(), chunk: rq2wire.NotApplicablePhase(), hash: rq2wire.NotApplicablePhase(),
	}
	postSource, payloads, err := w.fixture.ApplySourceOperation(w.source, operation, w.serial)
	if err != nil || len(payloads) != 1 {
		if err == nil {
			err = fmt.Errorf("browser operation must produce exactly one changed payload")
		}
		return browserPayload{}, err
	}
	prepared.postSource, prepared.payload = postSource, payloads[0]
	if operation.Kind == rq2fixture.KindDocumentEdit {
		scanStarted := time.Now()
		body := append([]byte(nil), prepared.payload...)
		prepared.scan = measured(durationNS(time.Since(scanStarted)), uint64(len(body)), 1)
		chunkStarted := time.Now()
		chunkSize := 256 << 10
		chunkCount := max(1, (len(body)+chunkSize-1)/chunkSize)
		for offset := 0; offset < len(body); offset += chunkSize {
			_ = append([]byte(nil), body[offset:min(len(body), offset+chunkSize)]...)
		}
		prepared.chunk = measured(durationNS(time.Since(chunkStarted)), uint64(len(body)), uint64(chunkCount))
	}
	hashStarted := time.Now()
	digest, err := mh.Sum(prepared.payload, mh.SHA2_256, -1)
	if err != nil {
		return browserPayload{}, err
	}
	prepared.payloadCID = cid.NewCidV1(cid.Raw, digest)
	prepared.hash = measured(durationNS(time.Since(hashStarted)), uint64(len(prepared.payload)), 1)
	return prepared, nil
}

func browserMetrics(payload browserPayload, result authenticationgraph.Result, snapshotNS, projectionNS uint64, payloadUpload transport.PutBatchMeasurement, mutationTotalNS uint64) (rq2wire.MutationMetrics, error) {
	phase := func(duration, bytes, count uint64) rq2wire.PhaseMeasurement {
		return measured(duration, bytes, max(uint64(1), count))
	}
	generation, err := rq2metrics.AddDurations(snapshotNS, projectionNS)
	if err != nil {
		return rq2wire.MutationMetrics{}, err
	}
	computation, submission := result.Computation, result.Submission
	return rq2wire.MutationMetrics{
		TaxonomyProfile: rq2metrics.TaxonomyProfile, MutationTotal: phase(mutationTotalNS, 0, 1), Scan: payload.scan, Chunk: payload.chunk, Hash: payload.hash,
		GraphSnapshot: phase(snapshotNS, 0, 1), CandidateApply: phase(computation.ApplyNS, 0, computation.Candidates), CandidateExport: phase(computation.ExportNS, computation.CandidateBytes, computation.Candidates), CandidateGeneration: phase(generation, 0, 1),
		BatchEncoding: phase(submission.RequestEncodingNS, submission.RequestWireBytes, 1), Upload: phase(payloadUpload.RoundTripNS, payloadUpload.RequestWireBytes, 1),
		GatewayValidateStage: phase(submission.Gateway.ValidationAndStageNS, 0, 1), GatewayPersist: phase(submission.Gateway.PersistNS, 0, 1), ReceiptCheck: phase(submission.ResponseVerifyNS, submission.ResponseWireBytes, 1),
		CPUTotal: rq2wire.NotApplicablePhase(), PeakMemory: rq2wire.NotApplicablePhase(), WASMDownload: rq2wire.NotApplicablePhase(), WASMInstantiate: rq2wire.NotApplicablePhase(), ParameterLoad: rq2wire.NotApplicablePhase(), FirstMutation: rq2wire.NotApplicablePhase(), JSWASMBoundary: rq2wire.NotApplicablePhase(),
	}, nil
}

func measured(duration, bytes, count uint64) rq2wire.PhaseMeasurement {
	return rq2wire.ObservedPhase(duration, bytes, count)
}

func canonicalSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			if char < 'a' || char > 'f' {
				return false
			}
		}
	}
	return true
}

func operationID(request rq2wire.WorkerRequest) string {
	digest := sha256.Sum256([]byte(request.WorkerID + "\x00" + request.SessionID + "\x00" + request.RequestID + "\x00" + request.Operation))
	return "rq2-" + hex.EncodeToString(digest[:16])
}

func classifyFailure(err error) string {
	var transportError *transport.Error
	if errors.As(err, &transportError) {
		return "gateway_rejected"
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	return "authentication_failed"
}

func durationNS(value time.Duration) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}

func strictJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("JSON contains trailing data")
	}
	return nil
}

func marshalJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	return string(data), err
}

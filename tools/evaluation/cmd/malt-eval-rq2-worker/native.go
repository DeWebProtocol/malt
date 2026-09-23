package main

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
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
	"github.com/dewebprotocol/malt-core/engine"
	cid "github.com/ipfs/go-cid"
)

var nativeOperations = map[string]struct{}{
	"append": {}, "batch-sync": {}, "create-small-file": {}, "delete-directory-entry": {},
	"insert-directory-entry": {}, "modify-small-file": {}, "move": {}, "rename": {},
	"replace-large-file-chunk": {},
}

type nativeSession struct {
	config    workerConfig
	remote    *transport.Client
	app       *authenticationgraph.Session
	fixture   *rq2fixture.Fixture
	workspace *nativeWorkspace
	root      cid.Cid
	serial    uint64
}

func newNativeSession(config workerConfig, remote *transport.Client, evaluation *gatewaytransport.Client, fixture *rq2fixture.Fixture) (*nativeSession, error) {
	if config.clientKind != rq2wire.ClientNative || config.lifecycle != rq2wire.LifecycleNativeLong {
		return nil, fmt.Errorf("native worker requires native-long-lived coordinate")
	}
	var scheme engine.Profile
	var err error
	switch config.backend {
	case "kzg":
		scheme, err = kzg.NewScheme()
	case "ipa":
		scheme, err = ipa.NewScheme()
	default:
		return nil, fmt.Errorf("unsupported backend %q", config.backend)
	}
	if err != nil {
		return nil, err
	}
	registry := engine.NewRegistry()
	if err := registry.Register(scheme); err != nil {
		return nil, err
	}
	app, err := authenticationgraph.New(evaluation, engine.New(registry))
	if err != nil {
		return nil, err
	}
	if fixture == nil {
		return nil, fmt.Errorf("native RQ2 source fixture is nil")
	}
	if _, err := fixture.Root(config.backend); err != nil {
		return nil, err
	}
	operations := make([]string, 0, len(nativeOperations))
	for operation := range nativeOperations {
		operations = append(operations, operation)
	}
	if err := fixture.RequireOperations(operations); err != nil {
		return nil, err
	}
	return &nativeSession{config: config, remote: remote, app: app, fixture: fixture}, nil
}

func (n *nativeSession) close() error {
	if n == nil {
		return nil
	}
	return n.workspace.close()
}

func (w *worker) startSession(request rq2wire.WorkerRequest) rq2wire.WorkerRecord {
	w.state = rq2wire.RecordMutation
	if w.native == nil {
		return rq2wire.FailedRecord(request, "capability_unavailable", fmt.Errorf("native capability did not pass preflight"))
	}
	root, err := cid.Parse(request.ExpectedAcceptedRoot)
	if err != nil || !rq2wire.ValidTypedRoot(root.String(), w.config.backend) {
		return rq2wire.FailedRecord(request, "input_invalid", fmt.Errorf("session root is not a typed %s MALT root", w.config.backend))
	}
	ctx, cancel := context.WithTimeout(context.Background(), w.config.requestTimeout)
	defer cancel()
	load, err := w.native.app.Load(ctx, root)
	if err != nil {
		return rq2wire.FailedRecord(request, classifyNativeFailure(err), fmt.Errorf("load initial authentication graph: %w", err))
	}
	view, err := w.native.app.Snapshot()
	if err != nil {
		return rq2wire.FailedRecord(request, "verification_failed", err)
	}
	if err := w.native.fixture.ValidateInitialGraph(view, w.config.backend); err != nil {
		return rq2wire.FailedRecord(request, "fixture_incompatible", err)
	}
	workspace, err := newNativeWorkspace(w.native.fixture)
	if err != nil {
		return rq2wire.FailedRecord(request, "fixture_incompatible", fmt.Errorf("create native UnixFS workspace: %w", err))
	}
	if w.native.workspace != nil {
		_ = w.native.workspace.close()
	}
	w.native.workspace = workspace
	w.native.root = root
	record := rq2wire.BaseRecord(request)
	record.Success = true
	record.Session = &rq2wire.SessionEvidence{AcceptedRoot: root.String(), GraphFetch: rq2wire.ObservedPhase(load.FetchNS, load.WireBytes, load.Objects), GraphImport: rq2wire.ObservedPhase(load.ImportNS, load.WireBytes, load.Objects)}
	return record
}

func (w *worker) mutate(request rq2wire.WorkerRequest) rq2wire.WorkerRecord {
	if _, ok := nativeOperations[request.Operation]; !ok {
		return rq2wire.FailedRecord(request, "operation_unsupported", fmt.Errorf("unsupported native operation %q", request.Operation))
	}
	if w.native == nil || !w.native.root.Defined() || request.ExpectedAcceptedRoot != w.native.root.String() {
		return rq2wire.FailedRecord(request, "root_continuity", fmt.Errorf("mutation expected root does not match retained session root"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), w.config.requestTimeout)
	defer cancel()
	usageBefore, err := readProcessUsage()
	if err != nil {
		return rq2wire.FailedRecord(request, "measurement_unavailable", err)
	}
	mutationStarted := time.Now()
	viewSnapshotStarted := time.Now()
	view, err := w.native.app.Snapshot()
	viewSnapshotNS := durationNS(time.Since(viewSnapshotStarted))
	if err != nil {
		return rq2wire.FailedRecord(request, "verification_failed", err)
	}
	currentSource, err := w.native.workspace.snapshot()
	if err != nil {
		return rq2wire.FailedRecord(request, "fixture_incompatible", fmt.Errorf("scan native source pre-image: %w", err))
	}
	if err := w.native.fixture.ValidateGraphAgainstSource(view, w.config.backend, currentSource); err != nil {
		return rq2wire.FailedRecord(request, "fixture_incompatible", fmt.Errorf("native source pre-image binding: %w", err))
	}
	operation, err := w.native.fixture.Operation(request.Operation)
	if err != nil {
		return rq2wire.FailedRecord(request, "fixture_incompatible", err)
	}
	prepared, err := prepareNativeOperation(operation, w.native.fixture, w.native.workspace, w.native.serial)
	if err != nil {
		return rq2wire.FailedRecord(request, "fixture_incompatible", err)
	}
	var payloadUpload transport.PutBatchMeasurement
	if len(prepared.blocks) > 0 {
		payloadUpload, err = w.native.remote.PutBatchMeasured(ctx, prepared.blocks)
		if err != nil {
			return rq2wire.FailedRecord(request, classifyNativeFailure(err), fmt.Errorf("upload operation payloads: %w", err))
		}
		results := payloadUpload.Results
		if len(results) != len(prepared.cids) {
			return rq2wire.FailedRecord(request, "transport_invalid", fmt.Errorf("CAS batch returned a mismatched result count"))
		}
		for index := range results {
			if !results[index].CID.Equals(prepared.cids[index]) {
				return rq2wire.FailedRecord(request, "transport_invalid", fmt.Errorf("CAS result %d does not match locally hashed payload", index))
			}
		}
	}
	projectionStarted := time.Now()
	edit, err := w.native.app.Begin()
	if err != nil {
		return rq2wire.FailedRecord(request, "verification_failed", err)
	}
	candidate, err := rq2write.Apply(ctx, edit, w.native.root, operation, prepared.cids)
	projectionNS := durationNS(time.Since(projectionStarted))
	if err != nil {
		return rq2wire.FailedRecord(request, "fixture_incompatible", err)
	}
	operationID := exactOperationID(request)
	result, err := w.native.app.Submit(ctx, operationID, edit, candidate)
	if err != nil {
		return rq2wire.FailedRecord(request, classifyNativeFailure(err), err)
	}
	if result.Idempotent {
		return rq2wire.FailedRecord(request, "gateway_instance_reused", fmt.Errorf("disposable Gateway returned an idempotent authentication batch replay"))
	}
	mutationTotalNS := durationNS(time.Since(mutationStarted))
	usageAfter, err := readProcessUsage()
	if err != nil || usageAfter.peakRSSBytes == 0 {
		return rq2wire.FailedRecord(request, "measurement_unavailable", fmt.Errorf("read process usage after mutation: %w", err))
	}
	cpuNS := usageAfter.cpuNS - min(usageAfter.cpuNS, usageBefore.cpuNS)
	postView, err := w.native.app.Snapshot()
	if err != nil {
		return rq2wire.FailedRecord(request, "verification_failed", fmt.Errorf("snapshot native post-image: %w", err))
	}
	postSource, err := w.native.workspace.snapshot()
	if err != nil {
		return rq2wire.FailedRecord(request, "fixture_incompatible", fmt.Errorf("scan native source post-image: %w", err))
	}
	if err := w.native.fixture.ValidateGraphAgainstSource(postView, w.config.backend, postSource); err != nil {
		return rq2wire.FailedRecord(request, "verification_failed", fmt.Errorf("native full source post-image oracle: %w", err))
	}
	encodedCandidate, err := encodeExpectedRoot(result.Root)
	if err != nil {
		return rq2wire.FailedRecord(request, "verification_failed", err)
	}
	record := rq2wire.BaseRecord(request)
	record.Success = true
	metrics, err := nativeMetrics(prepared, result, viewSnapshotNS, projectionNS, payloadUpload, mutationTotalNS, cpuNS, usageAfter.peakRSSBytes)
	if err != nil {
		return rq2wire.FailedRecord(request, "measurement_invalid", err)
	}
	if err := metrics.Validate(w.config.clientKind, w.config.backend, w.config.lifecycle, request.Operation); err != nil {
		return rq2wire.FailedRecord(request, "measurement_invalid", err)
	}
	record.Mutation = &rq2wire.MutationEvidence{
		Operation: request.Operation, PriorRoot: result.Base.String(), CandidateRoot: encodedCandidate,
		ReceiptRoot: result.Receipt.Root, ReceiptAccepted: result.Receipt.Root == result.Root.String(),
		BatchSHA256: result.Receipt.Digest,
		Metrics:     metrics,
	}
	w.native.root = result.Root
	w.native.serial++
	w.receiptCount++
	return record
}

func (w *worker) endSession(request rq2wire.WorkerRequest) rq2wire.WorkerRecord {
	w.state = "finished"
	if w.native == nil || !w.native.root.Defined() || request.ExpectedAcceptedRoot != w.native.root.String() {
		return rq2wire.FailedRecord(request, "root_continuity", fmt.Errorf("session-end root does not match retained exact receipt root"))
	}
	ctx, cancel := context.WithTimeout(context.Background(), w.config.requestTimeout)
	defer cancel()
	if err := w.native.app.Audit(ctx); err != nil {
		return rq2wire.FailedRecord(request, "audit_failed", err)
	}
	record := rq2wire.BaseRecord(request)
	record.Success = true
	record.Session = &rq2wire.SessionEvidence{AcceptedRoot: w.native.root.String(), ReceiptCount: w.receiptCount, AuditPassed: true, GraphFetch: rq2wire.NotApplicablePhase(), GraphImport: rq2wire.NotApplicablePhase()}
	return record
}

func nativeMetrics(prepared preparedPayloads, operation authenticationgraph.Result, snapshotNS, projectionNS uint64, payloadUpload transport.PutBatchMeasurement, mutationTotalNS, cpuNS, peakRSS uint64) (rq2wire.MutationMetrics, error) {
	phase := func(duration, bytes, count uint64) rq2wire.PhaseMeasurement {
		return rq2wire.ObservedPhase(duration, bytes, max(uint64(1), count))
	}
	upload := rq2wire.NotApplicablePhase()
	if len(prepared.blocks) > 0 {
		upload = phase(payloadUpload.RoundTripNS, payloadUpload.RequestWireBytes, uint64(len(prepared.blocks)))
	}
	generation, err := rq2metrics.AddDurations(snapshotNS, projectionNS)
	if err != nil {
		return rq2wire.MutationMetrics{}, err
	}
	computation, submission := operation.Computation, operation.Submission
	return rq2wire.MutationMetrics{
		TaxonomyProfile: rq2metrics.TaxonomyProfile, MutationTotal: phase(mutationTotalNS, 0, 1), Scan: prepared.scan, Chunk: prepared.chunk, Hash: prepared.hash,
		GraphSnapshot: phase(snapshotNS, 0, 1), CandidateApply: phase(computation.ApplyNS, 0, computation.Candidates), CandidateExport: phase(computation.ExportNS, computation.CandidateBytes, computation.Candidates), CandidateGeneration: phase(generation, 0, 1),
		BatchEncoding: phase(submission.RequestEncodingNS, submission.RequestWireBytes, 1), Upload: upload,
		GatewayValidateStage: phase(submission.Gateway.ValidationAndStageNS, 0, 1), GatewayPersist: phase(submission.Gateway.PersistNS, 0, 1), ReceiptCheck: phase(submission.ResponseVerifyNS, submission.ResponseWireBytes, 1),
		CPUTotal: phase(cpuNS, 0, 1), PeakMemory: phase(0, peakRSS, 1), WASMDownload: rq2wire.NotApplicablePhase(), WASMInstantiate: rq2wire.NotApplicablePhase(), ParameterLoad: rq2wire.NotApplicablePhase(), FirstMutation: rq2wire.NotApplicablePhase(), JSWASMBoundary: rq2wire.NotApplicablePhase(),
	}, nil
}

func encodeExpectedRoot(candidate cid.Cid) (string, error) {
	encoded := candidate.String()
	parsed, err := cid.Parse(encoded)
	if err != nil || !parsed.Equals(candidate) {
		return "", fmt.Errorf("expected-root CID encoding did not round-trip")
	}
	return encoded, nil
}

func exactOperationID(request rq2wire.WorkerRequest) string {
	value := request.WorkerID + "\x00" + request.SessionID + "\x00" + request.RequestID + "\x00" + request.Operation
	digest := mutationDigest([]byte(value))
	return "rq2-" + hex.EncodeToString(digest[:16])
}

func classifyNativeFailure(err error) string {
	if err == nil {
		return "unknown"
	}
	if _, ok := err.(*transport.Error); ok {
		return "gateway_rejected"
	}
	if contextCanceled(err) {
		return "timeout"
	}
	return "authentication_failed"
}

func contextCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func durationNS(value time.Duration) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}

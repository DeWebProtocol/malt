package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"slices"
	"strconv"
	"time"

	clientrootapp "github.com/dewebprotocol/malt-client/application/clientroot"
	"github.com/dewebprotocol/malt-client/internal/evaluation/gatewaytransport"
	"github.com/dewebprotocol/malt-client/internal/evaluation/rq3baseline"
	"github.com/dewebprotocol/malt-client/transport"
	"github.com/dewebprotocol/malt-core/auth/arcset"
	"github.com/dewebprotocol/malt-core/protocol"
	clientwriter "github.com/dewebprotocol/malt-core/sdk/writer"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

const (
	healthCASAccounting = "cas.put-batch-disposition/v1"
	healthCASIsolation  = "gateway-process-serialized"
)

type workerConfig struct {
	gatewayBaseURL              string
	instanceToken               string
	bootstrapAuthorizationToken string
	requestTimeout              time.Duration
	initialRoot                 string
}

type campaignWorker struct {
	config     workerConfig
	remote     *transport.Client
	evaluation evaluationGateway
}

type evaluationGateway interface {
	Health(context.Context) (*gatewaytransport.Health, error)
	BootstrapEvaluationObject(context.Context, string, gatewaytransport.BootstrapObject) (gatewaytransport.BootstrapResult, error)
	ApplyEvaluationFlatMap(context.Context, string, gatewaytransport.FlatMapMutation) (gatewaytransport.FlatMapResult, error)
}

type clientRootRemote struct{ client *transport.Client }

func (r clientRootRemote) FetchUpdateView(ctx context.Context, root cid.Cid, bounds *protocol.UpdateViewBounds) (clientrootapp.ViewEnvelope, error) {
	response, err := r.client.FetchUpdateView(ctx, root, bounds)
	if err != nil {
		return clientrootapp.ViewEnvelope{}, err
	}
	return clientrootapp.ViewEnvelope{View: response.View, WireBytes: response.WireBytes}, nil
}

func (r clientRootRemote) SubmitClientRoot(ctx context.Context, prepared clientwriter.ComputeResult) (clientrootapp.ReceiptEnvelope, error) {
	response, err := r.client.SubmitClientRoot(ctx, prepared.Bundle)
	if err != nil {
		return clientrootapp.ReceiptEnvelope{}, err
	}
	accounting := clientrootapp.GatewayWriteAccounting{
		Profile: response.WriteAccounting.Profile, Available: response.WriteAccounting.Available,
		UnavailableReason: response.WriteAccounting.UnavailableReason, ByteMethod: response.WriteAccounting.ByteMethod,
		ObjectLedgerSHA256: response.WriteAccounting.ObjectLedgerSHA256, WireBytes: response.WriteAccountingWireBytes,
		Categories: make([]clientrootapp.GatewayWriteCategoryAccounting, len(response.WriteAccounting.Categories)),
	}
	for index, category := range response.WriteAccounting.Categories {
		accounting.Categories[index] = clientrootapp.GatewayWriteCategoryAccounting{
			Category: category.Category, AttemptedWrites: category.AttemptedWrites, AttemptedBytes: category.AttemptedBytes,
			AttemptedNewWrites: category.AttemptedNewWrites, AttemptedNewBytes: category.AttemptedNewBytes,
			AttemptedReplacementWrites: category.AttemptedReplacementWrites, AttemptedReplacementBytes: category.AttemptedReplacementBytes,
			AttemptedSameValueWrites: category.AttemptedSameValueWrites, AttemptedSameValueBytes: category.AttemptedSameValueBytes,
			AttemptedDeleteWrites: category.AttemptedDeleteWrites, AttemptedDeleteBytes: category.AttemptedDeleteBytes,
			NewlyPersistedWrites: category.NewlyPersistedWrites, GrossNewBytes: category.GrossNewBytes,
			NewWrites: category.NewWrites, NewBytes: category.NewBytes, ReplacedWrites: category.ReplacedWrites,
			ReplacementNewBytes: category.ReplacementNewBytes, ReplacementReclaimedBytes: category.ReplacementReclaimedBytes,
			SameValueWrites: category.SameValueWrites, DeletedWrites: category.DeletedWrites,
			DeletedReclaimedBytes: category.DeletedReclaimedBytes, ReclaimedBytes: category.ReclaimedBytes, NetBytes: category.NetBytes,
		}
	}
	return clientrootapp.ReceiptEnvelope{
		Receipt: response.Receipt, RequestWireBytes: response.RequestWireBytes, ResponseWireBytes: response.ResponseWireBytes,
		RequestEncodingNS: response.RequestEncodingNS, ResponseVerifyNS: response.ResponseVerifyNS,
		Idempotent: response.Idempotent,
		Gateway: clientrootapp.GatewayPhaseMetrics{
			OldStateValidationNS: response.Gateway.OldStateValidationNS, GatewayReplayNS: response.Gateway.GatewayReplayNS,
			PersistNS: response.Gateway.PersistNS, ReceiptNS: response.Gateway.ReceiptNS,
		},
		WriteAccounting: accounting,
	}, nil
}

func newCampaignWorker(config workerConfig) (*campaignWorker, error) {
	evaluation, err := gatewaytransport.New(gatewaytransport.Options{
		BaseURL: config.gatewayBaseURL, InstanceToken: config.instanceToken,
		HTTPClient: &http.Client{Timeout: config.requestTimeout},
	})
	if err != nil {
		return nil, err
	}
	client, err := transport.New(transport.Options{
		BaseURL:    config.gatewayBaseURL,
		HTTPClient: evaluation.InstanceHTTPClient(),
	})
	if err != nil {
		return nil, err
	}
	return &campaignWorker{config: config, remote: client, evaluation: evaluation}, nil
}

func (w *campaignWorker) validateHealth(ctx context.Context) error {
	checkCtx, cancel := context.WithTimeout(ctx, w.config.requestTimeout)
	defer cancel()
	health, err := w.evaluation.Health(checkCtx)
	if err != nil {
		return fmt.Errorf("Gateway health: %w", err)
	}
	if health.Status != "ok" || health.EvaluationInstanceToken != w.config.instanceToken ||
		health.BlobBackend != "filesystem" || health.KVBackend != "fs" || health.ArcTableMode != "versioned" ||
		health.CommitmentProfile != "kzg" || health.CommitmentBackends != "ipa,kzg" ||
		health.EvaluationCASWriteAccounting != healthCASAccounting || health.EvaluationCASWriteIsolation != healthCASIsolation ||
		health.ClientRootExactAcceptance != "false" || health.ClientRootWriteAccounting != "" ||
		health.EvaluationClientRootBootstrap != "" ||
		health.EvaluationRQ3FlatMap != gatewaytransport.FlatMapProfile ||
		health.EvaluationRQ3FlatMapStorageScope != "arctable-arcset-key-plus-value-only/v1" ||
		health.EvaluationRQ3FlatMapCheckpoint != "false" ||
		health.EvaluationRQ3FlatMapMaterializationCache != "none" {
		return fmt.Errorf("Gateway health does not expose the exact disposable FSKV-ArcTable/filesystem-CAS/KZG evaluation boundary")
	}
	return nil
}

func (w *campaignWorker) run(ctx context.Context, spec runSpec) (*runResult, error) {
	if !validRunCoordinate(spec.PassMode, spec.RunPhase, spec.ClusterID, spec.RunIndex) {
		return nil, fmt.Errorf("invalid raw RQ3 pass/run/cluster coordinate")
	}
	if err := validateWorkloadIdentity(spec.Workload); err != nil {
		return nil, err
	}
	if spec.CommitManifest != nil {
		return nil, fmt.Errorf("one-shot run must not carry a stream commit manifest")
	}
	if err := validateFrozenCommitListBinding(spec); err != nil {
		return nil, err
	}
	if w.config.initialRoot != spec.InitialRoot {
		return nil, fmt.Errorf("-initial-root and run.initial_root do not match")
	}
	if spec.InitialRoot != "" {
		return nil, fmt.Errorf("nonempty initial_root cannot fairly account the frozen snapshot; a clean disposable Gateway is required")
	}
	if err := prevalidateMALTFlatRun(spec); err != nil {
		return nil, err
	}
	if err := w.validateHealth(ctx); err != nil {
		return nil, err
	}
	return w.runFlatDirect(ctx, spec)
}

// validateFrozenCommitListBinding closes the last source-to-worker identity
// hop. The evaluator derives this digest from the controlled workload or the
// exact Git first-parent trace; the worker independently recomputes it from
// the snapshot and commit sequence it is about to execute.
func validateFrozenCommitListBinding(spec runSpec) error {
	commitIDs := make([]string, 0, len(spec.Commits)+1)
	commitIDs = append(commitIDs, spec.Snapshot.CommitID)
	for _, commit := range spec.Commits {
		commitIDs = append(commitIDs, commit.CommitID)
	}
	return validateCommitManifest(spec.Workload, commitIDs)
}

func validateCommitManifest(workload workloadIdentity, commitIDs []string) error {
	if len(commitIDs) == 0 || len(commitIDs) > maximumMALTCommitManifest {
		return fmt.Errorf("frozen commit manifest requires 1..%d entries", maximumMALTCommitManifest)
	}
	seen := make(map[string]struct{}, len(commitIDs))
	for index, commitID := range commitIDs {
		if commitID == "" || len(commitID) > 256 {
			return fmt.Errorf("frozen commit manifest entry %d is invalid", index)
		}
		if _, duplicate := seen[commitID]; duplicate {
			return fmt.Errorf("frozen commit manifest repeats commit_id %q", commitID)
		}
		seen[commitID] = struct{}{}
	}
	encoded, err := json.Marshal(struct {
		Commits []string `json:"commits"`
	}{Commits: commitIDs})
	if err != nil {
		return fmt.Errorf("encode frozen commit-list binding: %w", err)
	}
	digest := sha256.Sum256(encoded)
	if hex.EncodeToString(digest[:]) != workload.CommitListSHA256 {
		return fmt.Errorf("frozen snapshot/commit sequence does not match workload commit_list_sha256")
	}
	return nil
}

type baselineRunner func(context.Context, rq3baseline.RunSpec) (*rq3baseline.RunResult, error)

// postMeasurementBaselineConformance deliberately runs only after the MALT
// session has produced every commit and passed its retained-state audit. The
// UnixFS adapter is therefore never a one-sided prewarm for measured MALT.
func postMeasurementBaselineConformance(ctx context.Context, spec rq3baseline.RunSpec, result *runResult, runBaseline baselineRunner) error {
	if result == nil || len(result.Commits) != len(spec.Commits)+1 {
		return fmt.Errorf("MALT measurements are incomplete before baseline conformance")
	}
	baseline, err := runBaseline(ctx, spec)
	if err != nil {
		return fmt.Errorf("post-measurement frozen workload baseline conformance: %w", err)
	}
	if len(baseline.Records) != len(result.Commits) {
		return fmt.Errorf("post-measurement baseline conformance returned a mismatched commit count")
	}
	for index, record := range baseline.Records {
		commit := result.Commits[index]
		if record.CommitID != commit.CommitID || record.LogicalObjectsChanged != commit.LogicalObjectsChanged ||
			record.LogicalBindingsChanged != commit.LogicalBindingsChanged || record.AdapterPayloadInputBytes != commit.AdapterPayloadInputBytes ||
			commit.HistoryRootsRetained != uint32(index+1) || commit.NonWorkloadSetupRootsRetained != 1 {
			return fmt.Errorf("post-measurement baseline conformance mismatch at commit %q", commit.CommitID)
		}
	}
	return nil
}

func boolPointer(value bool) *bool { return &value }

func validRunCoordinate(passMode, runPhase, clusterID string, runIndex int) bool {
	if !canonicalEvaluatorID(clusterID) || runIndex < 0 {
		return false
	}
	if passMode == "accounting" {
		return runPhase == "accounting" && runIndex == 0
	}
	return passMode == "timing" && (runPhase == "feasibility" || runPhase == "measured")
}

func isControlledListHistory(spec runSpec) bool {
	return spec.Workload.Kind == "controlled" && spec.Workload.ControlledStructure == "list"
}

func (w *campaignWorker) bootstrapGraph(ctx context.Context, commitID string, graph *hybridGraph, result *runResult) (int64, int64, error) {
	seenRoots := make(map[string]struct{}, len(graph.objects))
	var replayTotal, persistTotal uint64
	for order, logicalID := range graph.order {
		object := graph.objects[logicalID]
		if _, duplicate := seenRoots[object.root.KeyString()]; duplicate {
			continue
		}
		seenRoots[object.root.KeyString()] = struct{}{}
		entries := make([]gatewaytransport.BootstrapEntry, object.entries.Len())
		for index, entry := range object.entries.Entries() {
			entries[index].Target = entry.Target.CID()
			if object.kind == arcset.KindMap {
				coordinate := entry.Coordinate.String()
				entries[index].Path = &coordinate
			} else {
				coordinate, err := listIndex(entry)
				if err != nil {
					return 0, 0, err
				}
				entries[index].Index = &coordinate
			}
		}
		bootstrap, err := w.evaluation.BootstrapEvaluationObject(ctx, w.config.bootstrapAuthorizationToken, gatewaytransport.BootstrapObject{
			OperationID: operationID(commitID+"-bootstrap-"+strconv.Itoa(order), uint32(order)),
			Kind:        object.kind, Backend: maltcid.BackendKindKZG, ExpectedRoot: object.root,
			Entries: entries, Commit: object.commit,
		})
		if err != nil {
			return 0, 0, fmt.Errorf("bootstrap flat object %q: %w", logicalID, err)
		}
		if result.PassMode == "accounting" {
			if err := result.appendGatewayAccounting(commitID, accountingFromTransport(bootstrap.WriteAccounting)); err != nil {
				return 0, 0, err
			}
		}
		if replayTotal > math.MaxUint64-bootstrap.ReplayNanos || persistTotal > math.MaxUint64-bootstrap.PersistNanos {
			return 0, 0, fmt.Errorf("bootstrap phase duration overflow")
		}
		replayTotal += bootstrap.ReplayNanos
		persistTotal += bootstrap.PersistNanos
	}
	if replayTotal > math.MaxInt64 || persistTotal > math.MaxInt64 {
		return 0, 0, fmt.Errorf("bootstrap phase duration exceeds evaluator range")
	}
	return int64(replayTotal), int64(persistTotal), nil
}

func sortedManifestBlocks(graph *hybridGraph) []classifiedBlock {
	paths := make([]string, 0, len(graph.manifests))
	for value := range graph.manifests {
		paths = append(paths, value)
	}
	slices.Sort(paths)
	result := make([]classifiedBlock, len(paths))
	for index, value := range paths {
		result[index] = graph.manifests[value]
	}
	return result
}

func validateWorkloadIdentity(value workloadIdentity) error {
	if !canonicalEvaluatorID(value.ID) || (value.Kind != "controlled" && value.Kind != "git-first-parent") ||
		!canonicalSHA256(value.ArtifactSHA256) || !canonicalSHA256(value.SemanticSHA256) ||
		!canonicalSHA256(value.CommitListSHA256) || value.ChunkBytes == 0 || value.ChunkBytes > 16<<20 ||
		value.HistoryRetention != "all-roots" || (value.Kind == "controlled") != (value.ControlledCoordinate != nil) ||
		(value.Kind == "controlled" && value.ControlledStructure != "map" && value.ControlledStructure != "list") ||
		(value.Kind != "controlled" && value.ControlledStructure != "") || !validControlledCoordinate(value) {
		return fmt.Errorf("invalid RQ3 workload identity")
	}
	return nil
}

func validControlledCoordinate(value workloadIdentity) bool {
	if value.ControlledCoordinate == nil {
		return value.Kind != "controlled"
	}
	coordinate := value.ControlledCoordinate
	isRelocation := coordinate.Operation == "rename" || coordinate.Operation == "move"
	return value.Kind == "controlled" && canonicalEvaluatorID(coordinate.Operation) && coordinate.PathDepth > 0 &&
		coordinate.DirectoryWidth > 0 && coordinate.FileChunks > 0 && coordinate.BatchSize > 0 && coordinate.RenamedBindings >= 0 &&
		(coordinate.Operation == "subtree-rename") == (coordinate.RenamedBindings > 1) &&
		(!isRelocation || coordinate.RenamedBindings == 1) &&
		(isRelocation || coordinate.Operation == "subtree-rename" || coordinate.RenamedBindings == 0)
}

func canonicalEvaluatorID(value string) bool {
	if len(value) == 0 || len(value) > 128 || !lowerAlphaNumeric(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if !lowerAlphaNumeric(character) && character != '.' && character != '_' && character != '-' {
			return false
		}
	}
	return true
}

func lowerAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func evaluationBounds() *protocol.UpdateViewBounds {
	return &protocol.UpdateViewBounds{
		MaxObjects: protocol.MaxClientRootObjects, MaxTotalEntries: protocol.MaxClientRootEntries,
		MaxDepth: protocol.MaxClientRootDepth,
	}
}

func operationID(commitID string, order uint32) string {
	return stableIntentID("rq3", fmt.Sprintf("%d\x00%s", order, commitID))
}

func durationNanos(value time.Duration) int64 {
	if value <= 0 {
		return 1
	}
	return value.Nanoseconds()
}

func addDuration(a int64, b uint64) (int64, error) {
	if b > math.MaxInt64 || a > math.MaxInt64-int64(b) {
		return 0, fmt.Errorf("client duration overflow")
	}
	return a + int64(b), nil
}

func evaluatorNanos(value uint64) (int64, error) {
	if value > math.MaxInt64 {
		return 0, fmt.Errorf("duration exceeds evaluator range")
	}
	return int64(value), nil
}

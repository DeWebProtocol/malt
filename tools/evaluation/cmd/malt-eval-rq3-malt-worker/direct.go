package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	"github.com/dewebprotocol/malt-client/internal/evaluation/gatewaytransport"
	"github.com/dewebprotocol/malt-client/internal/evaluation/rq3baseline"
	"github.com/dewebprotocol/malt-client/transport"
	"github.com/dewebprotocol/malt-core/auth/arcset"
	materializermemory "github.com/dewebprotocol/malt-core/auth/arcset/materializer/memory"
	"github.com/dewebprotocol/malt-core/auth/commitment/kzg"
	cid "github.com/ipfs/go-cid"
)

type flatDirectStream struct {
	worker         *campaignWorker
	base           runSpec
	source         *rq3baseline.ValidationStreamSession
	state          map[string]logicalFile
	roles          *blockRoleIndex
	root           cid.Cid
	oracle         *flatRootOracle
	nextOrder      uint32
	commitIDs      []string
	commitManifest []string
	payloads       *logicalPayloadStore
	failed         bool
}

func (w *campaignWorker) startFlatDirectStream(ctx context.Context, spec runSpec) (_ *flatDirectStream, _ *runResult, returnErr error) {
	if !validRunCoordinate(spec.PassMode, spec.RunPhase, spec.ClusterID, spec.RunIndex) {
		return nil, nil, fmt.Errorf("invalid raw RQ3 pass/run/cluster coordinate")
	}
	if err := validateWorkloadIdentity(spec.Workload); err != nil {
		return nil, nil, err
	}
	if w.config.initialRoot != spec.InitialRoot || spec.InitialRoot != "" {
		return nil, nil, fmt.Errorf("stream start requires a clean disposable Gateway and empty initial_root")
	}
	if spec.Commits == nil || len(spec.Commits) != 0 {
		return nil, nil, fmt.Errorf("stream start requires an explicit empty commit chunk")
	}
	if spec.CommitManifest == nil || len(spec.CommitManifest) == 0 || spec.CommitManifest[0] != spec.Snapshot.CommitID {
		return nil, nil, fmt.Errorf("stream start requires a complete commit manifest beginning at the snapshot")
	}
	if err := validateCommitManifest(spec.Workload, spec.CommitManifest); err != nil {
		return nil, nil, err
	}
	if err := prevalidateMALTFlatRun(spec); err != nil {
		return nil, nil, err
	}
	baselineSpec := rq3baseline.RunSpec{
		System:   rq3baseline.SystemMerkleDAGUnixFS,
		Layout:   rq3baseline.LayoutSpec{Model: "unixfs", FileLayout: "balanced", DirectoryLayout: "basic", Chunking: rq3baseline.ChunkingSpec{Algorithm: "fixed", SizeBytes: int(spec.Workload.ChunkBytes)}, RawFileLeaf: boolPointer(true)},
		Snapshot: spec.Snapshot, Commits: []rq3baseline.Commit{},
	}
	source, logical, err := rq3baseline.StartValidationStream(baselineSpec)
	if err != nil {
		return nil, nil, fmt.Errorf("frozen stream snapshot semantic prevalidation: %w", err)
	}
	if err := w.validateHealth(ctx); err != nil {
		return nil, nil, errors.Join(err, retryMALTCleanup(source.Close))
	}
	payloads, err := newLogicalPayloadStore()
	if err != nil {
		return nil, nil, errors.Join(err, retryMALTCleanup(source.Close))
	}
	roles, err := newBlockRoleIndex()
	if err != nil {
		return nil, nil, errors.Join(err, retryMALTCleanup(source.Close), retryMALTCleanup(payloads.close))
	}
	succeeded := false
	defer func() {
		if !succeeded {
			returnErr = errors.Join(returnErr, retryMALTCleanup(source.Close), retryMALTCleanup(payloads.close), retryMALTCleanup(roles.close))
		}
	}()
	result := &runResult{SchemaVersion: runResultSchema, CapabilityID: capabilityID, System: systemMALTFlat, ResultScope: resultScopeStreamStart, PassMode: spec.PassMode, RunPhase: spec.RunPhase, ClusterID: spec.ClusterID, RunIndex: spec.RunIndex, Workload: spec.Workload, Commits: make([]commitRecord, 0, 1), WriteEvents: []writeEvent{}}
	started := time.Now()
	state, blocks, err := initialLogicalState(spec.Snapshot, spec.Workload.ChunkBytes, payloads)
	if err != nil {
		return nil, nil, err
	}
	if err := payloads.reconcile(state); err != nil {
		return nil, nil, fmt.Errorf("reconcile streamed MALT-flat snapshot payloads: %w", err)
	}
	blocks = append(blocks, flatSentinelBlock())
	roleIndexElapsed, err := uploadClassifiedBlocks(ctx, w.remote, spec.Snapshot.CommitID, blocks, roles, result)
	if err != nil {
		return nil, nil, err
	}
	changes, err := directFlatSnapshotChanges(state)
	if err != nil {
		return nil, nil, err
	}
	oracleStarted := time.Now()
	oracle, err := newFlatRootOracle(ctx, changes)
	oracleElapsed := time.Since(oracleStarted)
	if err != nil {
		return nil, nil, err
	}
	applied, err := w.evaluation.ApplyEvaluationFlatMap(ctx, w.config.bootstrapAuthorizationToken, gatewaytransport.FlatMapMutation{OperationID: operationID(spec.Snapshot.CommitID, 0), Initial: true, Changes: changes})
	if err != nil {
		return nil, nil, fmt.Errorf("apply streamed direct MALT-flat snapshot: %w", err)
	}
	if err := verifyFlatGatewayRoot("stream snapshot", oracle.root, applied.Root); err != nil {
		return nil, nil, err
	}
	if spec.PassMode == "accounting" {
		if err := result.appendGatewayAccounting(spec.Snapshot.CommitID, accountingFromTransport(applied.WriteAccounting)); err != nil {
			return nil, nil, err
		}
	}
	replayNS, err := evaluatorNanos(applied.ReplayNanos)
	if err != nil {
		return nil, nil, err
	}
	persistNS, err := evaluatorNanos(applied.PersistNanos)
	if err != nil {
		return nil, nil, err
	}
	result.Commits = append(result.Commits, commitRecord{Order: 0, CommitID: spec.Snapshot.CommitID, Root: applied.Root.String(), HistoryRootsRetained: 1, LogicalObjectsChanged: logical.LogicalObjectsChanged, LogicalBindingsChanged: logical.LogicalBindingsChanged, LogicalPayloadBytes: logical.AdapterPayloadInputBytes, AdapterPayloadInputBytes: logical.AdapterPayloadInputBytes, ClientComputeWallNS: durationNanos(time.Since(started) - oracleElapsed - roleIndexElapsed), GatewayReplayWallNS: replayNS, GatewayPersistWallNS: persistNS, OracleUnmeasured: true})
	base := spec
	base.Snapshot.Files = nil
	base.Commits = nil
	base.CommitManifest = nil
	succeeded = true
	return &flatDirectStream{worker: w, base: base, source: source, state: state, roles: roles, root: applied.Root, oracle: oracle, nextOrder: 1, commitIDs: []string{spec.Snapshot.CommitID}, commitManifest: append([]string(nil), spec.CommitManifest...), payloads: payloads}, result, nil
}

func (s *flatDirectStream) applyChunk(ctx context.Context, spec runSpec) (*runResult, error) {
	if s == nil || s.worker == nil || s.source == nil || s.failed {
		return nil, fmt.Errorf("direct MALT-flat stream is not active")
	}
	if spec.PassMode != s.base.PassMode || spec.RunPhase != s.base.RunPhase || spec.ClusterID != s.base.ClusterID || spec.RunIndex != s.base.RunIndex || spec.InitialRoot != "" || !reflect.DeepEqual(spec.Workload, s.base.Workload) || spec.Snapshot.CommitID != "" || spec.Snapshot.Files != nil || spec.Commits == nil || len(spec.Commits) == 0 || spec.CommitManifest != nil {
		s.failed = true
		return nil, fmt.Errorf("direct MALT-flat stream chunk does not bind the active run or carries a snapshot")
	}
	if int(s.nextOrder)+len(spec.Commits) > len(s.commitManifest) {
		s.failed = true
		return nil, fmt.Errorf("direct MALT-flat stream chunk exceeds the frozen commit manifest")
	}
	for index, commit := range spec.Commits {
		if commit.CommitID != s.commitManifest[int(s.nextOrder)+index] {
			s.failed = true
			return nil, fmt.Errorf("direct MALT-flat stream chunk commit %d differs from the frozen commit manifest", index)
		}
	}
	if err := prevalidateMALTFlatCommits(spec.Commits); err != nil {
		s.failed = true
		return nil, err
	}
	if err := prevalidateMALTFlatChunk(s.state, spec.Commits, s.base.Workload.ChunkBytes, s.base.Workload.Kind == "git-first-parent" || isControlledListHistory(s.base), s.payloads); err != nil {
		s.failed = true
		return nil, err
	}
	logicalRecords, err := s.source.ApplyChunk(spec.Commits)
	if err != nil {
		s.failed = true
		return nil, fmt.Errorf("frozen stream chunk semantic prevalidation: %w", err)
	}
	result := &runResult{SchemaVersion: runResultSchema, CapabilityID: capabilityID, System: systemMALTFlat, ResultScope: resultScopeStreamChunk, PassMode: s.base.PassMode, RunPhase: s.base.RunPhase, ClusterID: s.base.ClusterID, RunIndex: s.base.RunIndex, Workload: s.base.Workload, Commits: make([]commitRecord, 0, len(spec.Commits)), WriteEvents: []writeEvent{}}
	changedChunksOnly := s.base.Workload.Kind == "git-first-parent" || isControlledListHistory(s.base)
	for index, commit := range spec.Commits {
		s.failed = true
		started := time.Now()
		blocks, canonicalPayloadBytes, logicalChanges, err := applyFrozenCommit(s.state, commit, s.base.Workload.ChunkBytes, changedChunksOnly, s.payloads)
		if err != nil {
			s.failed = true
			return nil, err
		}
		expectedRoot := s.oracle.root
		var gatewayChanges []gatewaytransport.FlatMapChange
		var oracleElapsed time.Duration
		if len(logicalChanges) != 0 {
			gatewayChanges, err = directFlatDeltaChanges(logicalChanges)
			if err != nil {
				return nil, err
			}
			oracleStarted := time.Now()
			expectedRoot, err = s.oracle.apply(ctx, gatewayChanges)
			oracleElapsed = time.Since(oracleStarted)
			if err != nil {
				return nil, err
			}
		}
		if err := verifyFlatGatewayRoot("stream pre-commit", expectedRoot, s.oracle.root); err != nil {
			return nil, err
		}
		roleIndexElapsed, err := uploadClassifiedBlocks(ctx, s.worker.remote, commit.CommitID, blocks, s.roles, result)
		if err != nil {
			s.failed = true
			return nil, err
		}
		nextRoot := s.root
		var replayNS, persistNS int64
		if len(logicalChanges) != 0 {
			next, err := s.worker.evaluation.ApplyEvaluationFlatMap(ctx, s.worker.config.bootstrapAuthorizationToken, gatewaytransport.FlatMapMutation{OperationID: operationID(commit.CommitID, s.nextOrder), BaseRoot: s.root, Changes: gatewayChanges})
			if err != nil {
				s.failed = true
				return nil, fmt.Errorf("apply streamed direct MALT-flat commit %q: %w", commit.CommitID, err)
			}
			if err := verifyFlatGatewayRoot("stream commit "+commit.CommitID, expectedRoot, next.Root); err != nil {
				return nil, err
			}
			if s.base.PassMode == "accounting" {
				if err := result.appendGatewayAccounting(commit.CommitID, accountingFromTransport(next.WriteAccounting)); err != nil {
					s.failed = true
					return nil, err
				}
			}
			replayNS, err = evaluatorNanos(next.ReplayNanos)
			if err != nil {
				return nil, err
			}
			persistNS, err = evaluatorNanos(next.PersistNanos)
			if err != nil {
				return nil, err
			}
			nextRoot = next.Root
		} else if err := verifyFlatGatewayRoot("stream unchanged commit "+commit.CommitID, expectedRoot, nextRoot); err != nil {
			return nil, err
		}
		logical := logicalRecords[index]
		result.Commits = append(result.Commits, commitRecord{Order: s.nextOrder, CommitID: commit.CommitID, ParentRoot: s.root.String(), Root: nextRoot.String(), HistoryRootsRetained: s.nextOrder + 1, LogicalObjectsChanged: logical.LogicalObjectsChanged, LogicalBindingsChanged: logical.LogicalBindingsChanged, LogicalPayloadBytes: canonicalPayloadBytes, AdapterPayloadInputBytes: logical.AdapterPayloadInputBytes, ClientComputeWallNS: durationNanos(time.Since(started) - oracleElapsed - roleIndexElapsed), GatewayReplayWallNS: replayNS, GatewayPersistWallNS: persistNS, OracleUnmeasured: true})
		s.root = nextRoot
		s.nextOrder++
		s.commitIDs = append(s.commitIDs, commit.CommitID)
		if err := s.payloads.reconcile(s.state); err != nil {
			return nil, fmt.Errorf("reconcile streamed MALT-flat commit %q payloads: %w", commit.CommitID, err)
		}
		s.failed = false
	}
	return result, nil
}

func (s *flatDirectStream) finish(ctx context.Context) (streamStatus, error) {
	if s == nil || s.worker == nil || s.source == nil || s.failed {
		return streamStatus{}, fmt.Errorf("direct MALT-flat stream is not active")
	}
	if int(s.nextOrder) != len(s.commitManifest) || !slices.Equal(s.commitIDs, s.commitManifest) {
		s.failed = true
		return streamStatus{}, fmt.Errorf("direct MALT-flat stream ended before the frozen commit manifest was exhausted")
	}
	scheme, err := kzg.NewScheme()
	if err != nil {
		s.failed = true
		return streamStatus{}, fmt.Errorf("initialize final streamed flat oracle: %w", err)
	}
	oracle, err := (graphBuilder{chunkBytes: s.base.Workload.ChunkBytes, scheme: scheme, store: materializermemory.New(true)}).buildFlat(ctx, s.state)
	if err != nil {
		s.failed = true
		return streamStatus{}, fmt.Errorf("build final streamed MALT-flat independent full oracle: %w", err)
	}
	if !oracle.root.Equals(s.root) {
		s.failed = true
		return streamStatus{}, fmt.Errorf("final streamed MALT-flat root differs from independent full oracle")
	}
	if s.oracle == nil || !s.oracle.root.Equals(s.root) {
		s.failed = true
		return streamStatus{}, fmt.Errorf("final streamed MALT-flat root differs from incremental per-commit oracle")
	}
	status := streamStatus{Root: s.root.String(), CommitsApplied: s.nextOrder, Complete: true}
	if err := s.close(); err != nil {
		return streamStatus{}, err
	}
	return status, nil
}

func (s *flatDirectStream) close() error {
	if s == nil {
		return nil
	}
	var sourceErr error
	if s.source != nil {
		sourceErr = s.source.Close()
		if sourceErr == nil {
			s.source = nil
		}
	}
	var payloadErr error
	if s.payloads != nil {
		payloadErr = s.payloads.close()
		if payloadErr == nil {
			s.payloads = nil
		}
	}
	var rolesErr error
	if s.roles != nil {
		rolesErr = s.roles.close()
		if rolesErr == nil {
			s.roles = nil
		}
	}
	s.failed = true
	return errors.Join(sourceErr, payloadErr, rolesErr)
}

func (w *campaignWorker) runFlatDirect(ctx context.Context, spec runSpec) (_ *runResult, returnErr error) {
	payloads, err := newLogicalPayloadStore()
	if err != nil {
		return nil, err
	}
	roles, err := newBlockRoleIndex()
	if err != nil {
		return nil, errors.Join(err, retryMALTCleanup(payloads.close))
	}
	defer func() {
		returnErr = errors.Join(returnErr, retryMALTCleanup(payloads.close), retryMALTCleanup(roles.close))
	}()
	baselineSpec := rq3baseline.RunSpec{
		System: rq3baseline.SystemMerkleDAGUnixFS,
		Layout: rq3baseline.LayoutSpec{
			Model: "unixfs", FileLayout: "balanced", DirectoryLayout: "basic",
			Chunking:    rq3baseline.ChunkingSpec{Algorithm: "fixed", SizeBytes: int(spec.Workload.ChunkBytes)},
			RawFileLeaf: boolPointer(true),
		},
		Snapshot: spec.Snapshot, Commits: spec.Commits,
	}
	source, err := rq3baseline.ValidateAndAccountSource(baselineSpec)
	if err != nil {
		return nil, fmt.Errorf("frozen workload semantic prevalidation: %w", err)
	}
	result := &runResult{
		SchemaVersion: runResultSchema, CapabilityID: capabilityID, System: systemMALTFlat,
		ResultScope: resultScopeComplete,
		PassMode:    spec.PassMode, RunPhase: spec.RunPhase, ClusterID: spec.ClusterID, RunIndex: spec.RunIndex,
		Workload: spec.Workload, Commits: make([]commitRecord, 0, len(source)), WriteEvents: []writeEvent{},
	}
	started := time.Now()
	state, blocks, err := initialLogicalState(spec.Snapshot, spec.Workload.ChunkBytes, payloads)
	if err != nil {
		return nil, err
	}
	if err := payloads.reconcile(state); err != nil {
		return nil, fmt.Errorf("reconcile direct MALT-flat snapshot payloads: %w", err)
	}
	blocks = append(blocks, flatSentinelBlock())
	roleIndexElapsed, err := uploadClassifiedBlocks(ctx, w.remote, spec.Snapshot.CommitID, blocks, roles, result)
	if err != nil {
		return nil, err
	}
	changes, err := directFlatSnapshotChanges(state)
	if err != nil {
		return nil, err
	}
	oracleStarted := time.Now()
	rootOracle, err := newFlatRootOracle(ctx, changes)
	oracleElapsed := time.Since(oracleStarted)
	if err != nil {
		return nil, err
	}
	applied, err := w.evaluation.ApplyEvaluationFlatMap(ctx, w.config.bootstrapAuthorizationToken, gatewaytransport.FlatMapMutation{
		OperationID: operationID(spec.Snapshot.CommitID, 0), Initial: true, Changes: changes,
	})
	if err != nil {
		return nil, fmt.Errorf("apply direct MALT-flat snapshot: %w", err)
	}
	if err := verifyFlatGatewayRoot("snapshot", rootOracle.root, applied.Root); err != nil {
		return nil, err
	}
	clientNS := durationNanos(time.Since(started) - oracleElapsed - roleIndexElapsed)
	if spec.PassMode == "accounting" {
		if err := result.appendGatewayAccounting(spec.Snapshot.CommitID, accountingFromTransport(applied.WriteAccounting)); err != nil {
			return nil, err
		}
	}
	replayNS, err := evaluatorNanos(applied.ReplayNanos)
	if err != nil {
		return nil, err
	}
	persistNS, err := evaluatorNanos(applied.PersistNanos)
	if err != nil {
		return nil, err
	}
	result.Commits = append(result.Commits, commitRecord{
		Order: 0, CommitID: spec.Snapshot.CommitID, Root: applied.Root.String(), HistoryRootsRetained: 1,
		LogicalObjectsChanged: source[0].LogicalObjectsChanged, LogicalBindingsChanged: source[0].LogicalBindingsChanged,
		LogicalPayloadBytes: source[0].AdapterPayloadInputBytes, AdapterPayloadInputBytes: source[0].AdapterPayloadInputBytes,
		ClientComputeWallNS: clientNS, GatewayReplayWallNS: replayNS,
		GatewayPersistWallNS: persistNS, OracleUnmeasured: true,
	})
	root := applied.Root
	changedChunksOnly := spec.Workload.Kind == "git-first-parent" || isControlledListHistory(spec)
	for index, commit := range spec.Commits {
		started = time.Now()
		blocks, canonicalPayloadBytes, logicalChanges, err := applyFrozenCommit(state, commit, spec.Workload.ChunkBytes, changedChunksOnly, payloads)
		if err != nil {
			return nil, err
		}
		expectedRoot := rootOracle.root
		var gatewayChanges []gatewaytransport.FlatMapChange
		oracleElapsed = 0
		if len(logicalChanges) != 0 {
			gatewayChanges, err = directFlatDeltaChanges(logicalChanges)
			if err != nil {
				return nil, err
			}
			oracleStarted = time.Now()
			expectedRoot, err = rootOracle.apply(ctx, gatewayChanges)
			oracleElapsed = time.Since(oracleStarted)
			if err != nil {
				return nil, err
			}
		}
		roleIndexElapsed, err := uploadClassifiedBlocks(ctx, w.remote, commit.CommitID, blocks, roles, result)
		if err != nil {
			return nil, err
		}
		nextRoot := root
		replayNS, persistNS = 0, 0
		if len(logicalChanges) != 0 {
			next, err := w.evaluation.ApplyEvaluationFlatMap(ctx, w.config.bootstrapAuthorizationToken, gatewaytransport.FlatMapMutation{
				OperationID: operationID(commit.CommitID, uint32(index+1)), BaseRoot: root, Changes: gatewayChanges,
			})
			if err != nil {
				return nil, fmt.Errorf("apply direct MALT-flat commit %q: %w", commit.CommitID, err)
			}
			if err := verifyFlatGatewayRoot("commit "+commit.CommitID, expectedRoot, next.Root); err != nil {
				return nil, err
			}
			if spec.PassMode == "accounting" {
				if err := result.appendGatewayAccounting(commit.CommitID, accountingFromTransport(next.WriteAccounting)); err != nil {
					return nil, err
				}
			}
			replayNS, err = evaluatorNanos(next.ReplayNanos)
			if err != nil {
				return nil, err
			}
			persistNS, err = evaluatorNanos(next.PersistNanos)
			if err != nil {
				return nil, err
			}
			nextRoot = next.Root
		} else if err := verifyFlatGatewayRoot("unchanged commit "+commit.CommitID, expectedRoot, nextRoot); err != nil {
			return nil, err
		}
		clientNS = durationNanos(time.Since(started) - oracleElapsed - roleIndexElapsed)
		logical := source[index+1]
		result.Commits = append(result.Commits, commitRecord{
			Order: uint32(index + 1), CommitID: commit.CommitID, ParentRoot: root.String(), Root: nextRoot.String(),
			HistoryRootsRetained: uint32(index + 2), LogicalObjectsChanged: logical.LogicalObjectsChanged,
			LogicalBindingsChanged: logical.LogicalBindingsChanged, LogicalPayloadBytes: canonicalPayloadBytes,
			AdapterPayloadInputBytes: logical.AdapterPayloadInputBytes, ClientComputeWallNS: clientNS,
			GatewayReplayWallNS: replayNS, GatewayPersistWallNS: persistNS,
			OracleUnmeasured: true,
		})
		root = nextRoot
		if err := payloads.reconcile(state); err != nil {
			return nil, fmt.Errorf("reconcile direct MALT-flat commit %q payloads: %w", commit.CommitID, err)
		}
	}
	scheme, err := kzg.NewScheme()
	if err != nil {
		return nil, fmt.Errorf("initialize final direct flat oracle: %w", err)
	}
	oracle, err := (graphBuilder{chunkBytes: spec.Workload.ChunkBytes, scheme: scheme, store: materializermemory.New(true)}).buildFlat(ctx, state)
	if err != nil {
		return nil, fmt.Errorf("build final direct MALT-flat independent full oracle: %w", err)
	}
	if !oracle.root.Equals(root) {
		return nil, fmt.Errorf("final direct MALT-flat root differs from independent full oracle")
	}
	if !rootOracle.root.Equals(root) {
		return nil, fmt.Errorf("final direct MALT-flat root differs from incremental per-commit oracle")
	}
	if spec.PassMode == "timing" && len(result.WriteEvents) != 0 {
		return nil, fmt.Errorf("timing pass retained byte-accounting events")
	}
	return result, nil
}

func prevalidateMALTFlatRun(spec runSpec) error {
	if len(spec.Snapshot.Files) > maximumMALTSnapshotFiles {
		return fmt.Errorf("MALT-flat snapshot has %d files; maximum is %d", len(spec.Snapshot.Files), maximumMALTSnapshotFiles)
	}
	for index, file := range spec.Snapshot.Files {
		payload, err := decodeFrozenPayload(file.PayloadBase64)
		if err != nil {
			return fmt.Errorf("MALT-flat snapshot file[%d]: %w", index, err)
		}
		if len(payload) > maximumMALTWholeFileBytes {
			return fmt.Errorf("MALT-flat snapshot file[%d] exceeds %d-byte whole-file CAS limit", index, maximumMALTWholeFileBytes)
		}
	}
	return prevalidateMALTFlatCommits(spec.Commits)
}

func prevalidateMALTFlatCommits(commits []rq3baseline.Commit) error {
	total := 0
	for commitIndex, commit := range commits {
		total += len(commit.Mutations)
		if total > maximumMALTChunkMutations {
			return fmt.Errorf("MALT-flat chunk has more than %d mutations", maximumMALTChunkMutations)
		}
		for mutationIndex, mutation := range commit.Mutations {
			if mutation.PayloadBase64 == "" {
				continue
			}
			payload, err := decodeFrozenPayload(mutation.PayloadBase64)
			if err != nil {
				return fmt.Errorf("MALT-flat commit[%d] mutation[%d]: %w", commitIndex, mutationIndex, err)
			}
			if len(payload) > maximumMALTWholeFileBytes {
				return fmt.Errorf("MALT-flat commit[%d] mutation[%d] exceeds %d-byte whole-file CAS limit", commitIndex, mutationIndex, maximumMALTWholeFileBytes)
			}
		}
	}
	return nil
}

func prevalidateMALTFlatChunk(state map[string]logicalFile, commits []rq3baseline.Commit, chunkBytes uint64, changedChunksOnly bool, payloads *logicalPayloadStore) (returnErr error) {
	overlay, err := newLogicalPayloadOverlay(payloads)
	if err != nil {
		return fmt.Errorf("create MALT-flat chunk prevalidation payload overlay: %w", err)
	}
	defer func() { returnErr = errors.Join(returnErr, retryMALTCleanup(overlay.close)) }()
	projected := make(map[string]logicalFile, len(state))
	for path, file := range state {
		projected[path] = file
	}
	for index, commit := range commits {
		_, _, changes, err := applyFrozenCommit(projected, commit, chunkBytes, changedChunksOnly, overlay)
		if err != nil {
			return fmt.Errorf("prevalidate MALT-flat commit[%d]: %w", index, err)
		}
		if len(changes) == 0 {
			continue
		}
		if _, err := directFlatDeltaChanges(changes); err != nil {
			return fmt.Errorf("prevalidate MALT-flat commit[%d] changes: %w", index, err)
		}
	}
	return nil
}

func directFlatSnapshotChanges(state map[string]logicalFile) ([]gatewaytransport.FlatMapChange, error) {
	blueprint, err := buildFlatBlueprint(state, 1)
	if err != nil {
		return nil, err
	}
	entries := blueprint.objects[blueprint.topID].entries
	changes := make([]gatewaytransport.FlatMapChange, len(entries))
	if len(changes) > maximumGatewayFlatMapChanges {
		return nil, fmt.Errorf("direct flat snapshot exceeds %d Gateway changes", maximumGatewayFlatMapChanges)
	}
	for index, entry := range entries {
		if entry.literal == nil {
			return nil, fmt.Errorf("direct flat snapshot contains a nonliteral target")
		}
		changes[index] = gatewaytransport.FlatMapChange{
			Path: arcset.CanonicalizePath(entry.coordinate.String()), After: entry.literal.CID(),
		}
	}
	slices.SortFunc(changes, compareFlatChanges)
	return changes, nil
}

func directFlatDeltaChanges(changes []fileChange) ([]gatewaytransport.FlatMapChange, error) {
	result := make([]gatewaytransport.FlatMapChange, 0, 2*len(changes))
	for _, change := range changes {
		for _, mode := range []bool{false, true} {
			coordinate, err := flatCoordinate(change.path, mode)
			if err != nil {
				return nil, err
			}
			before, err := directFlatTarget(change.before, mode)
			if err != nil {
				return nil, err
			}
			after, err := directFlatTarget(change.after, mode)
			if err != nil {
				return nil, err
			}
			if before.Defined() && after.Defined() && before.Equals(after) {
				continue
			}
			result = append(result, gatewaytransport.FlatMapChange{
				Path: arcset.CanonicalizePath(coordinate.String()), Before: before, After: after,
			})
		}
	}
	slices.SortFunc(result, compareFlatChanges)
	if len(result) == 0 {
		return nil, fmt.Errorf("direct flat delta contains no changed coordinates")
	}
	if len(result) > maximumGatewayFlatMapChanges {
		return nil, fmt.Errorf("direct flat delta exceeds %d Gateway changes", maximumGatewayFlatMapChanges)
	}
	return result, nil
}

func directFlatTarget(file *logicalFile, mode bool) (cid.Cid, error) {
	if file == nil {
		return cid.Undef, nil
	}
	if !mode && file.payload.Defined() {
		return file.payload, nil
	}
	block := transport.Block{Codec: cid.Raw, Data: file.data}
	if mode {
		sidecar, err := modeCASBlock(file.mode)
		if err != nil {
			return cid.Undef, err
		}
		block = sidecar.block
	}
	return clientcas.CIDForBlock(block)
}

func compareFlatChanges(left, right gatewaytransport.FlatMapChange) int {
	return strings.Compare(left.Path.String(), right.Path.String())
}

package rq3baseline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"time"

	merkledagimport "github.com/dewebprotocol/malt-client/merkledag/importer"
)

// StreamSession retains one Editor and accounting CAS across bounded chunks.
type StreamSession struct {
	spec            RunSpec
	store           *accountingStore
	editor          *merkledagimport.Editor
	state           map[string]logicalFile
	validationState map[string]logicalFile
	payloads        *payloadStore
	seenCommits     map[string]struct{}
	parentRoot      string
	failed          bool
	commitIDs       []string
}

// StartStream validates and applies one immutable initial snapshot.
func StartStream(ctx context.Context, spec RunSpec) (_ *StreamSession, _ CommitRecord, returnErr error) {
	if spec.Commits == nil || len(spec.Commits) != 0 {
		return nil, CommitRecord{}, fmt.Errorf("stream start requires an explicit empty commit chunk")
	}
	store := newAccountingStore()
	payloads, err := newPayloadStore()
	if err != nil {
		return nil, CommitRecord{}, errors.Join(err, retryCleanup(store.close))
	}
	succeeded := false
	defer func() {
		if !succeeded {
			returnErr = errors.Join(returnErr, retryCleanup(store.close), retryCleanup(payloads.close))
		}
	}()
	state, source, err := prepareStreamSnapshot(spec, payloads)
	if err != nil {
		return nil, CommitRecord{}, err
	}
	editor, err := merkledagimport.NewEditor(store, importerOptions(spec))
	if err != nil {
		return nil, CommitRecord{}, fmt.Errorf("create UnixFS stream editor: %w", err)
	}
	minimal := RunSpec{System: spec.System, Layout: spec.Layout}
	session := &StreamSession{spec: minimal, store: store, editor: editor, state: cloneLogicalState(state), validationState: state, payloads: payloads, seenCommits: map[string]struct{}{spec.Snapshot.CommitID: {}}, commitIDs: []string{spec.Snapshot.CommitID}}
	if err := payloads.reconcile(session.state, session.validationState); err != nil {
		return nil, CommitRecord{}, fmt.Errorf("reconcile hash stream snapshot payloads: %w", err)
	}
	store.beginPhase()
	started := time.Now()
	executions := make([]MutationExecution, 0, len(spec.Snapshot.Files))
	for index, frozen := range spec.Snapshot.Files {
		if err := ctx.Err(); err != nil {
			return nil, CommitRecord{}, err
		}
		file := state[frozen.Path]
		data, err := payloads.read(file)
		if err != nil {
			return nil, CommitRecord{}, fmt.Errorf("snapshot commit %q read %q: %w", spec.Snapshot.CommitID, frozen.Path, err)
		}
		if err := editor.PutFile(ctx, frozen.Path, data, fs.FileMode(file.mode)); err != nil {
			return nil, CommitRecord{}, fmt.Errorf("snapshot commit %q put %q: %w", spec.Snapshot.CommitID, frozen.Path, err)
		}
		executions = append(executions, MutationExecution{Index: index, Kind: MutationInsert, Path: frozen.Path, Translation: "snapshot_put_file", LogicalObjectsChanged: 1, LogicalBindingsChanged: 1, LogicalPayloadBytes: int64(len(data))})
	}
	elapsed := time.Since(started).Nanoseconds()
	root := editor.Root()
	if root == "" {
		return nil, CommitRecord{}, fmt.Errorf("snapshot commit %q produced an empty root", spec.Snapshot.CommitID)
	}
	accounting, putNanos, getNanos := store.finishPhase()
	session.parentRoot = root
	succeeded = true
	return session, CommitRecord{CommitID: spec.Snapshot.CommitID, Root: root, Snapshot: true, LogicalObjectsChanged: source.LogicalObjectsChanged, LogicalBindingsChanged: source.LogicalBindingsChanged, LogicalPayloadBytes: source.AdapterPayloadInputBytes, AdapterPayloadInputBytes: source.AdapterPayloadInputBytes, Mutations: executions, CAS: accounting, ClientPhases: phaseMetrics(elapsed, putNanos, getNanos)}, nil
}

// ApplyChunk prevalidates a complete bounded chunk before mutating the Editor.
func (s *StreamSession) ApplyChunk(ctx context.Context, commits []Commit) ([]CommitRecord, error) {
	if s == nil || s.editor == nil || s.store == nil || s.failed {
		return nil, fmt.Errorf("hash stream session is not initialized")
	}
	prepared, nextValidation, nextSeen, source, err := s.prepareChunk(commits)
	if err != nil {
		s.failed = true
		return nil, err
	}
	s.failed = true
	records := make([]CommitRecord, 0, len(prepared))
	for commitIndex, commit := range prepared {
		s.store.beginPhase()
		started := time.Now()
		executions := make([]MutationExecution, 0, len(commit.mutations))
		logicalObjects, logicalBindings := 0, 0
		var logicalPayload int64
		for index, mutation := range commit.mutations {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			execution, err := executeMutation(ctx, s.editor, s.state, index, mutation, s.payloads)
			if err != nil {
				return nil, fmt.Errorf("commit %q mutation[%d]: %w", commit.id, index, err)
			}
			executions = append(executions, execution)
			logicalObjects += execution.LogicalObjectsChanged
			logicalBindings += execution.LogicalBindingsChanged
			logicalPayload += execution.LogicalPayloadBytes
		}
		elapsed := time.Since(started).Nanoseconds()
		root := s.editor.Root()
		if root == "" {
			return nil, fmt.Errorf("commit %q produced an empty root", commit.id)
		}
		accounting, putNanos, getNanos := s.store.finishPhase()
		records = append(records, CommitRecord{CommitID: commit.id, ParentRoot: s.parentRoot, Root: root, LogicalObjectsChanged: logicalObjects, LogicalBindingsChanged: logicalBindings, LogicalPayloadBytes: logicalPayload, AdapterPayloadInputBytes: source[commitIndex].AdapterPayloadInputBytes, Mutations: executions, CAS: accounting, ClientPhases: phaseMetrics(elapsed, putNanos, getNanos)})
		s.parentRoot = root
	}
	if err := s.payloads.reconcile(s.state, nextValidation); err != nil {
		return nil, fmt.Errorf("reconcile hash stream chunk payloads: %w", err)
	}
	s.validationState, s.seenCommits = nextValidation, nextSeen
	for _, commit := range commits {
		s.commitIDs = append(s.commitIDs, commit.CommitID)
	}
	s.failed = false
	return records, nil
}

func (s *StreamSession) CommitListSHA256() (string, error) {
	if s == nil || s.failed || len(s.commitIDs) == 0 {
		return "", fmt.Errorf("hash stream commit list is unavailable")
	}
	encoded, err := json.Marshal(struct {
		Commits []string `json:"commits"`
	}{Commits: s.commitIDs})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func (s *StreamSession) prepareChunk(commits []Commit) ([]preparedCommit, map[string]logicalFile, map[string]struct{}, []SourceCommitAccounting, error) {
	return prepareStreamChunk(s.spec, s.validationState, s.seenCommits, commits, s.payloads)
}

func prepareStreamChunk(spec RunSpec, validationState map[string]logicalFile, seenCommits map[string]struct{}, commits []Commit, payloads *payloadStore) ([]preparedCommit, map[string]logicalFile, map[string]struct{}, []SourceCommitAccounting, error) {
	if commits == nil || len(commits) == 0 || len(commits) > maxCommits {
		return nil, nil, nil, nil, fmt.Errorf("stream chunk requires 1..%d commits", maxCommits)
	}
	state := cloneLogicalState(validationState)
	seen := make(map[string]struct{}, len(seenCommits)+len(commits))
	for commit := range seenCommits {
		seen[commit] = struct{}{}
	}
	prepared := make([]preparedCommit, 0, len(commits))
	source := make([]SourceCommitAccounting, 0, len(commits))
	mutationCount, totalPayload, projected := 0, 0, 0
	for commitIndex, commit := range commits {
		if err := validateCommitID(fmt.Sprintf("stream commits[%d].commit_id", commitIndex), commit.CommitID); err != nil {
			return nil, nil, nil, nil, err
		}
		if _, duplicate := seen[commit.CommitID]; duplicate {
			return nil, nil, nil, nil, fmt.Errorf("duplicate commit_id %q", commit.CommitID)
		}
		seen[commit.CommitID] = struct{}{}
		if commit.Mutations == nil {
			return nil, nil, nil, nil, fmt.Errorf("commit %q mutations must be an explicit array", commit.CommitID)
		}
		mutationCount += len(commit.Mutations)
		if mutationCount > maxMutations {
			return nil, nil, nil, nil, fmt.Errorf("stream chunk has more than %d mutations", maxMutations)
		}
		next := preparedCommit{id: commit.CommitID, mutations: make([]preparedMutation, 0, len(commit.Mutations))}
		accounting := SourceCommitAccounting{CommitID: commit.CommitID, LogicalObjectsChanged: len(commit.Mutations)}
		for mutationIndex, mutation := range commit.Mutations {
			decoded, added, err := validateAndApplyMutation(state, commit.CommitID, mutationIndex, mutation, payloads)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			totalPayload += added
			if totalPayload > maxPayloadBytes {
				return nil, nil, nil, nil, fmt.Errorf("stream chunk decoded payload bytes exceed %d", maxPayloadBytes)
			}
			if mutation.Kind == MutationInsert || mutation.Kind == MutationReplace || mutation.Kind == MutationAppend {
				projected += projectedChunks(len(decoded), spec.Layout.Chunking.SizeBytes)
				accounting.AdapterPayloadInputBytes += int64(len(decoded))
			}
			if projected > maxProjectedChunks {
				return nil, nil, nil, nil, fmt.Errorf("stream chunk projected payload chunks exceed %d", maxProjectedChunks)
			}
			if mutation.Kind == MutationRename || mutation.Kind == MutationMove {
				accounting.LogicalBindingsChanged += 2
			} else {
				accounting.LogicalBindingsChanged++
			}
			next.mutations = append(next.mutations, preparedMutation{mutation: mutation, data: decoded})
		}
		if len(state) > maxFiles {
			return nil, nil, nil, nil, fmt.Errorf("stream state has more than %d files", maxFiles)
		}
		prepared = append(prepared, next)
		source = append(source, accounting)
	}
	return prepared, state, seen, source, nil
}

// Close releases the private disk-backed logical CAS retained by a stream.
func (s *StreamSession) Close() error {
	if s == nil || s.store == nil && s.payloads == nil {
		return nil
	}
	var storeErr, payloadErr error
	if s.store != nil {
		storeErr = s.store.close()
		if storeErr == nil {
			s.store = nil
		}
	}
	if s.payloads != nil {
		payloadErr = s.payloads.close()
		if payloadErr == nil {
			s.payloads = nil
		}
	}
	s.editor = nil
	s.failed = true
	return errors.Join(storeErr, payloadErr)
}

func cloneLogicalState(source map[string]logicalFile) map[string]logicalFile {
	result := make(map[string]logicalFile, len(source))
	for path, file := range source {
		result[path] = file
	}
	return result
}

func (s *StreamSession) Root() string {
	if s == nil {
		return ""
	}
	return s.parentRoot
}

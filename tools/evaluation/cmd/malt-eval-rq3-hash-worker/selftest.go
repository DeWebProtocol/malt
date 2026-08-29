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
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/dewebprotocol/malt-client/internal/evaluation/e0selftest"
	"github.com/dewebprotocol/malt-client/internal/evaluation/rq3baseline"
)

const hashAdapterSelfTestCorpusSchema = "malt-eval-rq3-hash-adapter-self-test/v3"

var hashAdapterSelfTestProfile = e0selftest.Profile{
	ProfileID: "rq3-hash-adapters-positive-hostile-v3",
	PositiveCases: []string{
		"execute-controlled-merkledag",
		"execute-controlled-hamt",
		"execute-git-first-parent-merkledag",
		"execute-git-first-parent-hamt",
	},
	HostileCases: []string{
		"reject-malformed-request",
		"reject-tampered-payload",
		"reject-stale-before-value",
		"detect-commit-list-digest-mismatch",
		"reject-unfinished-stream",
		"reject-failed-stream-reuse",
		"reject-active-stream-binding-reuse",
	},
}

type hashAdapterSelfTestCorpus struct {
	SchemaVersion string                            `json:"schema_version"`
	PositiveCases []hashAdapterSelfTestPositiveCase `json:"positive_cases"`
	HostileCases  []hashAdapterSelfTestHostileCase  `json:"hostile_cases"`
}

type hashAdapterSelfTestPositiveCase struct {
	ID           string              `json:"id"`
	SourceKind   string              `json:"source_kind"`
	Run          rq3baseline.RunSpec `json:"run"`
	TraceBinding *hashTraceBinding   `json:"trace_binding,omitempty"`
}

type hashTraceBinding struct {
	TraceID          string   `json:"trace_id"`
	CommitIDs        []string `json:"commit_ids"`
	CommitListSHA256 string   `json:"commit_list_sha256"`
}

type hashAdapterSelfTestHostileCase struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
}

type repeatedPathFlag []string

func (f *repeatedPathFlag) String() string { return strings.Join(*f, ",") }
func (f *repeatedPathFlag) Set(value string) error {
	*f = append(*f, value)
	return nil
}

func runHashAdapterSelfTest(ctx context.Context, corpusPath string, boundWorkloads, gitExecutables []string, output io.Writer) error {
	if err := validateHashSelfTestInputs(corpusPath, boundWorkloads, gitExecutables); err != nil {
		return err
	}
	var corpus hashAdapterSelfTestCorpus
	if err := decodeHashSelfTestCorpus(corpusPath, &corpus); err != nil {
		return err
	}
	if err := corpus.validate(); err != nil {
		return err
	}
	results := make([]e0selftest.CaseResult, 0, len(corpus.PositiveCases)+len(corpus.HostileCases))
	for _, testCase := range corpus.PositiveCases {
		if err := executeHashPositiveCase(ctx, testCase); err != nil {
			return fmt.Errorf("hash adapter self-test positive case %q: %w", testCase.ID, err)
		}
		results = append(results, e0selftest.CaseResult{ID: testCase.ID, Passed: true})
	}
	for _, testCase := range corpus.HostileCases {
		if err := executeHashHostileCase(ctx, testCase, corpus.PositiveCases); err != nil {
			return fmt.Errorf("hash adapter self-test hostile case %q: %w", testCase.ID, err)
		}
		results = append(results, e0selftest.CaseResult{ID: testCase.ID, Passed: true})
	}
	receipt, err := e0selftest.Issue("rq3.hash-system-adapters", hashAdapterSelfTestProfile, results)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(receipt)
}

func (c hashAdapterSelfTestCorpus) validate() error {
	if c.SchemaVersion != hashAdapterSelfTestCorpusSchema || len(c.PositiveCases) != len(hashAdapterSelfTestProfile.PositiveCases) || len(c.HostileCases) != len(hashAdapterSelfTestProfile.HostileCases) {
		return errors.New("hash adapter self-test corpus has an invalid schema or case count")
	}
	for index, testCase := range c.PositiveCases {
		if testCase.ID != hashAdapterSelfTestProfile.PositiveCases[index] {
			return fmt.Errorf("hash adapter positive case %d has id %q", index, testCase.ID)
		}
		wantSystem := rq3baseline.SystemMerkleDAGUnixFS
		wantSource := "controlled"
		if testCase.ID == "execute-controlled-hamt" {
			wantSystem = rq3baseline.SystemHAMTUnixFS
		}
		if testCase.ID == "execute-git-first-parent-merkledag" || testCase.ID == "execute-git-first-parent-hamt" {
			wantSource = "git-first-parent"
		}
		if testCase.ID == "execute-git-first-parent-hamt" {
			wantSystem = rq3baseline.SystemHAMTUnixFS
		}
		if testCase.SourceKind != wantSource || testCase.Run.System != wantSystem {
			return fmt.Errorf("hash adapter positive case %q has a mismatched source/system", testCase.ID)
		}
		if testCase.SourceKind == "git-first-parent" {
			if testCase.TraceBinding == nil || validateHashTraceBinding(testCase.Run, *testCase.TraceBinding) != nil {
				return fmt.Errorf("hash adapter positive case %q has an invalid trace binding", testCase.ID)
			}
		} else if testCase.TraceBinding != nil {
			return fmt.Errorf("controlled hash adapter case %q carries a trace binding", testCase.ID)
		}
	}
	wantKinds := []string{"malformed-request", "tampered-payload", "stale-before-value", "commit-list-digest-mismatch", "unfinished-stream", "failed-stream-reuse", "active-stream-binding-reuse"}
	for index, testCase := range c.HostileCases {
		if testCase.ID != hashAdapterSelfTestProfile.HostileCases[index] || testCase.Kind != wantKinds[index] {
			return fmt.Errorf("hash adapter hostile case %d does not match the compiled contract", index)
		}
	}
	return nil
}

func executeHashPositiveCase(ctx context.Context, testCase hashAdapterSelfTestPositiveCase) error {
	handler := new(workerHandler)
	if response, err := sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: testCase.ID + "-cap", Operation: rq3baseline.OperationCapabilities}); err != nil || !response.OK {
		return fmt.Errorf("production hash capability preflight: response=%#v err=%w", response, err)
	}
	start := testCase.Run
	start.Commits = []rq3baseline.Commit{}
	response, err := sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: testCase.ID + "-start", Operation: rq3baseline.OperationStreamStart, Run: &start})
	if err != nil || !response.OK || response.Stream == nil || len(response.Stream.Records) != 1 {
		return fmt.Errorf("production hash stream start: response=%#v err=%w", response, err)
	}
	records := append([]rq3baseline.CommitRecord{}, response.Stream.Records...)
	for index, commit := range testCase.Run.Commits {
		chunk := rq3baseline.RunSpec{System: testCase.Run.System, Layout: testCase.Run.Layout, Commits: []rq3baseline.Commit{commit}}
		response, err = sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: fmt.Sprintf("%s-chunk-%d", testCase.ID, index), Operation: rq3baseline.OperationStreamChunk, Run: &chunk})
		if err != nil || !response.OK || response.Stream == nil || len(response.Stream.Records) != 1 {
			return fmt.Errorf("production hash stream chunk %d: response=%#v err=%w", index, response, err)
		}
		records = append(records, response.Stream.Records...)
	}
	response, err = sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: testCase.ID + "-finish", Operation: rq3baseline.OperationStreamFinish})
	wantDigest, digestErr := hashCommitListDigest(append([]string{testCase.Run.Snapshot.CommitID}, func() []string {
		values := make([]string, len(testCase.Run.Commits))
		for i := range testCase.Run.Commits {
			values[i] = testCase.Run.Commits[i].CommitID
		}
		return values
	}()...))
	if err != nil || digestErr != nil || !response.OK || response.Stream == nil || !response.Stream.Complete || response.Stream.CommitsApplied != uint32(len(records)) || response.Stream.CommitListSHA256 != wantDigest {
		return fmt.Errorf("production hash stream finish: response=%#v err=%w", response, err)
	}
	for index, record := range records {
		want := testCase.Run.Snapshot.CommitID
		if index > 0 {
			want = testCase.Run.Commits[index-1].CommitID
		}
		if record.CommitID != want || record.Root == "" {
			return fmt.Errorf("hash result record %d does not bind commit %q", index, want)
		}
		if index > 0 && len(testCase.Run.Commits[index-1].Mutations) == 0 &&
			(record.Root != record.ParentRoot || record.LogicalObjectsChanged != 0 || record.LogicalBindingsChanged != 0 || record.LogicalPayloadBytes != 0 || record.AdapterPayloadInputBytes != 0 ||
				record.CAS.Total.AttemptedObjects != 0 || record.CAS.Total.AttemptedBytes != 0 || record.CAS.Total.NewlyPersistedObjects != 0 || record.CAS.Total.NewlyPersistedBytes != 0) {
			return fmt.Errorf("hash no-op commit %q changed the root or emitted writes", want)
		}
	}
	return nil
}

func sendHashSelfTestRequest(ctx context.Context, handler *workerHandler, request rq3baseline.WorkerRequest) (rq3baseline.WorkerResponse, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return rq3baseline.WorkerResponse{}, err
	}
	return handler.handleLine(ctx, encoded), nil
}

func executeHashHostileCase(ctx context.Context, testCase hashAdapterSelfTestHostileCase, positives []hashAdapterSelfTestPositiveCase) error {
	switch testCase.Kind {
	case "malformed-request":
		handler := new(workerHandler)
		if response, err := sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: "cap", Operation: rq3baseline.OperationCapabilities}); err != nil || !response.OK {
			return errors.New("production hash capability preflight failed")
		}
		response := handler.handleLine(ctx, []byte(`{"schema_version":"malt-rq3-hash-worker-request/v1","request_id":"malformed","operation":"stream-start","unknown":true}`))
		if response.OK || response.Error == nil || response.Error.Code != "invalid_request" {
			return errors.New("production hash request decoder accepted an unknown field")
		}
		return nil
	case "tampered-payload":
		run, err := cloneHashRun(positives[0].Run)
		if err != nil {
			return err
		}
		run.Snapshot.Files[0].PayloadBase64 = "dGFtcGVyZWQ="
		run.Commits = []rq3baseline.Commit{}
		handler := new(workerHandler)
		_, _ = sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: "cap", Operation: rq3baseline.OperationCapabilities})
		response, err := sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: "tampered", Operation: rq3baseline.OperationStreamStart, Run: &run})
		if err != nil || response.OK || response.Error == nil || response.Error.Code != "invalid_or_failed_stream_start" {
			return errors.New("production hash adapter accepted a payload whose bytes do not match its SHA-256 binding")
		}
		return nil
	case "stale-before-value":
		run, err := cloneHashRun(positives[0].Run)
		if err != nil {
			return err
		}
		run.Commits[0].Mutations[0].ExpectedOldSHA256 = strings.Repeat("0", 64)
		start := run
		start.Commits = []rq3baseline.Commit{}
		handler := new(workerHandler)
		_, _ = sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: "cap", Operation: rq3baseline.OperationCapabilities})
		started, err := sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: "start", Operation: rq3baseline.OperationStreamStart, Run: &start})
		if err != nil || !started.OK {
			return errors.New("production hash hostile stream failed to start")
		}
		chunk := rq3baseline.RunSpec{System: run.System, Layout: run.Layout, Commits: []rq3baseline.Commit{run.Commits[0]}}
		response, err := sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: "stale-before", Operation: rq3baseline.OperationStreamChunk, Run: &chunk})
		if err != nil || response.OK || response.Error == nil || response.Error.Code != "invalid_or_failed_stream_chunk" {
			return errors.New("production hash adapter accepted a stale expected-old value")
		}
		return nil
	case "commit-list-digest-mismatch":
		testCase := positives[2]
		handler := new(workerHandler)
		if response, err := sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: "cap", Operation: rq3baseline.OperationCapabilities}); err != nil || !response.OK {
			return errors.New("production hash capability preflight failed")
		}
		start := testCase.Run
		start.Commits = []rq3baseline.Commit{}
		if response, err := sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: "start", Operation: rq3baseline.OperationStreamStart, Run: &start}); err != nil || !response.OK {
			return errors.New("production hash digest-mismatch stream failed to start")
		}
		for index, commit := range testCase.Run.Commits {
			chunk := rq3baseline.RunSpec{System: testCase.Run.System, Layout: testCase.Run.Layout, Commits: []rq3baseline.Commit{commit}}
			if response, err := sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: fmt.Sprintf("chunk-%d", index), Operation: rq3baseline.OperationStreamChunk, Run: &chunk}); err != nil || !response.OK {
				return errors.New("production hash digest-mismatch stream chunk failed")
			}
		}
		response, err := sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: "finish", Operation: rq3baseline.OperationStreamFinish})
		if err != nil || !response.OK || response.Stream == nil || response.Stream.CommitListSHA256 != testCase.TraceBinding.CommitListSHA256 || response.Stream.CommitListSHA256 == strings.Repeat("0", 64) {
			return errors.New("production hash worker did not expose the actual commit-list digest for evaluator mismatch detection")
		}
		return nil
	case "unfinished-stream":
		run := positives[0].Run
		start := run
		start.Commits = []rq3baseline.Commit{}
		input, err := encodeHashSelfTestRequests([]rq3baseline.WorkerRequest{
			{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: "cap", Operation: rq3baseline.OperationCapabilities},
			{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: "start", Operation: rq3baseline.OperationStreamStart, Run: &start},
		})
		if err != nil {
			return err
		}
		if err := runWorker(ctx, bytes.NewReader(input), io.Discard); err == nil || !strings.Contains(err.Error(), "before stream-finish") {
			return fmt.Errorf("production hash worker accepted EOF with an unfinished stream: %v", err)
		}
		return nil
	case "failed-stream-reuse":
		run, err := cloneHashRun(positives[0].Run)
		if err != nil {
			return err
		}
		run.Commits[0].Mutations[0].ExpectedOldSHA256 = strings.Repeat("0", 64)
		start := run
		start.Commits = []rq3baseline.Commit{}
		handler := new(workerHandler)
		_, _ = sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: "cap", Operation: rq3baseline.OperationCapabilities})
		_, _ = sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: "start", Operation: rq3baseline.OperationStreamStart, Run: &start})
		chunk := rq3baseline.RunSpec{System: run.System, Layout: run.Layout, Commits: []rq3baseline.Commit{run.Commits[0]}}
		failed, _ := sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: "fail", Operation: rq3baseline.OperationStreamChunk, Run: &chunk})
		reused, _ := sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: "reuse", Operation: rq3baseline.OperationStreamFinish})
		if failed.OK || failed.Error == nil || reused.OK || reused.Error == nil || !strings.Contains(reused.Error.Message, "poisoned") {
			return errors.New("production hash worker allowed reuse after a failed stream chunk")
		}
		return nil
	case "active-stream-binding-reuse":
		run := positives[0].Run
		start := run
		start.Commits = []rq3baseline.Commit{}
		handler := new(workerHandler)
		_, _ = sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: "cap", Operation: rq3baseline.OperationCapabilities})
		_, _ = sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: "start", Operation: rq3baseline.OperationStreamStart, Run: &start})
		wrongLayout := run.Layout
		wrongLayout.FileLayout = "trickle"
		chunk := rq3baseline.RunSpec{System: run.System, Layout: wrongLayout, Commits: []rq3baseline.Commit{run.Commits[0]}}
		failed, _ := sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: "wrong-binding", Operation: rq3baseline.OperationStreamChunk, Run: &chunk})
		reused, _ := sendHashSelfTestRequest(ctx, handler, rq3baseline.WorkerRequest{SchemaVersion: rq3baseline.WorkerRequestSchema, RequestID: "reuse", Operation: rq3baseline.OperationStreamFinish})
		if failed.OK || failed.Error == nil || reused.OK || reused.Error == nil || !strings.Contains(reused.Error.Message, "poisoned") {
			return errors.New("production hash worker allowed reuse after an active-stream binding failure")
		}
		return nil
	default:
		return fmt.Errorf("unsupported hostile kind %q", testCase.Kind)
	}
}

func encodeHashSelfTestRequests(requests []rq3baseline.WorkerRequest) ([]byte, error) {
	var input bytes.Buffer
	encoder := json.NewEncoder(&input)
	encoder.SetEscapeHTML(false)
	for _, request := range requests {
		if err := encoder.Encode(request); err != nil {
			return nil, err
		}
	}
	return input.Bytes(), nil
}

func validateHashSelfTestInputs(corpusPath string, boundWorkloads, gitExecutables []string) error {
	consumed := []string{corpusPath}
	for _, path := range boundWorkloads {
		if err := validateBoundWorkload(path); err != nil {
			return fmt.Errorf("bound workload %q: %w", path, err)
		}
		consumed = append(consumed, path)
	}
	for _, path := range gitExecutables {
		if err := validateBoundExecutable(path); err != nil {
			return fmt.Errorf("Git executable %q: %w", path, err)
		}
		consumed = append(consumed, path)
	}
	return requireExactInvocationPaths(consumed)
}

func validateBoundWorkload(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) == 0 || len(data) > maxJSONLRecordBytes {
		return errors.New("workload is empty or exceeds the bounded size")
	}
	var identity struct {
		SchemaVersion string `json:"schema_version"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&identity); err != nil {
		return err
	}
	if identity.SchemaVersion != "malt-rq3-controlled-workload/v1" && identity.SchemaVersion != "malt-eval-git-trace-request/v1" {
		return fmt.Errorf("unsupported production workload schema %q", identity.SchemaVersion)
	}
	return nil
}

func validateBoundExecutable(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("path must be absolute and clean")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errors.New("file must be a non-symlink executable")
	}
	return nil
}

func requireExactInvocationPaths(consumed []string) error {
	for index, path := range consumed {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return fmt.Errorf("consumed path %d is not absolute and clean", index)
		}
	}
	want, err := e0selftest.InputPaths()
	if err != nil {
		return err
	}
	slices.Sort(consumed)
	if !slices.Equal(consumed, want) {
		return fmt.Errorf("typed self-test flags consume %v, E0 invocation pins %v", consumed, want)
	}
	return nil
}

func validateHashTraceBinding(run rq3baseline.RunSpec, binding hashTraceBinding) error {
	commitIDs := make([]string, 0, len(run.Commits)+1)
	commitIDs = append(commitIDs, run.Snapshot.CommitID)
	for _, commit := range run.Commits {
		commitIDs = append(commitIDs, commit.CommitID)
	}
	if binding.TraceID == "" || !slices.Equal(commitIDs, binding.CommitIDs) {
		return errors.New("frozen run commit IDs do not equal the registered first-parent trace")
	}
	digest, err := hashCommitListDigest(commitIDs)
	if err != nil {
		return err
	}
	if digest != binding.CommitListSHA256 {
		return errors.New("registered first-parent trace commit-list digest mismatch")
	}
	return nil
}

func hashCommitListDigest(commitIDs []string) (string, error) {
	encoded, err := json.Marshal(struct {
		Commits []string `json:"commits"`
	}{Commits: commitIDs})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func cloneHashRun(run rq3baseline.RunSpec) (rq3baseline.RunSpec, error) {
	encoded, err := json.Marshal(run)
	if err != nil {
		return rq3baseline.RunSpec{}, err
	}
	var clone rq3baseline.RunSpec
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return rq3baseline.RunSpec{}, err
	}
	return clone, nil
}

func decodeHashSelfTestCorpus(path string, target *hashAdapterSelfTestCorpus) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read hash adapter self-test corpus: %w", err)
	}
	if len(data) == 0 || len(data) > maxJSONLRecordBytes {
		return errors.New("hash adapter self-test corpus is empty or exceeds the bounded size")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode strict hash adapter self-test corpus: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("hash adapter self-test corpus contains a trailing JSON value")
		}
		return fmt.Errorf("decode hash adapter self-test corpus trailer: %w", err)
	}
	return nil
}

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
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/dewebprotocol/malt-client/internal/evaluation/machine"
	"github.com/dewebprotocol/malt-client/internal/evaluation/rq2e0"
	"github.com/dewebprotocol/malt-client/internal/evaluation/rq2fixture"
	"github.com/dewebprotocol/malt-client/internal/evaluation/rq2wire"
	cid "github.com/ipfs/go-cid"
)

func TestParseBrowserEngineOutputUsesExactVersion(t *testing.T) {
	for input, expected := range map[string]string{
		"Chromium 150.0.7871.114 snap\n":        "chromium-150.0.7871.114",
		"Google Chrome 149.0.7654.2":            "chrome-149.0.7654.2",
		"Google Chrome for Testing 148.0.1.2\n": "chrome-148.0.1.2",
	} {
		actual, err := parseBrowserEngineOutput(input)
		if err != nil || actual != expected {
			t.Fatalf("parseBrowserEngineOutput(%q) = %q, %v; want %q", input, actual, err, expected)
		}
	}
	for _, invalid := range []string{"Chromium", "Firefox 150.0", "Chromium 150", "Chromium latest"} {
		if _, err := parseBrowserEngineOutput(invalid); err == nil {
			t.Fatalf("invalid version output %q was accepted", invalid)
		}
	}
}

func TestPoisonBrowserMakesSessionTerminal(t *testing.T) {
	worker := &browserWorker{browser: &browserSession{}, state: "active"}
	cause := fmt.Errorf("exchange timeout")
	if err := worker.poisonBrowser(cause); !strings.Contains(err.Error(), cause.Error()) {
		t.Fatalf("poison error = %v", err)
	}
	if worker.browser != nil || worker.state != "poisoned" {
		t.Fatalf("poisoned worker retained browser/state: %#v", worker)
	}
	request := rq2wire.WorkerRequest{RecordKind: rq2wire.RecordMutation}
	if err := worker.bindRequest(request); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("poisoned worker accepted another request: %v", err)
	}
}

func TestPoisonBrowserImmediatelyForceTerminatesProcess(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=TestBrowserForceTerminationHelper")
	command.Env = append(os.Environ(), "MALT_BROWSER_FORCE_TERMINATION_HELPER=1")
	configureBrowserProcess(command)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	completion := &processCompletion{done: make(chan struct{})}
	go func() {
		completion.err = command.Wait()
		close(completion.done)
	}()
	worker := &browserWorker{browser: &browserSession{command: command, process: completion}, state: "active"}
	started := time.Now()
	err := worker.poisonBrowser(errors.New("forced test failure"))
	if elapsed := time.Since(started); elapsed > browserForceTerminationBound+time.Second {
		t.Fatalf("force termination took %s, bound %s", elapsed, browserForceTerminationBound)
	}
	if err == nil || !strings.Contains(err.Error(), "forced test failure") || strings.Contains(err.Error(), "exceeded") {
		t.Fatalf("force termination error = %v", err)
	}
	select {
	case <-completion.done:
	default:
		t.Fatal("force termination returned before the browser process was reaped")
	}
}

func TestBrowserForceTerminationHelper(t *testing.T) {
	if os.Getenv("MALT_BROWSER_FORCE_TERMINATION_HELPER") != "1" {
		return
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestParseFlagsRequiresPinnedBrowserAndWASMArtifacts(t *testing.T) {
	digest := strings.Repeat("a", 64)
	base := []string{
		"-gateway-base-url", "http://127.0.0.1:8080", "-gateway-instance-token", digest,
		"-fixture", "/tmp/fixture", "-worker-id", "worker", "-platform-id", "browser",
		"-client-kind", "browser-wasm", "-backend", "kzg", "-lifecycle", "browser-cold",
		"-steady-warmup-operation", "map-replace",
		"-low-power-arm=false", "-browser-path", "/usr/bin/chromium", "-browser-engine", "chromium-150.0.7871.114",
		"-browser-launcher-sha256", digest, "-browser-launcher-bytes", "1",
		"-wasm", "/tmp/writer.wasm", "-wasm-sha256", digest, "-wasm-bytes", "1",
		"-wasm-exec", "/tmp/wasm_exec.js", "-wasm-exec-sha256", digest, "-wasm-exec-bytes", "1",
		"-machine-descriptor", "/tmp/machine.json", "-machine-descriptor-sha256", digest, "-machine-descriptor-bytes", "1",
	}
	config, err := parseFlags(base, &bytes.Buffer{})
	if err != nil || config.clientKind != rq2wire.ClientBrowserWASM {
		t.Fatalf("parseFlags() = %#v, %v", config, err)
	}
	for index, value := range base {
		if value == "-wasm-sha256" {
			hostile := append([]string(nil), base...)
			hostile[index+1] = "latest"
			if _, err := parseFlags(hostile, &bytes.Buffer{}); err == nil {
				t.Fatal("non-pinned WASM artifact was accepted")
			}
			break
		}
	}
	short := append([]string(nil), base...)
	for index := range short {
		if short[index] == "browser-cold" {
			short[index] = "browser-short-session"
			break
		}
	}
	if _, err := parseFlags(short, &bytes.Buffer{}); err == nil {
		t.Fatal("short-session lifecycle without an exact mutation count was accepted")
	}
	short = append(short, "-session-mutation-count", "2")
	if config, err := parseFlags(short, &bytes.Buffer{}); err != nil || config.sessionMutationCount != 2 {
		t.Fatalf("typed short-session flags = %#v, %v", config, err)
	}
}

func TestMissingBrowserCapabilityFailsClosedAtPreflight(t *testing.T) {
	directory := t.TempDir()
	fixturePath := filepath.Join(directory, "fixture.bin")
	wasmPath := filepath.Join(directory, "writer.wasm")
	wasmExecPath := filepath.Join(directory, "wasm_exec.js")
	for path, value := range map[string][]byte{fixturePath: []byte("fixture"), wasmPath: []byte("wasm"), wasmExecPath: []byte("exec")} {
		if err := os.WriteFile(path, value, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	config := hostConfig{
		gatewayBaseURL: "http://127.0.0.1:1", gatewayToken: strings.Repeat("a", 64), fixturePath: fixturePath,
		workerID: "worker", platformID: "browser", clientKind: rq2wire.ClientBrowserWASM, backend: "kzg",
		lifecycle: rq2wire.LifecycleBrowserCold, requestTimeout: time.Second,
		steadyWarmup: "map-replace",
		browserPath:  "/definitely/missing/chromium", browserEngine: "chromium-150.0.7871.114",
		browserPin: artifactPin{path: "/definitely/missing/chromium", sha256: strings.Repeat("b", 64), bytes: 1, executable: true, allowSymlink: true, maxBytes: maxBrowserLauncherBytes},
		wasmPin:    testArtifactPin(t, wasmPath, maxWASMBytes), wasmExecPin: testArtifactPin(t, wasmExecPath, maxSupportBytes),
		machinePin: testMachinePin(t, false),
	}
	worker, err := newBrowserWorker(config)
	if err != nil {
		t.Fatal(err)
	}
	request := preflightRequest(config)
	record := worker.preflight(context.Background(), request)
	if record.Success || record.FailureClass != "capability_unavailable" || !strings.Contains(record.Error, "browser launcher unavailable") {
		t.Fatalf("missing browser preflight = %#v", record)
	}
	if err := record.Validate(); err != nil {
		t.Fatalf("failed preflight is not protocol-valid: %v", err)
	}
}

func TestDecodeWorkerRequestRejectsUnknownAndTrailingData(t *testing.T) {
	raw := `{"schema_version":"malt-rq2-worker-request/v1","worker_id":"worker","request_id":"preflight","record_kind":"preflight","session_id":"session","client_kind":"browser-wasm","platform_id":"browser","backend":"kzg","lifecycle":"browser-cold","fixture_id":"fixture","measured":false}`
	if _, err := decodeWorkerRequest([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	for _, hostile := range []string{
		strings.TrimSuffix(raw, "}") + `,"extra":true}`,
		strings.Replace(raw, `"worker_id":"worker"`, `"worker_id":"worker","worker_id":"other"`, 1),
		raw + `{}`,
	} {
		if _, err := decodeWorkerRequest([]byte(hostile)); err == nil {
			t.Fatalf("hostile JSON was accepted: %s", hostile)
		}
	}
}

func TestRunMissingBrowserEmitsOneStrictFailedPreflightLine(t *testing.T) {
	directory := t.TempDir()
	fixturePath := filepath.Join(directory, "fixture.bin")
	wasmPath := filepath.Join(directory, "writer.wasm")
	wasmExecPath := filepath.Join(directory, "wasm_exec.js")
	for path, value := range map[string][]byte{fixturePath: []byte("fixture"), wasmPath: []byte("wasm"), wasmExecPath: []byte("exec")} {
		if err := os.WriteFile(path, value, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wasmPin := testArtifactPin(t, wasmPath, maxWASMBytes)
	wasmExecPin := testArtifactPin(t, wasmExecPath, maxSupportBytes)
	machinePin := testMachinePin(t, false)
	token := strings.Repeat("a", 64)
	args := []string{
		"-gateway-base-url", "http://127.0.0.1:1", "-gateway-instance-token", token,
		"-fixture", fixturePath, "-worker-id", "worker", "-platform-id", "browser",
		"-client-kind", "browser-wasm", "-backend", "kzg", "-lifecycle", "browser-cold", "-low-power-arm=false",
		"-steady-warmup-operation", "map-replace", "-browser-path", "/definitely/missing/chromium",
		"-browser-engine", "chromium-150.0.7871.114", "-browser-launcher-sha256", strings.Repeat("b", 64), "-browser-launcher-bytes", "1",
		"-wasm", wasmPath, "-wasm-sha256", wasmPin.sha256, "-wasm-bytes", strconv.FormatInt(wasmPin.bytes, 10),
		"-wasm-exec", wasmExecPath, "-wasm-exec-sha256", wasmExecPin.sha256, "-wasm-exec-bytes", strconv.FormatInt(wasmExecPin.bytes, 10),
		"-machine-descriptor", machinePin.path, "-machine-descriptor-sha256", machinePin.sha256, "-machine-descriptor-bytes", strconv.FormatInt(machinePin.bytes, 10),
	}
	config, err := parseFlags(args, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	requestJSON, _ := json.Marshal(preflightRequest(config))
	var stdout bytes.Buffer
	err = run(args, bytes.NewReader(append(requestJSON, '\n')), &stdout, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "closed before session-end") {
		t.Fatalf("run error = %v", err)
	}
	if bytes.Count(stdout.Bytes(), []byte{'\n'}) != 1 {
		t.Fatalf("stdout is not one strict JSONL record: %q", stdout.String())
	}
	var record rq2wire.WorkerRecord
	if err := strictJSON(bytes.TrimSpace(stdout.Bytes()), &record); err != nil {
		t.Fatal(err)
	}
	if record.Success || record.FailureClass != "capability_unavailable" {
		t.Fatalf("missing-browser stdout = %#v", record)
	}
}

func TestRealChromiumExecutesColdAndSteadyBrowserOperations(t *testing.T) {
	if testing.Short() || runtime.GOOS != "linux" {
		t.Skip("real Chromium/WASM integration requires a non-short Linux test")
	}
	browserPath, err := exec.LookPath("chromium")
	if err != nil {
		browserPath, err = exec.LookPath("google-chrome")
	}
	if err != nil {
		t.Skip("Chromium-family browser is not installed")
	}
	versionCommand := exec.Command(browserPath, "--version")
	versionOutput, err := versionCommand.CombinedOutput()
	if err != nil {
		t.Skipf("browser version probe unavailable: %v", err)
	}
	engine, err := parseBrowserEngineOutput(string(versionOutput))
	if err != nil {
		t.Skipf("unsupported installed browser: %v", err)
	}
	directory := t.TempDir()
	wasmPath := filepath.Join(directory, "writer.wasm")
	moduleRoot := filepath.Clean(filepath.Join("..", "..", "..", ".."))
	build := exec.Command("go", "build", "-p=6", "-buildvcs=false", "-o", wasmPath, "./tools/evaluation/cmd/malt-eval-rq2-browser-wasm")
	build.Dir = moduleRoot
	build.Env = append(os.Environ(), "GOOS=js", "GOARCH=wasm", "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build real Go/WASM writer: %v\n%s", err, output)
	}
	wasmExecPath := filepath.Join(runtime.GOROOT(), "lib", "wasm", "wasm_exec.js")
	if _, err := os.Stat(wasmExecPath); err != nil {
		t.Skipf("matching wasm_exec.js is unavailable: %v", err)
	}
	browserPin := testArtifactPinAllowSymlink(t, browserPath, maxBrowserLauncherBytes, true)
	wasmPin := testArtifactPin(t, wasmPath, maxWASMBytes)
	wasmExecPin := testArtifactPin(t, wasmExecPath, maxSupportBytes)
	testCases := []struct {
		name                 string
		backend              string
		lifecycle            string
		sessionMutationCount int
		operations           []testBrowserOperation
	}{
		{name: "kzg-cold", backend: "kzg", lifecycle: rq2wire.LifecycleBrowserCold, operations: []testBrowserOperation{{name: "document-edit-cid-binding-submit", measured: true}}},
		{name: "kzg-steady", backend: "kzg", lifecycle: rq2wire.LifecycleBrowserSteady, operations: []testBrowserOperation{
			{name: "map-replace", measured: false},
			{name: "document-edit-cid-binding-submit", measured: true}, {name: "list-append", measured: true},
			{name: "list-replace", measured: true}, {name: "map-insert", measured: true}, {name: "map-replace", measured: true},
		}},
		{name: "ipa-cold", backend: "ipa", lifecycle: rq2wire.LifecycleBrowserCold, operations: []testBrowserOperation{{name: "map-replace", measured: true}}},
		{name: "kzg-short-n2", backend: "kzg", lifecycle: rq2wire.LifecycleBrowserShort, sessionMutationCount: 2, operations: []testBrowserOperation{
			{name: "document-edit-cid-binding-submit", measured: true}, {name: "document-edit-cid-binding-submit", measured: true},
		}},
		{name: "kzg-short-baseline-n2", backend: "kzg", lifecycle: rq2wire.LifecycleBrowserSteady, sessionMutationCount: 2, operations: []testBrowserOperation{
			{name: "map-replace", measured: false},
			{name: "document-edit-cid-binding-submit", measured: true}, {name: "document-edit-cid-binding-submit", measured: true},
		}},
	}
	for caseIndex, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			token := strings.Repeat(string(rune('a'+caseIndex)), 64)
			gatewayFixture, initialRoot, fixtureBytes := newTestBrowserGateway(t, token, testCase.backend)
			fixturePath := filepath.Join(t.TempDir(), "fixture.bin")
			if err := os.WriteFile(fixturePath, fixtureBytes, 0o600); err != nil {
				t.Fatal(err)
			}
			gateway := gatewayFixture
			defer gateway.Close()
			config := hostConfig{
				gatewayBaseURL: gateway.URL(), gatewayToken: token, fixturePath: fixturePath,
				workerID: "worker-" + testCase.name, platformID: "browser", clientKind: rq2wire.ClientBrowserWASM, backend: testCase.backend,
				lifecycle: testCase.lifecycle, sessionMutationCount: testCase.sessionMutationCount, requestTimeout: 2 * time.Minute,
				steadyWarmup: "map-replace",
				browserPath:  browserPath, browserEngine: engine, browserPin: browserPin,
				wasmPin: wasmPin, wasmExecPin: wasmExecPin,
				machinePin: testMachinePin(t, false),
			}
			worker, err := newBrowserWorker(config)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			exchange := func(request rq2wire.WorkerRequest) rq2wire.WorkerRecord {
				t.Helper()
				if err := worker.bindRequest(request); err != nil {
					t.Fatal(err)
				}
				record := worker.exchange(ctx, request)
				if err := rq2wire.Bind(record, request); err != nil {
					t.Fatal(err)
				}
				if err := record.Validate(); err != nil {
					t.Fatalf("invalid real-browser record: %v\n%#v", err, record)
				}
				if !record.Success {
					t.Fatalf("real browser request %s failed: %s: %s", request.RequestID, record.FailureClass, record.Error)
				}
				return record
			}
			preflight := exchange(preflightRequest(config))
			if preflight.Runtime == nil || preflight.Runtime.BrowserEngine != engine || preflight.Runtime.WASMSHA256 != wasmPin.sha256 {
				t.Fatalf("real browser runtime provenance = %#v", preflight.Runtime)
			}
			start := requestFor(config, rq2wire.RecordSessionStart, "session-start", "", false, initialRoot.String())
			exchange(start)
			acceptedRoot := initialRoot.String()
			for index, operation := range testCase.operations {
				request := requestFor(config, rq2wire.RecordMutation, fmt.Sprintf("mutation-%03d", index), operation.name, operation.measured, acceptedRoot)
				record := exchange(request)
				acceptedRoot = record.Mutation.ReceiptRoot
				metrics := record.Mutation.Metrics
				if !metrics.CPUTotal.Applicable || !metrics.PeakMemory.Applicable || metrics.PeakMemory.Bytes == 0 ||
					!metrics.JSWASMBoundary.Applicable || metrics.JSWASMBoundary.Bytes == 0 {
					t.Fatalf("real browser resource/boundary metrics are incomplete: %#v", metrics)
				}
				cold := testCase.lifecycle == rq2wire.LifecycleBrowserCold || testCase.lifecycle == rq2wire.LifecycleBrowserShort && index == 0
				if metrics.WASMDownload.Applicable != cold || metrics.WASMInstantiate.Applicable != cold || metrics.ParameterLoad.Applicable != cold || metrics.FirstMutation.Applicable != cold {
					t.Fatalf("cold/steady phase classification is wrong: %#v", metrics)
				}
			}
			exchange(requestFor(config, rq2wire.RecordSessionEnd, "session-end", "", false, acceptedRoot))
			if err := worker.browser.close(); err != nil {
				t.Fatalf("close real browser: %v", err)
			}
		})
	}
}

type testBrowserOperation struct {
	name     string
	measured bool
}

func requestFor(config hostConfig, kind, requestID, operation string, measured bool, root string) rq2wire.WorkerRequest {
	return rq2wire.WorkerRequest{
		SchemaVersion: rq2wire.WorkerRequestSchema, WorkerID: config.workerID, RequestID: requestID,
		RecordKind: kind, SessionID: "session", ClientKind: rq2wire.ClientBrowserWASM, PlatformID: config.platformID,
		Backend: config.backend, Lifecycle: config.lifecycle, FixtureID: "fixture", Operation: operation,
		Measured: measured, ExpectedAcceptedRoot: root,
	}
}

func newTestBrowserGateway(t *testing.T, token, backend string) (*rq2e0.ConformanceGateway, cid.Cid, []byte) {
	t.Helper()
	fixture := testBrowserSourceFixture(t)
	gateway, root, err := rq2e0.NewConformanceGateway(fixture, backend, token)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(fixture)
	if err != nil {
		gateway.Close()
		t.Fatal(err)
	}
	return gateway, root, raw
}

func testBrowserSourceFixture(t *testing.T) *rq2fixture.Fixture {
	t.Helper()
	document := []byte("document")
	chunkA := make([]byte, 32)
	chunkB := make([]byte, 32)
	copy(chunkA, "chunk-a")
	copy(chunkB, "chunk-b")
	index := uint64(0)
	seed := sha256.Sum256([]byte("browser source fixture seed"))
	value := rq2fixture.SourceDefinition{
		SchemaVersion: rq2fixture.SourceSchemaVersion, FixtureID: "fixture", MutationSeedSHA256: hex.EncodeToString(seed[:]),
		DirectFiles: []rq2fixture.SourceDirectFile{{Path: "document.txt", Coordinate: "document.txt", Bytes: document}},
		ListFiles: []rq2fixture.SourceListFile{{
			Path: "list.bin", Coordinate: "list.bin", ChunkSize: 32, TotalSize: 64,
			Chunks: []rq2fixture.SourceListChunk{{Index: 0, Bytes: chunkA}, {Index: 1, Bytes: chunkB}},
		}},
		Operations: []rq2fixture.Operation{
			{Name: "document-edit-cid-binding-submit", Kind: rq2fixture.KindDocumentEdit, SourcePath: "document.txt", SourceCoordinate: "document.txt", PayloadBytes: 32},
			{Name: "map-replace", Kind: rq2fixture.KindDirectReplace, SourcePath: "document.txt", SourceCoordinate: "document.txt", PayloadBytes: 16},
			{Name: "map-insert", Kind: rq2fixture.KindDirectInsert, DestinationPath: "inserted.txt", DestinationCoordinate: "inserted.txt", PayloadBytes: 16},
			{Name: "list-append", Kind: rq2fixture.KindListAppend, SourcePath: "list.bin", SourceCoordinate: "list.bin", PayloadBytes: 32},
			{Name: "list-replace", Kind: rq2fixture.KindListReplace, SourcePath: "list.bin", SourceCoordinate: "list.bin", PayloadBytes: 32, ListIndex: &index},
		},
	}
	fixture, err := rq2e0.BuildFixture(t.Context(), &value)
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func preflightRequest(config hostConfig) rq2wire.WorkerRequest {
	return rq2wire.WorkerRequest{
		SchemaVersion: rq2wire.WorkerRequestSchema, WorkerID: config.workerID, RequestID: "preflight",
		RecordKind: rq2wire.RecordPreflight, SessionID: "session", ClientKind: rq2wire.ClientBrowserWASM,
		PlatformID: config.platformID, Backend: config.backend, Lifecycle: config.lifecycle, FixtureID: "fixture",
	}
}

func testArtifactPin(t *testing.T, path string, maximum int64) artifactPin {
	t.Helper()
	return testArtifactPinAllowSymlink(t, path, maximum, false)
}

func testMachinePin(t *testing.T, lowPower bool) artifactPin {
	t.Helper()
	identity, err := machine.Probe()
	if err != nil {
		t.Fatal(err)
	}
	classification := machine.ClassGeneral
	if lowPower {
		classification = machine.ClassLowPower
	}
	descriptor, err := machine.NewDescriptor("test-machine", classification, "test-suite:registered-platform-evidence", identity)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "machine.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	return artifactPin{path: path, sha256: hex.EncodeToString(digest[:]), bytes: int64(len(raw)), maxBytes: machine.MaxDescriptorBytes}
}

func testArtifactPinAllowSymlink(t *testing.T, path string, maximum int64, executable bool) artifactPin {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return artifactPin{
		path: path, sha256: hex.EncodeToString(digest[:]), bytes: int64(len(data)), executable: executable,
		allowSymlink: executable, maxBytes: maximum,
	}
}

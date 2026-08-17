package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	clientbackup "github.com/dewebprotocol/malt-client/application/backup"
	filesystemmount "github.com/dewebprotocol/malt-client/filesystem/mount"
	truststore "github.com/dewebprotocol/malt-client/trust"
)

type planRunnerCall struct {
	operation string
	request   clientbackup.PlanRequest
}

type recordingPlanRunner struct {
	result *clientbackup.BatchResult
	err    error
	calls  []planRunnerCall
}

type recordingMountController struct {
	statuses     []filesystemmount.Status
	mountErr     error
	unmountErr   error
	mountCalls   []filesystemmount.Spec
	unmountCalls []string
}

func (c *recordingMountController) List() ([]filesystemmount.Status, error) {
	return append([]filesystemmount.Status(nil), c.statuses...), nil
}

func (c *recordingMountController) Mount(_ context.Context, spec filesystemmount.Spec) (filesystemmount.Status, error) {
	c.mountCalls = append(c.mountCalls, spec)
	status := filesystemmount.Status{Spec: spec, Desired: true, Active: c.mountErr == nil, Adapter: "test"}
	return status, c.mountErr
}

func (c *recordingMountController) Unmount(_ context.Context, id string) error {
	c.unmountCalls = append(c.unmountCalls, id)
	return c.unmountErr
}

func (r *recordingPlanRunner) BackupPlans(_ context.Context, request clientbackup.PlanRequest) (*clientbackup.BatchResult, error) {
	r.calls = append(r.calls, planRunnerCall{operation: "backup", request: request})
	return r.result, r.err
}

func (r *recordingPlanRunner) SyncPlans(_ context.Context, request clientbackup.PlanRequest) (*clientbackup.BatchResult, error) {
	r.calls = append(r.calls, planRunnerCall{operation: "sync", request: request})
	return r.result, r.err
}

func TestLifecycleIdentityIsAuthenticatedAndNotExposedByHealth(t *testing.T) {
	store, err := truststore.Open(filepath.Join(t.TempDir(), "roots.json"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewWithInstance(store, "managed-instance")
	if err != nil {
		t.Fatal(err)
	}

	healthReq := httptest.NewRequest(http.MethodGet, "/health", nil)
	healthRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(healthRec, healthReq)
	var health map[string]any
	if err := json.Unmarshal(healthRec.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	if _, ok := health["instance"]; ok {
		t.Fatalf("health exposed lifecycle instance: %#v", health)
	}

	for _, test := range []struct {
		name   string
		token  string
		status int
	}{
		{name: "missing", status: http.StatusUnauthorized},
		{name: "wrong", token: "other-instance", status: http.StatusUnauthorized},
		{name: "matching", token: "managed-instance", status: http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/_lifecycle/identity", nil)
			if test.token != "" {
				req.Header.Set(lifecycleInstanceHeader, test.token)
			}
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, req)
			if rec.Code != test.status {
				t.Fatalf("status = %d, want %d", rec.Code, test.status)
			}
		})
	}
}

func TestLocalAPIKeepsCandidateSeparate(t *testing.T) {
	store, err := truststore.Open(filepath.Join(t.TempDir(), "roots.json"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	root := "bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"
	req := httptest.NewRequest(http.MethodPut, "/v1/roots/docs", strings.NewReader(`{"root":"`+root+`","profile":"unixfs"}`))
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("trust status=%d body=%s", rec.Code, rec.Body.String())
	}
	record, err := store.Get("docs")
	if err != nil || record.AcceptedRoot != root {
		t.Fatalf("record=%#v err=%v", record, err)
	}
}

func TestLocalAPIAcceptsOnlyRecordedObservationThroughObservationRoute(t *testing.T) {
	store, err := truststore.Open(filepath.Join(t.TempDir(), "roots.json"))
	if err != nil {
		t.Fatal(err)
	}
	root := "bafkreihdwdcefgh4dqkjv67uzcmw7ojee6xedzdetojuzjevtenxquvyku"
	if _, err := store.ObserveHead("docs", truststore.ObservedHead{
		Source: "https://gateway.example", DatasetID: "bucket-one", Branch: "main",
		CommitID: "commit-one", Root: root, Revision: 1,
	}); err != nil {
		t.Fatal(err)
	}
	server, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	candidateRequest := httptest.NewRequest(http.MethodPost, "/v1/roots/docs/candidates/"+root+"/accept", nil)
	candidateResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(candidateResponse, candidateRequest)
	if candidateResponse.Code != http.StatusNotFound {
		t.Fatalf("candidate acceptance of observation status=%d body=%s", candidateResponse.Code, candidateResponse.Body.String())
	}
	request := httptest.NewRequest(
		http.MethodPost, "/v1/roots/docs/observations/"+root+"/accept",
		strings.NewReader(`{"profile":"unixfs","gateway":"https://gateway.example","source":"explicit-local-test"}`),
	)
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("observation acceptance status=%d body=%s", response.Code, response.Body.String())
	}
	record, err := store.Get("docs")
	state, stateErr := store.GetState("docs")
	if err != nil || stateErr != nil || record.AcceptedRoot != root || len(record.Candidates) != 0 || len(state.ObservedHeads) != 1 {
		t.Fatalf("accepted observation record=%#v state=%#v err=%v stateErr=%v", record, state, err, stateErr)
	}
	stateRequest := httptest.NewRequest(http.MethodGet, "/v1/trust-states/docs", nil)
	stateResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(stateResponse, stateRequest)
	if stateResponse.Code != http.StatusOK || !strings.Contains(stateResponse.Body.String(), `"observed_heads"`) ||
		!strings.Contains(stateResponse.Body.String(), `"accepted"`) {
		t.Fatalf("structured trust-state status=%d body=%s", stateResponse.Code, stateResponse.Body.String())
	}
}

func TestPlanControlPlaneMatchesDirectApplicationRunner(t *testing.T) {
	for _, test := range []struct {
		name      string
		operation string
		route     string
		status    int
		runErr    error
	}{
		{name: "backup", operation: "backup", route: "/v1/plan-backups", status: http.StatusOK},
		{name: "sync conflict", operation: "sync", route: "/v1/sync", status: http.StatusConflict, runErr: clientbackup.ErrBackupConflict},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, err := truststore.Open(filepath.Join(t.TempDir(), "roots.json"))
			if err != nil {
				t.Fatal(err)
			}
			result := &clientbackup.BatchResult{
				Operation:   test.operation,
				Runs:        []clientbackup.PlanRun{{PlanID: "plan-one", PlanName: "documents", BucketID: "bucket-one", Branch: "main"}},
				CompletedAt: time.Date(2026, time.August, 17, 1, 2, 3, 0, time.UTC),
			}
			if test.runErr != nil {
				result.Failures = []clientbackup.PlanFailure{{PlanID: "plan-one", Error: test.runErr.Error(), Conflict: true}}
			}
			runner := &recordingPlanRunner{result: result, err: test.runErr}
			server, err := NewWithOptions(store, Options{Plans: runner})
			if err != nil {
				t.Fatal(err)
			}
			request := clientbackup.PlanRequest{Plans: []string{"plan-one"}, Message: "adapter parity", MergeConflicts: true}
			var direct *clientbackup.BatchResult
			if test.operation == "sync" {
				direct, err = runner.SyncPlans(context.Background(), request)
			} else {
				direct, err = runner.BackupPlans(context.Background(), request)
			}
			if !errors.Is(err, test.runErr) {
				t.Fatalf("direct error = %v, want %v", err, test.runErr)
			}
			body, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			httpRequest := httptest.NewRequest(http.MethodPost, test.route, strings.NewReader(string(body)))
			recorder := httptest.NewRecorder()
			server.Handler().ServeHTTP(recorder, httpRequest)
			if recorder.Code != test.status {
				t.Fatalf("HTTP status = %d body=%s, want %d", recorder.Code, recorder.Body.String(), test.status)
			}
			if len(runner.calls) != 2 || runner.calls[0].operation != test.operation || runner.calls[1].operation != test.operation {
				t.Fatalf("runner calls = %#v", runner.calls)
			}
			for _, call := range runner.calls {
				if strings.Join(call.request.Plans, ",") != "plan-one" || call.request.Message != request.Message || !call.request.MergeConflicts {
					t.Fatalf("adapter changed request: %#v", call.request)
				}
			}
			if test.runErr == nil {
				var decoded clientbackup.BatchResult
				if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
					t.Fatal(err)
				}
				if decoded.Operation != direct.Operation || len(decoded.Runs) != len(direct.Runs) || !decoded.CompletedAt.Equal(direct.CompletedAt) {
					t.Fatalf("HTTP result = %#v, direct = %#v", decoded, direct)
				}
				return
			}
			var failure struct {
				Error  string                    `json:"error"`
				Result *clientbackup.BatchResult `json:"result"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &failure); err != nil {
				t.Fatal(err)
			}
			if failure.Error != test.runErr.Error() || failure.Result == nil || failure.Result.Operation != direct.Operation {
				t.Fatalf("HTTP failure = %#v, direct result = %#v err=%v", failure, direct, test.runErr)
			}
		})
	}
}

func TestMountControlPlaneUsesOneLifecycleServiceAndStrictJSON(t *testing.T) {
	store, err := truststore.Open(filepath.Join(t.TempDir(), "roots.json"))
	if err != nil {
		t.Fatal(err)
	}
	spec := filesystemmount.Spec{
		ID: "docs", DatasetID: "bucket-one", Branch: "main",
		Mountpoint: filepath.Join(t.TempDir(), "mnt"), TrustAlias: "docs",
		CachePolicy: filesystemmount.CacheVerified, WritePolicy: filesystemmount.WriteReadOnly,
		EncryptionEpoch: 2, ConflictPolicy: filesystemmount.ConflictFailReadOnly,
	}
	controller := &recordingMountController{}
	server, err := NewWithOptions(store, Options{Mounts: controller})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/v1/mounts", strings.NewReader(string(body)))
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusCreated || len(controller.mountCalls) != 1 || controller.mountCalls[0] != spec {
		t.Fatalf("mount response=%d body=%s calls=%#v", response.Code, response.Body.String(), controller.mountCalls)
	}
	controller.statuses = []filesystemmount.Status{{Spec: spec, Desired: true, Active: true, Adapter: "test"}}
	request = httptest.NewRequest(http.MethodGet, "/v1/mounts", nil)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"active":true`) {
		t.Fatalf("mount list response=%d body=%s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodDelete, "/v1/mounts/docs", nil)
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNoContent || len(controller.unmountCalls) != 1 || controller.unmountCalls[0] != "docs" {
		t.Fatalf("unmount response=%d body=%s calls=%#v", response.Code, response.Body.String(), controller.unmountCalls)
	}

	invalid := append([]byte(nil), body...)
	invalid = bytes.Replace(invalid, []byte("bucket-one"), []byte{0xff}, 1)
	request = httptest.NewRequest(http.MethodPost, "/v1/mounts", bytes.NewReader(invalid))
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || len(controller.mountCalls) != 1 {
		t.Fatalf("lossy JSON response=%d body=%s calls=%d", response.Code, response.Body.String(), len(controller.mountCalls))
	}
	reserved := spec
	reserved.ID = ".."
	reservedBody, err := json.Marshal(reserved)
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "/v1/mounts", bytes.NewReader(reservedBody))
	response = httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || len(controller.mountCalls) != 1 {
		t.Fatalf("reserved-ID response=%d body=%s calls=%d", response.Code, response.Body.String(), len(controller.mountCalls))
	}

	for _, test := range []struct {
		name   string
		err    error
		status int
	}{
		{name: "invalid specification", err: filesystemmount.ErrInvalidSpec, status: http.StatusBadRequest},
		{name: "identity conflict", err: filesystemmount.ErrIdentityReuse, status: http.StatusConflict},
		{name: "mountpoint conflict", err: filesystemmount.ErrMountpointUse, status: http.StatusConflict},
		{name: "pending unmount", err: filesystemmount.ErrPendingUnmount, status: http.StatusConflict},
		{name: "platform failure", err: errors.New("platform unavailable"), status: http.StatusBadGateway},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller.mountErr = fmt.Errorf("mount failed: %w", test.err)
			request := httptest.NewRequest(http.MethodPost, "/v1/mounts", strings.NewReader(string(body)))
			response := httptest.NewRecorder()
			server.Handler().ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d body=%s, want %d", response.Code, response.Body.String(), test.status)
			}
		})
	}
}

func TestListenCreatesPrivateUnixSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows named-pipe DACL is configured by the platform listener")
	}
	store, err := truststore.Open(filepath.Join(t.TempDir(), "roots.json"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	socket := shortSocketPath(t)
	listener, err := server.Listen(socket)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	info, err := os.Stat(socket)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %#o, want 0600", info.Mode().Perm())
	}
}

func TestListenRefusesToReplaceRegularFile(t *testing.T) {
	store, err := truststore.Open(filepath.Join(t.TempDir(), "roots.json"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	socket := shortSocketPath(t)
	if err := os.WriteFile(socket, []byte("do not replace"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := server.Listen(socket); err == nil {
		t.Fatal("Listen replaced a non-socket path")
	}
}

func TestListenRefusesToReplaceLiveUnixSocket(t *testing.T) {
	store, err := truststore.Open(filepath.Join(t.TempDir(), "roots.json"))
	if err != nil {
		t.Fatal(err)
	}
	server, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	socket := shortSocketPath(t)
	live, err := net.Listen("unix", socket)
	if err != nil {
		t.Skipf("unix sockets are unavailable: %v", err)
	}
	defer live.Close()
	if _, err := server.Listen(socket); err == nil || !strings.Contains(err.Error(), "live socket") {
		t.Fatalf("Listen error = %v, want live-socket refusal", err)
	}
}

func shortSocketPath(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "malt-client-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "client.sock")
}

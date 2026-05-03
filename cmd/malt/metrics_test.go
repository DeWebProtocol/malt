package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt/httpapi"
)

func TestMetricsCommandHasSnapshotAndResetSubcommands(t *testing.T) {
	for _, name := range []string{"snapshot", "reset"} {
		if cmd, _, err := metricsCmd.Find([]string{name}); err != nil || cmd == nil || cmd.Name() != name {
			t.Fatalf("metrics subcommand %q lookup = cmd %v err %v", name, cmd, err)
		}
	}
}

func TestMetricsSnapshotPrintsDaemonJSON(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)
	defaultClient = daemon
	t.Cleanup(func() { defaultClient = nil })

	root := newTestRoot(ctx, t, daemon, casClient)
	writeResp, err := daemon.AddUnixFSFile(ctx, root, "file.txt", []byte("hello metrics"))
	if err != nil {
		t.Fatalf("add unixfs file: %v", err)
	}
	if _, err := daemon.ContentProof(ctx, writeResp.NewRoot, "file.txt", ""); err != nil {
		t.Fatalf("content proof: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runMetricsSnapshot(testCommandWithContext(ctx), nil); err != nil {
			t.Fatalf("run metrics snapshot: %v", err)
		}
	})

	if !strings.Contains(out, "\n  \"snapshot\"") {
		t.Fatalf("metrics snapshot output is not indented JSON:\n%s", out)
	}
	var payload httpapi.MetricsResponse
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decode metrics snapshot output: %v\n%s", err, out)
	}
	if payload.Snapshot.Proof.ProofListCount == 0 {
		t.Fatalf("proof metrics were not printed from daemon response: %+v", payload.Snapshot)
	}
}

func TestMetricsResetPrintsDaemonJSON(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)
	defaultClient = daemon
	t.Cleanup(func() { defaultClient = nil })

	root := newTestRoot(ctx, t, daemon, casClient)
	writeResp, err := daemon.AddUnixFSFile(ctx, root, "file.txt", []byte("hello metrics reset"))
	if err != nil {
		t.Fatalf("add unixfs file: %v", err)
	}
	if _, err := daemon.ContentProof(ctx, writeResp.NewRoot, "file.txt", ""); err != nil {
		t.Fatalf("content proof: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runMetricsReset(testCommandWithContext(ctx), nil); err != nil {
			t.Fatalf("run metrics reset: %v", err)
		}
	})

	if !strings.Contains(out, "\n  \"snapshot\"") {
		t.Fatalf("metrics reset output is not indented JSON:\n%s", out)
	}
	var payload httpapi.MetricsResponse
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decode metrics reset output: %v\n%s", err, out)
	}
	if payload.Snapshot.Proof.ProofListCount != 0 || payload.Snapshot.CAS.GetCount != 0 || payload.Snapshot.ArcTable.GetCount != 0 {
		t.Fatalf("metrics reset output = %+v, want zero counters", payload.Snapshot)
	}
}

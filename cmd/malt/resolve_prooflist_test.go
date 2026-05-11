package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestResolvePrintsProofListByDefault(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)
	defaultClient = daemon
	t.Cleanup(func() { defaultClient = nil })

	targetCID, err := casClient.Put(ctx, []byte("resolve-target"))
	if err != nil {
		t.Fatalf("put target content: %v", err)
	}
	target := targetCID.String()
	createResp, err := daemon.CreateRootStructure(ctx, map[string]string{"@payload": target, "name": target})
	if err != nil {
		t.Fatalf("create root structure: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runResolve(testCommandWithContext(ctx), []string{createResp.Root, "name"}); err != nil {
			t.Fatalf("run resolve: %v", err)
		}
	})

	if !strings.Contains(out, "\n  \"target\"") || !strings.Contains(out, "\n  \"prooflist\"") {
		t.Fatalf("resolve output is not indented ProofList JSON:\n%s", out)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decode resolve output: %v\n%s", err, out)
	}
	if payload["target"] != target {
		t.Fatalf("target = %v, want %s", payload["target"], target)
	}
	if _, ok := payload["prooflist"].(map[string]any); !ok {
		t.Fatalf("prooflist field missing or wrong type: %#v", payload["prooflist"])
	}
	if _, ok := payload["transcript"]; ok {
		t.Fatalf("resolve output should not expose transcript: %#v", payload["transcript"])
	}
}

func TestResolveCanOmitProofList(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)
	defaultClient = daemon
	t.Cleanup(func() { defaultClient = nil })

	targetCID, err := casClient.Put(ctx, []byte("resolve-target"))
	if err != nil {
		t.Fatalf("put target content: %v", err)
	}
	target := targetCID.String()
	createResp, err := daemon.CreateRootStructure(ctx, map[string]string{"@payload": target, "name": target})
	if err != nil {
		t.Fatalf("create root structure: %v", err)
	}

	cmd := testCommandWithContext(ctx)
	cmd.Flags().Bool("proof", false, "")
	out := captureStdout(t, func() {
		if err := runResolve(cmd, []string{createResp.Root, "name"}); err != nil {
			t.Fatalf("run resolve: %v", err)
		}
	})

	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decode resolve output: %v\n%s", err, out)
	}
	if payload["target"] != target {
		t.Fatalf("target = %v, want %s", payload["target"], target)
	}
	if _, ok := payload["prooflist"]; ok {
		t.Fatalf("prooflist should be omitted when --proof=false: %#v", payload["prooflist"])
	}
}

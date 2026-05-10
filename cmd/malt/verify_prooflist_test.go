package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyAcceptsResolveProofListJSON(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)
	defaultClient = daemon
	t.Cleanup(func() { defaultClient = nil })

	targetCID, err := casClient.Put(ctx, []byte("verify-target"))
	if err != nil {
		t.Fatalf("put target content: %v", err)
	}
	target := targetCID.String()
	createResp, err := daemon.CreateRootStructure(ctx, map[string]string{"@payload": target, "name": target})
	if err != nil {
		t.Fatalf("create root structure: %v", err)
	}

	resolveResp, err := daemon.ResolveRoot(ctx, createResp.Root, "name")
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}
	if resolveResp.ProofList == nil {
		t.Fatal("resolve response missing ProofList")
	}

	proofPath := filepath.Join(t.TempDir(), "resolve-proof.json")
	data := captureStdout(t, func() {
		printJSON(resolveResp)
	})
	if err := os.WriteFile(proofPath, []byte(data), 0o644); err != nil {
		t.Fatalf("write proof file: %v", err)
	}

	cmd := testCommandWithContext(ctx)
	cmd.Flags().String("prooflist", proofPath, "")
	out := captureStdout(t, func() {
		if err := runVerify(cmd, nil); err != nil {
			t.Fatalf("run verify: %v", err)
		}
	})
	if !strings.Contains(out, "valid: true") {
		t.Fatalf("verify output = %q, want valid true", out)
	}
}

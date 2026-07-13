package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	malt "github.com/dewebprotocol/malt"
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
	// Verification must remain available after the execution client is removed.
	defaultClient = nil

	cmd := testCommandWithContext(ctx)
	cmd.Flags().String("prooflist", proofPath, "")
	cmd.Flags().String("root", createResp.Root, "")
	cmd.Flags().String("query", "name", "")
	out := captureStdout(t, func() {
		if err := runVerify(cmd, nil); err != nil {
			t.Fatalf("run verify: %v", err)
		}
	})
	if !strings.Contains(out, "valid: true") {
		t.Fatalf("verify output = %q, want valid true", out)
	}

	tamperedRootCmd := testCommandWithContext(ctx)
	tamperedRootCmd.Flags().String("prooflist", proofPath, "")
	tamperedRootCmd.Flags().String("root", fakeAddCID("different-trusted-root").String(), "")
	tamperedRootCmd.Flags().String("query", "name", "")
	if err := runVerify(tamperedRootCmd, nil); err == nil {
		t.Fatal("local verify accepted a ProofList under a different trusted root")
	}
}

func TestVerifyRejectedResultReturnsFailure(t *testing.T) {
	if err := reportLocalVerification(false); !errors.Is(err, malt.ErrVerifierRejected) {
		t.Fatalf("reportLocalVerification(false) error = %v, want ErrVerifierRejected", err)
	}
}

func TestVerifyRejectsResolveJSONWithMismatchedTarget(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)

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
	resolveResp.Target = fakeAddCID("tampered-target").String()

	proofPath := filepath.Join(t.TempDir(), "tampered-resolve-proof.json")
	data, err := json.Marshal(resolveResp)
	if err != nil {
		t.Fatalf("marshal tampered resolve response: %v", err)
	}
	if err := os.WriteFile(proofPath, data, 0o644); err != nil {
		t.Fatalf("write proof file: %v", err)
	}

	cmd := testCommandWithContext(ctx)
	cmd.Flags().String("prooflist", proofPath, "")
	if _, err := readProofListInput(cmd); err == nil {
		t.Fatal("expected mismatched resolve target to be rejected")
	}
}

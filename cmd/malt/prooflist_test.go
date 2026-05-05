package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestProofListArgumentShape(t *testing.T) {
	if err := proofListCmd.Args(proofListCmd, []string{"bafyroot"}); err == nil {
		t.Fatal("expected prooflist to require root and path")
	}

	if err := proofListCmd.Args(proofListCmd, []string{"root", "path"}); err != nil {
		t.Fatalf("expected prooflist to accept root and path: %v", err)
	}
}

func TestProofListPrintsIndentedJSON(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)
	defaultClient = daemon
	t.Cleanup(func() { defaultClient = nil })

	targetData := []byte("prooflist-target")
	targetCID, err := casClient.Put(ctx, targetData)
	if err != nil {
		t.Fatalf("put target content: %v", err)
	}
	target := targetCID.String()
	createResp, err := daemon.CreateRootStructure(ctx, map[string]string{"@payload": target, "name": target})
	if err != nil {
		t.Fatalf("create root structure: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runProofList(testCommandWithContext(ctx), []string{createResp.Root, "name"}); err != nil {
			t.Fatalf("run prooflist: %v", err)
		}
	})

	if !strings.Contains(out, "\n  \"target\"") || !strings.Contains(out, "\n  \"prooflist\"") {
		t.Fatalf("prooflist output is not indented JSON:\n%s", out)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decode prooflist output: %v\n%s", err, out)
	}
	if payload["target"] != target {
		t.Fatalf("target = %v, want %s", payload["target"], target)
	}
	if _, ok := payload["prooflist"].(map[string]any); !ok {
		t.Fatalf("prooflist field missing or wrong type: %#v", payload["prooflist"])
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	os.Stdout = w
	defer func() {
		os.Stdout = old
		_ = r.Close()
	}()

	fn()

	if err := w.Close(); err != nil {
		t.Fatalf("close stdout pipe: %v", err)
	}

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatalf("read stdout pipe: %v", err)
	}
	return buf.String()
}

func testCommandWithContext(ctx context.Context) *cobra.Command {
	cmd := &cobra.Command{}
	cmd.SetContext(ctx)
	return cmd
}

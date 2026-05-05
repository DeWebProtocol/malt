package main

import (
	"context"
	"strings"
	"testing"
)

func TestProvePrintsTranscriptSteps(t *testing.T) {
	ctx := context.Background()
	daemon, casClient := newAddTestClients(t)
	defaultClient = daemon
	t.Cleanup(func() { defaultClient = nil })

	targetData := []byte("prove-target")
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
		if err := runProve(testCommandWithContext(ctx), []string{createResp.Root, "name"}); err != nil {
			t.Fatalf("run prove: %v", err)
		}
	})

	if !strings.Contains(out, "[0]") || !strings.Contains(out, "evidence:") {
		t.Fatalf("prove output missing transcript step:\n%s", out)
	}
	if !strings.Contains(out, "target: "+target) {
		t.Fatalf("prove output target mismatch:\n%s", out)
	}
}

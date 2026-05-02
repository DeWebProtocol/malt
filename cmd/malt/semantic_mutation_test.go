package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSemanticMutationValidatesRequiredFlags(t *testing.T) {
	semanticMutationRoot = ""
	semanticMutationFile = ""
	if err := runSemanticMutation(semanticMutationCmd, nil); err == nil || !strings.Contains(err.Error(), "--root is required") {
		t.Fatalf("missing root error = %v", err)
	}

	semanticMutationRoot = fakeAddCID("semantic-base").String()
	semanticMutationFile = ""
	t.Cleanup(func() {
		semanticMutationRoot = ""
		semanticMutationFile = ""
	})
	if err := runSemanticMutation(semanticMutationCmd, nil); err == nil || !strings.Contains(err.Error(), "--file is required") {
		t.Fatalf("missing file error = %v", err)
	}
}

func TestSemanticMutationRejectsMalformedJSON(t *testing.T) {
	badFile := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(badFile, []byte(`{"puts":`), 0o644); err != nil {
		t.Fatalf("write bad json: %v", err)
	}

	semanticMutationRoot = fakeAddCID("semantic-base").String()
	semanticMutationFile = badFile
	t.Cleanup(func() {
		semanticMutationRoot = ""
		semanticMutationFile = ""
	})

	if err := runSemanticMutation(semanticMutationCmd, nil); err == nil || !strings.Contains(err.Error(), "decode semantic mutation request") {
		t.Fatalf("malformed json error = %v", err)
	}
}

func TestSemanticMutationPrintsIndentedJSON(t *testing.T) {
	ctx := context.Background()
	daemon, _ := newAddTestClients(t)
	defaultClient = daemon
	t.Cleanup(func() { defaultClient = nil })

	initialPayload := fakeAddCID("semantic-initial").String()
	initial, err := daemon.CreateRootStructure(ctx, map[string]string{"@payload": initialPayload, "name": initialPayload})
	if err != nil {
		t.Fatalf("create initial structure: %v", err)
	}
	target := fakeAddCID("semantic-target").String()
	reqFile := filepath.Join(t.TempDir(), "request.json")
	req := `{"puts":[{"object":"` + initial.Root + `","kind":"map","entries":[{"path":"@payload","target":"` + target + `"},{"path":"name","target":"` + target + `"}]}]}`
	if err := os.WriteFile(reqFile, []byte(req), 0o644); err != nil {
		t.Fatalf("write request: %v", err)
	}

	semanticMutationRoot = initial.Root
	semanticMutationFile = reqFile
	t.Cleanup(func() {
		semanticMutationRoot = ""
		semanticMutationFile = ""
	})
	out := captureStdout(t, func() {
		if err := runSemanticMutation(testCommandWithContext(ctx), nil); err != nil {
			t.Fatalf("run semantic mutation: %v", err)
		}
	})

	if strings.Contains(out, "\n  \"bucket\"") || !strings.Contains(out, "\n  \"new_root\"") {
		t.Fatalf("semantic mutation output is not indented JSON:\n%s", out)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("decode semantic mutation output: %v\n%s", err, out)
	}
	if payload["base_root"] != initial.Root {
		t.Fatalf("base_root = %v, want %s", payload["base_root"], initial.Root)
	}
	if payload["new_root"] == "" {
		t.Fatal("new_root should be present")
	}
}

package evalschema

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandListsSchemasAsJSON(t *testing.T) {
	var out bytes.Buffer
	cmd := NewCommandWithSchemas(&out, []Schema{
		{Name: "run-plan", Path: "cmd/eval/schemas/run-plan.schema.json"},
		{Name: "common-record", Path: "cmd/eval/schemas/common-record.schema.json"},
	})
	cmd.SetArgs(nil)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	var listed []Schema
	if err := json.Unmarshal(out.Bytes(), &listed); err != nil {
		t.Fatalf("unmarshal schema list: %v", err)
	}
	if len(listed) != 2 || listed[0].Name != "common-record" || listed[1].Name != "run-plan" {
		t.Fatalf("listed schemas = %#v", listed)
	}
}

func TestCommandPrintsNamedSchema(t *testing.T) {
	tmp := t.TempDir()
	schemaPath := filepath.Join(tmp, "schema.json")
	if err := os.WriteFile(schemaPath, []byte(`{"title":"test"}`), 0o644); err != nil {
		t.Fatalf("write schema: %v", err)
	}
	var out bytes.Buffer
	cmd := NewCommandWithSchemas(&out, []Schema{{Name: "test", Path: schemaPath}})
	cmd.SetArgs([]string{"--name", "test"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != `{"title":"test"}` {
		t.Fatalf("schema output = %q", got)
	}
}

func TestCommandRejectsUnknownSchema(t *testing.T) {
	cmd := NewCommandWithSchemas(&bytes.Buffer{}, []Schema{{Name: "known", Path: "known.json"}})
	cmd.SetArgs([]string{"--name", "missing"})

	if err := cmd.Execute(); err == nil {
		t.Fatal("Execute should reject unknown schema")
	}
}

func TestDefaultCommandPrintsNamedSchemaOutsideRepoRoot(t *testing.T) {
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	cmd := NewCommandWithOutput(&out)
	cmd.SetArgs([]string{"--name", "run-plan"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "MALT evaluation run plan") {
		t.Fatalf("schema output did not contain run-plan schema: %s", got)
	}
}

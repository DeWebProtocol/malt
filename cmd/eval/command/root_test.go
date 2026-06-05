package command

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

func TestRootCommandExposesEvaluationSubcommands(t *testing.T) {
	cmd := NewRootCommand()
	if cmd.Use != "malt-eval" {
		t.Fatalf("root command Use = %q, want malt-eval", cmd.Use)
	}

	// Root command accepts --plan flag directly.
	if planFlag := cmd.Flags().Lookup("plan"); planFlag == nil {
		t.Fatal("root command should expose --plan flag")
	}

	for _, name := range []string{"schema", "summarize", "read", "write", "metrics"} {
		if found, _, err := cmd.Find([]string{name}); err != nil || found == nil || found.Name() != name {
			t.Fatalf("subcommand %q not found: found=%v err=%v", name, found, err)
		}
	}
}

func TestRootCommandRejectsPositionalArgs(t *testing.T) {
	cmd := NewRootCommand()
	if cmd.Args == nil {
		t.Fatal("root command should reject positional arguments")
	}
	if err := cmd.Args(cmd, []string{"unexpected"}); err == nil {
		t.Fatal("root command should reject positional arguments")
	}
}

func TestRootCommandRequiresPlanFlag(t *testing.T) {
	cmd := NewRootCommand()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs(nil)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute should fail without --plan")
	}
	if !strings.Contains(err.Error(), "required flag") && !strings.Contains(stderr.String(), "required flag") {
		t.Fatalf("Execute error = %v, stderr = %q; want required flag error", err, stderr.String())
	}
}

func TestReadCommandExposesArcFlagAndResultSchema(t *testing.T) {
	cmd := NewRootCommand()
	readCmd, _, err := cmd.Find([]string{"read"})
	if err != nil {
		t.Fatalf("find read command: %v", err)
	}
	if readCmd == nil || readCmd.Name() != "read" {
		t.Fatalf("read command not found: %v", readCmd)
	}

	arcFlag := readCmd.Flags().Lookup("arc")
	if arcFlag == nil {
		t.Fatal("read command should expose repeatable --arc fixture seed flag")
	}
	if arcFlag.Value.Type() != "stringArray" {
		t.Fatalf("--arc flag type = %q, want stringArray", arcFlag.Value.Type())
	}
	if err := readCmd.ParseFlags([]string{"--arc", "@payload=bafkqaaa", "--arc", "dummy=bafkqaab"}); err != nil {
		t.Fatalf("parse read --arc flags: %v", err)
	}
	gotArcs, err := readCmd.Flags().GetStringArray("arc")
	if err != nil {
		t.Fatalf("read parsed --arc flags: %v", err)
	}
	if want := []string{"@payload=bafkqaaa", "dummy=bafkqaab"}; !reflect.DeepEqual(gotArcs, want) {
		t.Fatalf("parsed --arc flags = %#v, want %#v", gotArcs, want)
	}

	if got := readCmd.Annotations["malt.result_schema"]; got != "cmd/eval/schemas/readbench-result.schema.json" {
		t.Fatalf("read result schema annotation = %q", got)
	}
}

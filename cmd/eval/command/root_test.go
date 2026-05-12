package command

import "testing"

func TestRootCommandExposesEvaluationSubcommands(t *testing.T) {
	cmd := NewRootCommand()
	if cmd.Use != "malt-eval" {
		t.Fatalf("root command Use = %q, want malt-eval", cmd.Use)
	}

	for _, name := range []string{"read", "write", "metrics"} {
		if found, _, err := cmd.Find([]string{name}); err != nil || found == nil || found.Name() != name {
			t.Fatalf("subcommand %q not found: found=%v err=%v", name, found, err)
		}
	}
}

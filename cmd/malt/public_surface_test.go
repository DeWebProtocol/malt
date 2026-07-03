package main

import (
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRootCommandOnlyExposesProductCommands(t *testing.T) {
	want := []string{"add", "init", "resolve", "restart", "start", "status", "stop", "verify"}

	var got []string
	for _, cmd := range rootCmd.Commands() {
		if cmd.Hidden {
			continue
		}
		got = append(got, cmd.Name())
	}
	slices.Sort(got)

	if !slices.Equal(got, want) {
		t.Fatalf("public commands = %v, want %v", got, want)
	}
}

func TestAddCommandRootWordingIsRootRelative(t *testing.T) {
	texts := map[string]string{
		"root help":       rootCmd.Long,
		"add short":       addCmd.Short,
		"add prefix flag": addCmd.Flag("prefix").Usage,
		"add root flag":   addCmd.Flag("root").Usage,
	}
	for name, text := range texts {
		if strings.Contains(text, "current root") {
			t.Fatalf("%s contains current-root wording: %q", name, text)
		}
	}
	if !strings.Contains(addCmd.Short, "base root") || !strings.Contains(addCmd.Short, "result root") {
		t.Fatalf("add short = %q, want base root and result root wording", addCmd.Short)
	}
}

func TestDaemonLifecycleSubcommandsSuppressUsageAndErrorsForRuntimeErrors(t *testing.T) {
	for _, cmd := range []*cobra.Command{daemonStartCmd, daemonStatusCmd, daemonStopCmd, daemonRestartCmd} {
		if !cmd.SilenceUsage {
			t.Fatalf("%s SilenceUsage = false, want true", cmd.Name())
		}
		if !cmd.SilenceErrors {
			t.Fatalf("%s SilenceErrors = false, want true", cmd.Name())
		}
	}
}

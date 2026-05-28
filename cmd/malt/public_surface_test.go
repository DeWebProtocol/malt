package main

import (
	"slices"
	"testing"

	"github.com/spf13/cobra"
)

func TestRootCommandOnlyExposesProductCommands(t *testing.T) {
	want := []string{"add", "daemon", "init", "resolve", "verify"}

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

func TestDaemonCommandExposesLifecycleSubcommands(t *testing.T) {
	want := []string{"restart", "start", "status", "stop"}

	var got []string
	for _, cmd := range daemonCmd.Commands() {
		if cmd.Hidden {
			continue
		}
		got = append(got, cmd.Name())
	}
	slices.Sort(got)

	if !slices.Equal(got, want) {
		t.Fatalf("daemon subcommands = %v, want %v", got, want)
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

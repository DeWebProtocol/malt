package main

import (
	"slices"
	"testing"
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

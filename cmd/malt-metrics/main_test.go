package main

import "testing"

func TestMetricsRootHasSnapshotAndReset(t *testing.T) {
	for _, name := range []string{"snapshot", "reset"} {
		cmd, _, err := rootCmd.Find([]string{name})
		if err != nil {
			t.Fatalf("find %s: %v", name, err)
		}
		if cmd == nil || cmd.Name() != name {
			t.Fatalf("command %s = %v", name, cmd)
		}
	}
}

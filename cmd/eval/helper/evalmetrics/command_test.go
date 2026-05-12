package evalmetrics

import "testing"

func TestMetricsCommandHasSnapshotAndReset(t *testing.T) {
	rootCmd := NewCommand()
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

package main

import (
	"strings"
	"testing"
	"time"
)

func TestDaemonProcessEnvIncludesRuntimeOverrides(t *testing.T) {
	env := daemonProcessEnv([]string{
		"KEEP=1",
		daemonProcessKey + "=0",
		daemonListenKey + "=old",
	}, "settings.json", DaemonOverrides{
		Listen:        "127.0.0.1:4999",
		NoLatency:     true,
		GetLatency:    50 * time.Millisecond,
		PutLatency:    60 * time.Millisecond,
		HasLatency:    70 * time.Millisecond,
		Jitter:        5 * time.Millisecond,
		ShutdownToken: "secret",
	})

	want := map[string]bool{
		"KEEP=1":                            true,
		daemonProcessKey + "=1":             true,
		daemonConfigKey + "=settings.json":  true,
		daemonListenKey + "=127.0.0.1:4999": true,
		daemonNoLatencyKey + "=1":           true,
		daemonGetLatencyKey + "=50ms":       true,
		daemonPutLatencyKey + "=60ms":       true,
		daemonHasLatencyKey + "=70ms":       true,
		daemonJitterKey + "=5ms":            true,
		daemonShutdownTokenKey + "=secret":  true,
	}
	for _, entry := range env {
		delete(want, entry)
		if strings.HasPrefix(entry, daemonListenKey+"=old") {
			t.Fatalf("old listen override was not removed: %q", entry)
		}
	}
	if len(want) > 0 {
		t.Fatalf("missing env entries: %#v\nactual: %#v", want, env)
	}
}

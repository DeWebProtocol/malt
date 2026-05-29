package main

import (
	"slices"
	"testing"
)

func TestDaemonLifecycleProcessEnvSelectsInternalComponent(t *testing.T) {
	env := daemonProcessEnv([]string{
		managedDaemonProcessEnv + "=old",
		managedDaemonConfigEnv + "=old-config",
		managedDaemonListenEnv + "=old-listen",
		"PATH=/bin",
	}, "/tmp/malt.json", "127.0.0.1:4321")

	if !slices.Contains(env, managedDaemonProcessEnv+"=1") {
		t.Fatalf("env missing internal daemon process selector: %v", env)
	}
	if !slices.Contains(env, managedDaemonConfigEnv+"=/tmp/malt.json") {
		t.Fatalf("env missing daemon config path: %v", env)
	}
	if !slices.Contains(env, managedDaemonListenEnv+"=127.0.0.1:4321") {
		t.Fatalf("env missing daemon listen override: %v", env)
	}
	if !slices.Contains(env, "PATH=/bin") {
		t.Fatalf("env removed unrelated entry: %v", env)
	}
	if slices.Contains(env, managedDaemonProcessEnv+"=old") ||
		slices.Contains(env, managedDaemonConfigEnv+"=old-config") ||
		slices.Contains(env, managedDaemonListenEnv+"=old-listen") {
		t.Fatalf("env retained stale daemon process entries: %v", env)
	}
}

func TestDaemonLifecycleProcessArgsAreEmpty(t *testing.T) {
	if args := daemonProcessArgs(); len(args) != 0 {
		t.Fatalf("daemon process args = %v, want none", args)
	}
}

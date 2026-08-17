package main

import (
	"fmt"
	"path/filepath"
	"testing"

	clientbackup "github.com/dewebprotocol/malt-client/application/backup"
	clientconfig "github.com/dewebprotocol/malt-client/internal/config"
	truststore "github.com/dewebprotocol/malt-client/trust"
)

func TestPrepareSyncRetryAcceptsEveryObservedPlanRootInOneRound(t *testing.T) {
	root := t.TempDir()
	cfg, err := clientconfig.Default()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Gateway.BaseURL = "https://gateway.example"
	cfg.Gateway.CredentialPath = filepath.Join(root, "device.json")
	cfg.Daemon.SocketPath = filepath.Join(root, "daemon.sock")
	cfg.Daemon.StatePath = filepath.Join(root, "roots.json")
	cfg.Workspace.StatePath = filepath.Join(root, "workspace.json")
	cfg.Backup.KeyringPath = filepath.Join(root, "keys.json")
	cfg.Backup.HistoryDir = filepath.Join(root, "history")
	cfg.Backup.PlansPath = filepath.Join(root, "plans.json")
	cfg.Backup.TempDir = filepath.Join(root, "staging")
	configPath := filepath.Join(root, "config.json")
	if err := clientconfig.Write(configPath, cfg); err != nil {
		t.Fatal(err)
	}
	previousConfig := cfgFile
	cfgFile = configPath
	defer func() { cfgFile = previousConfig }()

	observedRoot := mustParseCID(t, "bafkqaaa").String()
	store, _, err := openTrustStore()
	if err != nil {
		t.Fatal(err)
	}
	result := &clientbackup.BatchResult{Operation: "sync"}
	for index := 0; index < 5; index++ {
		alias := fmt.Sprintf("backup:bucket-%d:main", index)
		if _, err := store.ObserveHead(alias, truststore.ObservedHead{
			Source: cfg.GatewayBaseURL(), DatasetID: fmt.Sprintf("bucket-%d", index), Branch: "main",
			CommitID: fmt.Sprintf("commit-%d", index), Root: observedRoot, Revision: 1,
		}); err != nil {
			t.Fatal(err)
		}
		result.Failures = append(result.Failures, clientbackup.PlanFailure{
			PlanName: fmt.Sprintf("plan-%d", index), TrustAlias: alias,
			ObservedRoot: observedRoot,
		})
	}
	request := clientbackup.PlanRequest{}
	confirmations := 0
	retry, err := prepareSyncRetryWithConfirm(rootCmd, &request, result, func(string) (bool, error) {
		confirmations++
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !retry || confirmations != len(result.Failures) {
		t.Fatalf("retry=%v confirmations=%d, want true/%d", retry, confirmations, len(result.Failures))
	}
	records, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != len(result.Failures) {
		t.Fatalf("trusted roots=%d, want %d", len(records), len(result.Failures))
	}
}

func TestPrepareSyncRetryEnablesAllLocalMergesWithOneConfirmation(t *testing.T) {
	request := clientbackup.PlanRequest{}
	result := &clientbackup.BatchResult{
		Operation: "sync",
		Failures: []clientbackup.PlanFailure{
			{PlanName: "one", MergeAvailable: true, ConflictBranch: "conflicts/one"},
			{PlanName: "two", MergeAvailable: true, ConflictBranch: "conflicts/two"},
		},
	}
	confirmations := 0
	retry, err := prepareSyncRetryWithConfirm(rootCmd, &request, result, func(string) (bool, error) {
		confirmations++
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !retry || !request.MergeConflicts || confirmations != 1 {
		t.Fatalf("retry=%v merge=%v confirmations=%d, want true/true/1", retry, request.MergeConflicts, confirmations)
	}
}

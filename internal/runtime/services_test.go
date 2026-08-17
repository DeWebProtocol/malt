package runtime

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	clientbackup "github.com/dewebprotocol/malt-client/application/backup"
	clientconfig "github.com/dewebprotocol/malt-client/internal/config"
)

func TestServicesOwnConfigurationAndPlanCompositionPaths(t *testing.T) {
	root := t.TempDir()
	cfg, err := clientconfig.Default()
	if err != nil {
		t.Fatal(err)
	}
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
	services, err := NewServices(configPath)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := services.Config()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Backup.PlansPath != cfg.Backup.PlansPath || services.ConfigPath() != configPath {
		t.Fatalf("loaded config = %#v path=%q", loaded.Backup, services.ConfigPath())
	}
	store, err := services.PlanStore(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if plans, err := store.List(); err != nil || len(plans) != 0 {
		t.Fatalf("initial plans = %#v err=%v", plans, err)
	}
	wantProtected := []string{
		configPath,
		cfg.Gateway.CredentialPath,
		cfg.Backup.KeyringPath,
		cfg.Backup.PlansPath,
		cfg.Backup.HistoryDir,
		cfg.Workspace.StatePath,
		cfg.Daemon.StatePath,
		cfg.Daemon.SocketPath,
		cfg.Daemon.SocketPath + ".pid",
	}
	if got := services.ProtectedPaths(loaded); !reflect.DeepEqual(got, wantProtected) {
		t.Fatalf("protected paths = %v, want %v", got, wantProtected)
	}
	if got := PlanHistoryPath(loaded, "plan-one"); got != filepath.Join(cfg.Backup.HistoryDir, "plan-one.json") {
		t.Fatalf("history path = %q", got)
	}

	_, err = services.BackupPlans(context.Background(), clientbackup.PlanRequest{})
	if err == nil || !strings.Contains(err.Error(), "no backup plans are configured") {
		t.Fatalf("empty backup error = %v", err)
	}
}

func TestNilServicesFailClosed(t *testing.T) {
	var services *Services
	if _, err := services.Config(); err == nil {
		t.Fatal("nil services loaded configuration")
	}
	if _, err := services.BackupPlans(context.Background(), clientbackup.PlanRequest{}); err == nil {
		t.Fatal("nil services ran backup plans")
	}
	if _, err := services.PlanStore(&clientconfig.Config{}); err == nil {
		t.Fatal("nil services opened a plan store")
	}
	if _, err := services.PlanService(&clientconfig.Config{}, clientbackup.Plan{}); err == nil {
		t.Fatal("nil services constructed a plan service")
	}
	if got := services.ProtectedPaths(nil); got != nil {
		t.Fatalf("nil protected paths = %v", got)
	}
}

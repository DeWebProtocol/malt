package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/dewebprotocol/malt-client/application"
	clientbackup "github.com/dewebprotocol/malt-client/application/backup"
	clientconfig "github.com/dewebprotocol/malt-client/internal/config"
	localruntime "github.com/dewebprotocol/malt-client/internal/runtime"
	client "github.com/dewebprotocol/malt-client/transport"
)

func loadRuntimeConfig() (*clientconfig.Config, error) {
	return clientconfig.Load(cfgFile)
}

func configuredRuntimeServices() (*localruntime.Services, error) {
	path, err := runtimeConfigPath()
	if err != nil {
		return nil, err
	}
	return localruntime.NewServices(path)
}

func configuredPlanStore() (*clientbackup.PlanStore, error) {
	services, err := configuredRuntimeServices()
	if err != nil {
		return nil, err
	}
	return services.PlanStore(nil)
}

func buildPlanService(cfg *clientconfig.Config, plan clientbackup.Plan) (*clientbackup.PlanService, error) {
	services, err := configuredRuntimeServices()
	if err != nil {
		return nil, err
	}
	return services.PlanService(cfg, plan)
}

func configuredProtectedPaths(cfg *clientconfig.Config, configPath string) []string {
	return localruntime.ProtectedPaths(configPath, cfg)
}

func planHistoryPath(cfg *clientconfig.Config, planID string) string {
	return localruntime.PlanHistoryPath(cfg, planID)
}

func gatewayClient() (*client.Client, error) {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return nil, err
	}
	opts, err := localruntime.GatewayOptions(cfg, cfg.Gateway.Bucket, "")
	if err != nil {
		return nil, err
	}
	return client.New(opts)
}

func daemonCommandError(err error) error {
	var apiErr *client.Error
	if errors.As(err, &apiErr) {
		return fmt.Errorf("gateway request failed: %w", err)
	}
	return err
}

// rootsForSelector keeps explicit CIDs independent from the optional alias
// store. Only a non-CID selector can trigger trust-store I/O.
func rootsForSelector(raw string) (*application.Roots, error) {
	explicit := application.NewExplicitRootSelector()
	if _, err := explicit.Select(raw); err == nil {
		return explicit, nil
	}
	store, _, err := openTrustStore()
	if err != nil {
		return nil, err
	}
	return application.NewRoots(store)
}

func printJSON(value any) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(value)
}

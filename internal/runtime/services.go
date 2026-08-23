// Package runtime composes the MALT local runtime's reusable application
// services. Process adapters such as Cobra commands and the daemon control
// plane use this package instead of rebuilding transport, trust, keyring, and
// UnixFS dependencies themselves.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dewebprotocol/malt-client/application"
	clientadd "github.com/dewebprotocol/malt-client/application/add"
	clientbackup "github.com/dewebprotocol/malt-client/application/backup"
	"github.com/dewebprotocol/malt-client/bucketsync"
	clientconfig "github.com/dewebprotocol/malt-client/internal/config"
	"github.com/dewebprotocol/malt-client/internal/deviceauth"
	"github.com/dewebprotocol/malt-client/internal/keyring"
	gatewayclient "github.com/dewebprotocol/malt-client/transport"
	truststore "github.com/dewebprotocol/malt-client/trust"
	"github.com/dewebprotocol/malt-client/unixfs"
)

// Services is the process-independent composition root for local runtime
// application services. It retains only the configuration path; each operation
// loads one coherent configuration snapshot so a long-running daemon observes
// safely written configuration updates.
type Services struct {
	configPath string
	plans      *clientbackup.BatchRunner
}

func NewServices(configPath string) (*Services, error) {
	if strings.TrimSpace(configPath) == "" {
		var err error
		configPath, err = clientconfig.DefaultPath()
		if err != nil {
			return nil, err
		}
	}
	services := &Services{configPath: filepath.Clean(configPath)}
	runner, err := clientbackup.NewBatchRunner(clientbackup.BatchRunnerOptions{
		OpenEnvironment: services.openPlanEnvironment,
	})
	if err != nil {
		return nil, err
	}
	services.plans = runner
	return services, nil
}

func (s *Services) Config() (*clientconfig.Config, error) {
	if s == nil {
		return nil, fmt.Errorf("runtime services are nil")
	}
	return clientconfig.Load(s.configPath)
}

func (s *Services) ConfigPath() string {
	if s == nil {
		return ""
	}
	return s.configPath
}

func (s *Services) BackupPlans(ctx context.Context, request clientbackup.PlanRequest) (*clientbackup.BatchResult, error) {
	if s == nil || s.plans == nil {
		return nil, fmt.Errorf("runtime plan service is not configured")
	}
	return s.plans.BackupPlans(ctx, request)
}

func (s *Services) SyncPlans(ctx context.Context, request clientbackup.PlanRequest) (*clientbackup.BatchResult, error) {
	if s == nil || s.plans == nil {
		return nil, fmt.Errorf("runtime plan service is not configured")
	}
	return s.plans.SyncPlans(ctx, request)
}

func (s *Services) PlanStore(cfg *clientconfig.Config) (*clientbackup.PlanStore, error) {
	if s == nil {
		return nil, fmt.Errorf("runtime services are nil")
	}
	if cfg == nil {
		var err error
		cfg, err = s.Config()
		if err != nil {
			return nil, err
		}
	}
	return clientbackup.OpenPlanStore(cfg.Backup.PlansPath)
}

func (s *Services) PlanService(cfg *clientconfig.Config, plan clientbackup.Plan) (service *clientbackup.PlanService, resultErr error) {
	if s == nil {
		return nil, fmt.Errorf("runtime services are nil")
	}
	if cfg == nil {
		var err error
		cfg, err = s.Config()
		if err != nil {
			return nil, err
		}
	}
	keys, err := keyring.Open(cfg.Backup.KeyringPath)
	if err != nil {
		return nil, err
	}
	remoteOptions, err := requiredGatewayOptions(cfg, plan.BucketID, plan.Branch)
	if err != nil {
		return nil, err
	}
	remote, err := gatewayclient.New(remoteOptions)
	if err != nil {
		return nil, err
	}
	blocks, err := ComposeCAS(cfg, remote, true)
	if err != nil {
		return nil, err
	}
	transferredBlocks := false
	defer func() {
		if !transferredBlocks {
			resultErr = errors.Join(resultErr, blocks.Close())
		}
	}()
	lists, err := unixfs.NewMutationAdapter(remote)
	if err != nil {
		return nil, err
	}
	graph, err := clientadd.NewMaterializer(remote, lists)
	if err != nil {
		return nil, err
	}
	syncer, err := bucketsync.OpenRemoteBranch(cfg.Workspace.StatePath, remote, plan.BucketID, plan.Branch)
	if err != nil {
		return nil, err
	}
	statePath := PlanHistoryPath(cfg, plan.ID)
	history, err := clientbackup.NewHistory(statePath)
	if err != nil {
		return nil, err
	}
	trustStore, err := truststore.Open(cfg.Daemon.StatePath)
	if err != nil {
		return nil, fmt.Errorf("open trust store: %w", err)
	}
	roots, err := application.NewRoots(trustStore)
	if err != nil {
		return nil, err
	}
	operationLock := statePath + ".operation.lock"
	planStore, err := s.PlanStore(cfg)
	if err != nil {
		return nil, err
	}
	allPlans, err := planStore.List()
	if err != nil {
		return nil, err
	}
	var restoreProtected []string
	for _, existingPlan := range allPlans {
		for _, binding := range existingPlan.Bindings {
			restoreProtected = append(restoreProtected, binding.Source)
		}
	}
	protected := append(
		s.ProtectedPaths(cfg),
		statePath, statePath+".lock", operationLock,
		operationLock+".sync-transaction.json", operationLock+".restore-transaction.json",
		operationLock+".conflicts",
	)
	service, resultErr = clientbackup.NewPlanServiceWithRelease(clientbackup.PlanServiceOptions{
		Plan: plan, TempDir: cfg.Backup.TempDir,
		LockPath: operationLock, Keys: keys, Sync: syncer,
		Materializer: clientbackup.NewAddPlanMaterializer(graph, blocks),
		History:      history, Remote: remote, Blocks: blocks, Roots: roots,
		Protected: protected, RestoreProtected: restoreProtected,
	}, blocks.Close)
	if resultErr == nil {
		transferredBlocks = true
	}
	return service, resultErr
}

func (s *Services) ProtectedPaths(cfg *clientconfig.Config) []string {
	if s == nil || cfg == nil {
		return nil
	}
	return ProtectedPaths(s.configPath, cfg)
}

func ProtectedPaths(configPath string, cfg *clientconfig.Config) []string {
	if cfg == nil {
		return nil
	}
	return []string{
		filepath.Clean(configPath),
		cfg.Gateway.CredentialPath,
		cfg.Backup.KeyringPath,
		cfg.Backup.PlansPath,
		cfg.Backup.HistoryDir,
		cfg.Workspace.StatePath,
		cfg.Daemon.StatePath,
		cfg.Daemon.SocketPath,
		cfg.Daemon.SocketPath + ".pid",
		cfg.Filesystem.MountsPath,
		cfg.Filesystem.MountsPath + ".lock",
		cfg.Filesystem.MountsPath + ".manager.lock",
		cfg.Filesystem.CacheDir,
		cfg.Filesystem.WritableStateDir,
		cfg.Transport.LocalCASDir,
	}
}

func PlanHistoryPath(cfg *clientconfig.Config, planID string) string {
	if cfg == nil {
		return ""
	}
	return filepath.Join(cfg.Backup.HistoryDir, planID+".json")
}

type planEnvironment struct {
	services *Services
	config   *clientconfig.Config
	store    *clientbackup.PlanStore
}

func (s *Services) openPlanEnvironment() (clientbackup.BatchEnvironment, error) {
	cfg, err := s.Config()
	if err != nil {
		return nil, err
	}
	store, err := s.PlanStore(cfg)
	if err != nil {
		return nil, err
	}
	return &planEnvironment{services: s, config: cfg, store: store}, nil
}

func (e *planEnvironment) List() ([]clientbackup.Plan, error) {
	return e.store.List()
}

func (e *planEnvironment) Get(selector string) (clientbackup.Plan, error) {
	return e.store.Get(selector)
}

func (e *planEnvironment) PlanService(plan clientbackup.Plan) (clientbackup.PlanOperations, error) {
	return e.services.PlanService(e.config, plan)
}

func requiredGatewayOptions(cfg *clientconfig.Config, bucketID, branch string) (gatewayclient.Options, error) {
	if cfg == nil {
		return gatewayclient.Options{}, fmt.Errorf("runtime config is nil")
	}
	opts := gatewayclient.Options{
		BaseURL: cfg.GatewayBaseURL(), BucketID: strings.TrimSpace(bucketID),
		BucketBranch: strings.TrimSpace(branch),
	}
	if token := strings.TrimSpace(cfg.Gateway.APIKey); token != "" {
		opts.TenantBearerToken = token
		return opts, nil
	}
	provider := deviceauth.FileProvider{Path: cfg.Gateway.CredentialPath}
	value, err := provider.Load()
	if errors.Is(err, deviceauth.ErrNotFound) {
		return gatewayclient.Options{}, fmt.Errorf("Gateway account is not authenticated; run `malt login`")
	}
	if err != nil {
		return gatewayclient.Options{}, err
	}
	if strings.TrimRight(value.Gateway, "/") != strings.TrimRight(cfg.GatewayBaseURL(), "/") {
		return gatewayclient.Options{}, fmt.Errorf("stored device credential belongs to %s; run `malt login` for %s", value.Gateway, cfg.GatewayBaseURL())
	}
	opts.DeviceAuthorizer = provider
	return opts, nil
}

var _ clientbackup.PlanRunner = (*Services)(nil)
var _ clientbackup.BatchEnvironment = (*planEnvironment)(nil)

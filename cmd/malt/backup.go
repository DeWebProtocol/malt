package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	clientadd "github.com/dewebprotocol/malt-client/application/add"
	clientbackup "github.com/dewebprotocol/malt-client/application/backup"
	"github.com/dewebprotocol/malt-client/bucketsync"
	clientconfig "github.com/dewebprotocol/malt-client/internal/config"
	"github.com/dewebprotocol/malt-client/internal/keyring"
	gatewayclient "github.com/dewebprotocol/malt-client/transport"
	"github.com/dewebprotocol/malt-client/unixfs"
	"github.com/spf13/cobra"
)

var (
	backupForeground bool
	backupMessage    string
	restoreOverwrite bool
)

var backupCmd = &cobra.Command{
	Use:   "backup <local-path>",
	Short: "Create and push an encrypted backup snapshot",
	Long: `Create a compressed XChaCha20-encrypted snapshot and publish it as an
unaccepted Bucket candidate. Remote integrity is established by the selected
MALT root, ProofLists, and CIDs; the archive format does not use an AEAD tag.`,
	Args: cobra.ExactArgs(1),
	RunE: runBackup,
}

var restoreCmd = &cobra.Command{
	Use:   "restore <trusted-root|alias> <destination>",
	Short: "Restore an encrypted backup from an explicitly selected trusted root",
	Args:  cobra.ExactArgs(2),
	RunE:  runRestore,
}

var backupScheduleCmd = &cobra.Command{
	Use:   "schedule",
	Short: "Configure daemon automatic-backup jobs",
}

var backupKeyInitCmd = &cobra.Command{
	Use:   "key-init",
	Short: "Create the client-owned backup keyring without replacing configuration",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		cfg, err := loadRuntimeConfig()
		if err != nil {
			return err
		}
		keys, err := keyring.Create(cfg.Backup.KeyringPath)
		if err != nil {
			return err
		}
		printJSON(map[string]any{"keyring_path": cfg.Backup.KeyringPath, "active_epoch": keys.ActiveEpoch()})
		return nil
	},
}

var backupKeyRotateCmd = &cobra.Command{
	Use:   "key-rotate",
	Short: "Create a new active backup key epoch while retaining old restore keys",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		cfg, err := loadRuntimeConfig()
		if err != nil {
			return err
		}
		keys, err := keyring.Open(cfg.Backup.KeyringPath)
		if err != nil {
			return err
		}
		epoch, err := keys.Rotate()
		if err != nil {
			return err
		}
		printJSON(map[string]any{"keyring_path": cfg.Backup.KeyringPath, "active_epoch": epoch})
		return nil
	},
}

var (
	scheduleEvery   string
	scheduleMessage string
	scheduleEnabled bool
)

var backupScheduleSetCmd = &cobra.Command{
	Use:   "set <name> <local-path>",
	Short: "Create or update an automatic-backup job",
	Args:  cobra.ExactArgs(2),
	RunE: func(_ *cobra.Command, args []string) error {
		if _, err := time.ParseDuration(scheduleEvery); err != nil {
			return fmt.Errorf("invalid backup interval: %w", err)
		}
		cfg, path, err := loadConfigForUpdate()
		if err != nil {
			return err
		}
		source, err := filepath.Abs(args[1])
		if err != nil {
			return err
		}
		if err := clientbackup.ValidateSource(source, configuredProtectedPaths(cfg, path)); err != nil {
			return err
		}
		job := clientconfig.BackupJobConfig{
			Name: strings.TrimSpace(args[0]), Source: source, Every: scheduleEvery,
			Enabled: scheduleEnabled, Message: strings.TrimSpace(scheduleMessage),
		}
		replaced := false
		for i := range cfg.Backup.Jobs {
			if cfg.Backup.Jobs[i].Name == job.Name {
				cfg.Backup.Jobs[i] = job
				replaced = true
				break
			}
		}
		if !replaced {
			cfg.Backup.Jobs = append(cfg.Backup.Jobs, job)
		}
		sort.Slice(cfg.Backup.Jobs, func(i, j int) bool { return cfg.Backup.Jobs[i].Name < cfg.Backup.Jobs[j].Name })
		if err := clientconfig.Write(path, cfg); err != nil {
			return err
		}
		printJSON(job)
		return nil
	},
}

var backupScheduleListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured jobs and their latest daemon results",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		cfg, err := loadRuntimeConfig()
		if err != nil {
			return err
		}
		history, err := clientbackup.NewHistory(cfg.Backup.StatePath)
		if err != nil {
			return err
		}
		states, err := history.Snapshot()
		if err != nil {
			return err
		}
		pending, err := history.Pending()
		if err != nil {
			return err
		}
		scheduler, err := history.SchedulerStatus()
		if err != nil {
			return err
		}
		printJSON(map[string]any{
			"jobs": cfg.Backup.Jobs, "states": states,
			"pending_backup": pending, "scheduler": scheduler,
		})
		return nil
	},
}

var backupScheduleRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove an automatic-backup job",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		cfg, path, err := loadConfigForUpdate()
		if err != nil {
			return err
		}
		name := strings.TrimSpace(args[0])
		next := cfg.Backup.Jobs[:0]
		found := false
		for _, job := range cfg.Backup.Jobs {
			if job.Name == name {
				found = true
				continue
			}
			next = append(next, job)
		}
		if !found {
			return fmt.Errorf("backup job %q was not found", name)
		}
		cfg.Backup.Jobs = next
		if err := clientconfig.Write(path, cfg); err != nil {
			return err
		}
		fmt.Printf("Removed backup job %s\n", name)
		return nil
	},
}

func init() {
	backupCmd.Flags().BoolVar(&backupForeground, "foreground", false, "Run without the daemon (advanced bypass)")
	backupCmd.Flags().StringVarP(&backupMessage, "message", "m", "", "Bucket commit message")
	restoreCmd.Flags().BoolVar(&restoreOverwrite, "overwrite", false, "Replace existing restored files")
	backupScheduleSetCmd.Flags().StringVar(&scheduleEvery, "every", "24h", "Backup interval (for example 30m or 24h)")
	backupScheduleSetCmd.Flags().StringVarP(&scheduleMessage, "message", "m", "", "Bucket commit message")
	backupScheduleSetCmd.Flags().BoolVar(&scheduleEnabled, "enabled", true, "Enable the job")
	backupScheduleCmd.AddCommand(backupScheduleSetCmd, backupScheduleListCmd, backupScheduleRemoveCmd)
	backupCmd.AddCommand(backupScheduleCmd, backupKeyInitCmd, backupKeyRotateCmd)
	rootCmd.AddCommand(backupCmd, restoreCmd)
}

func runBackup(cmd *cobra.Command, args []string) error {
	source, err := filepath.Abs(args[0])
	if err != nil {
		return fmt.Errorf("resolve backup source: %w", err)
	}
	request := clientbackup.Request{Source: source, Message: backupMessage}
	if backupForeground {
		runner := &configuredBackupRunner{}
		result, err := runner.Run(cmd.Context(), request)
		if result != nil {
			printJSON(result)
		}
		return err
	}
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return err
	}
	client, transport := daemonHTTPClient(cfg.Daemon.SocketPath)
	defer transport.CloseIdleConnections()
	client.Timeout = 0
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	httpRequest, err := http.NewRequestWithContext(cmd.Context(), http.MethodPost, "http://malt.local/v1/backups", bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	response, err := client.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("contact backup daemon: %w; start it with `malt daemon start` or use --foreground", err)
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		var failure struct {
			Error  string               `json:"error"`
			Result *clientbackup.Result `json:"result"`
		}
		if json.Unmarshal(data, &failure) == nil && failure.Error != "" {
			if failure.Result != nil {
				printJSON(failure.Result)
			}
			return errors.New(failure.Error)
		}
		return fmt.Errorf("backup daemon returned %s", response.Status)
	}
	var result clientbackup.Result
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("decode backup daemon response: %w", err)
	}
	printJSON(result)
	return nil
}

func runRestore(cmd *cobra.Command, args []string) error {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return err
	}
	if strings.TrimSpace(cfg.Gateway.Bucket) == "" {
		return fmt.Errorf("gateway.bucket is required for encrypted restore")
	}
	remote, err := gatewayClient()
	if err != nil {
		return err
	}
	roots, err := rootsForSelector(args[0])
	if err != nil {
		return err
	}
	selected, err := roots.Select(args[0])
	if err != nil {
		return err
	}
	keys, err := keyring.Open(cfg.Backup.KeyringPath)
	if err != nil {
		return err
	}
	if err := clientbackup.Restore(cmd.Context(), clientbackup.RestoreOptions{
		Remote: remote, Blocks: remote, TrustedRoot: selected.Root, Destination: args[1],
		TempDir: cfg.Backup.TempDir, BucketID: cfg.Gateway.Bucket,
		Keys: keys, Overwrite: restoreOverwrite,
	}); err != nil {
		return daemonCommandError(err)
	}
	fmt.Printf("Restored encrypted backup %s to %s\n", selected.Root, args[1])
	return nil
}

type configuredBackupRunner struct {
	mu sync.Mutex
}

func (r *configuredBackupRunner) Run(ctx context.Context, request clientbackup.Request) (*clientbackup.Result, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	service, err := buildBackupService()
	if err != nil {
		return nil, err
	}
	return service.Run(ctx, request)
}

func buildBackupService() (*clientbackup.Service, error) {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.Gateway.Bucket) == "" {
		return nil, fmt.Errorf("gateway.bucket is required for encrypted backup")
	}
	keys, err := keyring.Open(cfg.Backup.KeyringPath)
	if err != nil {
		return nil, err
	}
	remote, err := gatewayclient.New(gatewayclient.Options{
		BaseURL: cfg.GatewayBaseURL(), TenantBearerToken: cfg.Gateway.APIKey, BucketID: cfg.Gateway.Bucket,
	})
	if err != nil {
		return nil, err
	}
	lists, err := unixfs.NewGatewayMutationAdapter(remote)
	if err != nil {
		return nil, err
	}
	addGateway, err := clientadd.NewGateway(remote, lists)
	if err != nil {
		return nil, err
	}
	syncer, err := bucketsync.Open(cfg.Workspace.StatePath, remote, cfg.Gateway.Bucket)
	if err != nil {
		return nil, err
	}
	history, err := clientbackup.NewHistory(cfg.Backup.StatePath)
	if err != nil {
		return nil, err
	}
	configPath, err := runtimeConfigPath()
	if err != nil {
		return nil, err
	}
	return clientbackup.NewService(clientbackup.Options{
		BucketID: cfg.Gateway.Bucket, TempDir: cfg.Backup.TempDir,
		LockPath: cfg.Backup.StatePath + ".operation.lock", Keys: keys, Sync: syncer,
		Materializer: clientbackup.AddMaterializer{Gateway: addGateway, CAS: remote}, History: history,
		Protected: configuredProtectedPaths(cfg, configPath),
	})
}

type configuredBackupJobs struct{}

func (configuredBackupJobs) BackupJobs() ([]clientbackup.Job, error) {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return nil, err
	}
	configPath, err := runtimeConfigPath()
	if err != nil {
		return nil, err
	}
	protected := configuredProtectedPaths(cfg, configPath)
	jobs := make([]clientbackup.Job, 0, len(cfg.Backup.Jobs))
	for _, value := range cfg.Backup.Jobs {
		every, err := time.ParseDuration(value.Every)
		if err != nil {
			return nil, fmt.Errorf("backup job %q: %w", value.Name, err)
		}
		jobs = append(jobs, clientbackup.Job{
			Name: value.Name, Source: value.Source, Every: every,
			Enabled: value.Enabled, Message: value.Message, Protected: protected,
		})
	}
	return jobs, nil
}

func loadConfigForUpdate() (*clientconfig.Config, string, error) {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return nil, "", err
	}
	path, err := runtimeConfigPath()
	if err != nil {
		return nil, "", err
	}
	return cfg, path, nil
}

func runtimeConfigPath() (string, error) {
	if cfgFile != "" {
		return cfgFile, nil
	}
	return clientconfig.DefaultPath()
}

func configuredProtectedPaths(cfg *clientconfig.Config, configPath string) []string {
	return []string{
		configPath,
		cfg.Backup.KeyringPath,
		cfg.Backup.StatePath,
		cfg.Workspace.StatePath,
		cfg.Daemon.StatePath,
		cfg.Daemon.SocketPath,
		pidPath(cfg.Daemon.SocketPath),
	}
}

var _ clientbackup.Runner = (*configuredBackupRunner)(nil)
var _ clientbackup.JobSource = configuredBackupJobs{}

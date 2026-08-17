package main

import (
	"errors"
	"fmt"
	"time"

	"github.com/dewebprotocol/malt-client/application"
	clientbackup "github.com/dewebprotocol/malt-client/application/backup"
	clientconfig "github.com/dewebprotocol/malt-client/internal/config"
	"github.com/dewebprotocol/malt-client/internal/keyring"
	gatewayclient "github.com/dewebprotocol/malt-client/transport"
	"github.com/spf13/cobra"
)

var (
	backupForeground bool
	backupMessage    string
	restoreOverwrite bool
	restoreBucket    string
	restoreBranch    string
	restoreYes       bool
)

var backupCmd = &cobra.Command{
	Use:   "backup [plan...]",
	Short: "Push local changes from all or selected backup plans",
	Long: `Snapshot every changed binding before observing the remote branch,
then publish the encrypted candidates. Independent binding changes are merged
automatically by the Gateway; an unmergeable change is preserved on a conflict
branch and reported without discarding the local candidate.`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runConfiguredPlanOperation(cmd, "backup", args)
	},
}

var restoreCmd = &cobra.Command{
	Use:   "restore [plan] <destination>",
	Short: "Restore an entire Bucket branch plan into one local destination",
	Args:  cobra.RangeArgs(1, 2),
	RunE:  runRestore,
}

var backupScheduleCmd = &cobra.Command{
	Use:   "schedule",
	Short: "Configure daemon automatic backup plans",
}

var backupKeyInitCmd = &cobra.Command{
	Use:   "key-init",
	Short: "Create the runtime-owned backup keyring without replacing configuration",
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
	Use:   "set <plan>",
	Short: "Configure automatic backup for an existing plan",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		every, err := time.ParseDuration(scheduleEvery)
		if err != nil {
			return fmt.Errorf("invalid backup interval: %w", err)
		}
		store, err := configuredPlanStore()
		if err != nil {
			return err
		}
		plan, err := store.SetSchedule(args[0], every, scheduleEnabled, scheduleMessage)
		if err != nil {
			return err
		}
		printJSON(plan)
		return nil
	},
}

var backupScheduleListCmd = &cobra.Command{
	Use:   "list",
	Short: "List scheduled plans and their latest daemon results",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		cfg, err := loadRuntimeConfig()
		if err != nil {
			return err
		}
		store, err := clientbackup.OpenPlanStore(cfg.Backup.PlansPath)
		if err != nil {
			return err
		}
		plans, err := store.List()
		if err != nil {
			return err
		}
		states := map[string]any{}
		for _, plan := range plans {
			history, err := clientbackup.NewHistory(planHistoryPath(cfg, plan.ID))
			if err != nil {
				return err
			}
			snapshot, err := history.Snapshot()
			if err != nil {
				return err
			}
			pending, err := history.Pending()
			if err != nil {
				return err
			}
			states[plan.ID] = map[string]any{"state": snapshot[plan.ID], "pending_backup": pending}
		}
		printJSON(map[string]any{"plans": plans, "states": states})
		return nil
	},
}

var backupScheduleRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove an automatic backup schedule",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		store, err := configuredPlanStore()
		if err != nil {
			return err
		}
		plan, err := store.ClearSchedule(args[0])
		if err != nil {
			return err
		}
		printJSON(plan)
		return nil
	},
}

func init() {
	backupCmd.Flags().BoolVar(&backupForeground, "foreground", false, "Run without the daemon (advanced bypass)")
	backupCmd.Flags().StringVarP(&backupMessage, "message", "m", "", "Bucket commit message")
	restoreCmd.Flags().BoolVar(&restoreOverwrite, "overwrite", false, "Replace existing restored files")
	restoreCmd.Flags().StringVar(&restoreBucket, "bucket", "", "Discover and restore a complete remote Bucket branch")
	restoreCmd.Flags().StringVar(&restoreBranch, "branch", "main", "Bucket branch used with --bucket")
	restoreCmd.Flags().BoolVarP(&restoreYes, "yes", "y", false, "Confirm replacement of an existing safe destination")
	backupScheduleSetCmd.Flags().StringVar(&scheduleEvery, "every", "24h", "Backup interval (for example 30m or 24h)")
	backupScheduleSetCmd.Flags().StringVarP(&scheduleMessage, "message", "m", "", "Bucket commit message")
	backupScheduleSetCmd.Flags().BoolVar(&scheduleEnabled, "enabled", true, "Enable the plan")
	backupScheduleCmd.AddCommand(backupScheduleSetCmd, backupScheduleListCmd, backupScheduleRemoveCmd)
	backupCmd.AddCommand(backupScheduleCmd, backupKeyInitCmd, backupKeyRotateCmd)
	rootCmd.AddCommand(backupCmd, restoreCmd)
}

func runRestore(cmd *cobra.Command, args []string) error {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return err
	}
	store, err := clientbackup.OpenPlanStore(cfg.Backup.PlansPath)
	if err != nil {
		return err
	}
	var (
		plan        clientbackup.Plan
		destination string
		discover    bool
	)
	if restoreBucket == "" {
		if len(args) != 2 {
			return fmt.Errorf("restore requires <plan> <destination>, or --bucket <bucket> <destination>")
		}
		plan, err = store.Get(args[0])
		destination = args[1]
	} else {
		if len(args) != 1 {
			return fmt.Errorf("branch restore requires exactly one destination")
		}
		discover = true
		destination = args[0]
		options, optionsErr := requiredGatewayOptions(cfg, "", "")
		if optionsErr != nil {
			return optionsErr
		}
		account, clientErr := gatewayclient.New(options)
		if clientErr != nil {
			return clientErr
		}
		bucket, resolveErr := resolveBucket(cmd.Context(), account, restoreBucket)
		if resolveErr != nil {
			return resolveErr
		}
		branch, branchErr := normalizeCLIPlanBranch(restoreBranch)
		if branchErr != nil {
			return branchErr
		}
		if _, found, findErr := store.FindTarget(bucket.ID, branch); findErr != nil {
			return findErr
		} else if found {
			return fmt.Errorf("Bucket %s branch %s already has a local plan; restore it by plan name", bucket.Name, branch)
		}
		plan, err = clientbackup.NewRestorePlan(bucket.ID, bucket.Name, branch)
	}
	if err != nil {
		return err
	}
	if restoreOverwrite && !restoreYes {
		confirmed, err := confirmAtTerminal(cmd, fmt.Sprintf("Replace the existing safe restore destination %s?", destination))
		if err != nil {
			return err
		}
		if !confirmed {
			return fmt.Errorf("restore replacement was not confirmed; rerun interactively or pass --yes")
		}
	}
	service, err := buildPlanService(cfg, plan)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 3; attempt++ {
		if discover {
			restored, restoreErr := service.RestoreBranchTo(cmd.Context(), destination, restoreOverwrite)
			if restoreErr == nil {
				restored, err = store.ImportRestored(restored)
				if err != nil {
					return fmt.Errorf("restored plaintext but could not register its local plan: %w", err)
				}
				restoredService, baselineErr := buildPlanService(cfg, restored)
				if baselineErr == nil {
					_, baselineErr = restoredService.RecordRestoredBaseline(cmd.Context())
				}
				if baselineErr != nil {
					return fmt.Errorf("restored and registered plaintext but could not record its local baseline: %w", baselineErr)
				}
				fmt.Printf("Restored and registered backup plan %s (%s/%s) to %s\n", restored.Name, restored.BucketName, restored.Branch, destination)
				return nil
			}
			retry, retryErr := acceptRestoreRoot(cmd, cfg, restoreErr)
			if retryErr != nil {
				return retryErr
			}
			if retry {
				continue
			}
			return daemonCommandError(restoreErr)
		}
		restoreErr := service.RestoreTo(cmd.Context(), destination, restoreOverwrite)
		if restoreErr == nil {
			fmt.Printf("Restored backup plan %s (%s/%s) to %s\n", plan.Name, plan.BucketName, plan.Branch, destination)
			return nil
		}
		retry, retryErr := acceptRestoreRoot(cmd, cfg, restoreErr)
		if retryErr != nil {
			return retryErr
		}
		if retry {
			continue
		}
		return daemonCommandError(restoreErr)
	}
	return fmt.Errorf("restore root changed repeatedly; inspect `malt root list` before retrying")
}

func acceptRestoreRoot(cmd *cobra.Command, cfg *clientconfig.Config, restoreErr error) (bool, error) {
	var rootErr *clientbackup.UnacceptedRootError
	if !errors.As(restoreErr, &rootErr) {
		return false, nil
	}
	confirmed, err := confirmAtTerminal(cmd, fmt.Sprintf(
		"Restore observed remote root %s. Accept it for %s?", rootErr.Observed, rootErr.Alias,
	))
	if err != nil || !confirmed {
		return false, err
	}
	store, _, err := openTrustStore()
	if err != nil {
		return false, err
	}
	roots, err := application.NewRoots(store)
	if err != nil {
		return false, err
	}
	if !rootErr.Accepted.Defined() {
		_, err = roots.Trust(rootErr.Alias, rootErr.Observed.String(), "unixfs", cfg.GatewayBaseURL(), "explicit-malt-restore")
	} else {
		_, err = roots.AcceptCandidate(rootErr.Alias, rootErr.Observed, "explicit-malt-restore")
	}
	return err == nil, err
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

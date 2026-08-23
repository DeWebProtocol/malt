package main

import (
	"errors"
	"fmt"

	clientbackup "github.com/dewebprotocol/malt-client/application/backup"
	"github.com/spf13/cobra"
)

var conflictCmd = &cobra.Command{
	Use:   "conflict",
	Short: "Inspect and resolve encrypted backup conflicts",
}

var conflictListCmd = &cobra.Command{
	Use:   "list",
	Short: "List unresolved backup conflict branches and local merge checkouts",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
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
		statuses := make([]clientbackup.ConflictStatus, 0)
		for _, plan := range plans {
			service, err := buildPlanService(cfg, plan)
			if err != nil {
				return err
			}
			status, err := service.ConflictStatus()
			err = errors.Join(err, service.Close())
			if err != nil {
				return err
			}
			if len(status.Stashes) != 0 || status.Checkout != nil {
				statuses = append(statuses, status)
			}
		}
		printJSON(map[string]any{"conflicts": statuses})
		return nil
	},
}

var (
	conflictManual     bool
	conflictKeepLocal  bool
	conflictKeepRemote bool
	conflictMessage    string
)

var conflictResolveCmd = &cobra.Command{
	Use:   "resolve <plan>",
	Short: "Resolve a conflict using manually merged, local, or remote plaintext",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		selected := 0
		resolution := clientbackup.ConflictResolution("")
		if conflictManual {
			selected++
			resolution = clientbackup.ConflictManual
		}
		if conflictKeepLocal {
			selected++
			resolution = clientbackup.ConflictKeepLocal
		}
		if conflictKeepRemote {
			selected++
			resolution = clientbackup.ConflictKeepRemote
		}
		if selected != 1 {
			return fmt.Errorf("select exactly one of --manual, --keep-local, or --keep-remote")
		}
		cfg, err := loadRuntimeConfig()
		if err != nil {
			return err
		}
		store, err := clientbackup.OpenPlanStore(cfg.Backup.PlansPath)
		if err != nil {
			return err
		}
		plan, err := store.Get(args[0])
		if err != nil {
			return err
		}
		service, err := buildPlanService(cfg, plan)
		if err != nil {
			return err
		}
		result, err := service.ResolveConflict(cmd.Context(), resolution, conflictMessage)
		if result != nil {
			printJSON(result)
		}
		return errors.Join(err, service.Close())
	},
}

func init() {
	conflictResolveCmd.Flags().BoolVar(&conflictManual, "manual", false, "Install and publish the edited merged trees from the conflict checkout")
	conflictResolveCmd.Flags().BoolVar(&conflictKeepLocal, "keep-local", false, "Keep and republish the current local working tree")
	conflictResolveCmd.Flags().BoolVar(&conflictKeepRemote, "keep-remote", false, "Replace bindings with the locally accepted remote root")
	conflictResolveCmd.Flags().StringVarP(&conflictMessage, "message", "m", "", "Resolution commit message")
	conflictCmd.AddCommand(conflictListCmd, conflictResolveCmd)
	rootCmd.AddCommand(conflictCmd)
}

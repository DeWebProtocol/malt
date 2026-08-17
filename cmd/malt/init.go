package main

import (
	"fmt"

	clientconfig "github.com/dewebprotocol/malt-client/internal/config"
	"github.com/dewebprotocol/malt-client/internal/keyring"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create local runtime configuration",
	Args:  cobra.NoArgs,
	RunE: func(*cobra.Command, []string) error {
		cfg, err := clientconfig.Default()
		if err != nil {
			return err
		}
		path := cfgFile
		if path == "" {
			path, err = clientconfig.DefaultPath()
			if err != nil {
				return err
			}
		}
		if err := clientconfig.Write(path, cfg); err != nil {
			return err
		}
		if _, err := keyring.Create(cfg.Backup.KeyringPath); err != nil {
			return err
		}
		fmt.Printf("Initialized MALT local runtime config: %s\n", path)
		fmt.Printf("Initialized encrypted backup keyring: %s\n", cfg.Backup.KeyringPath)
		return nil
	},
}

func init() { rootCmd.AddCommand(initCmd) }

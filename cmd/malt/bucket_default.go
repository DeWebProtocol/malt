package main

import (
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/config"
	"github.com/spf13/cobra"
)

var (
	bucketDefaultClear bool
)

func init() {
	bucketCmd.AddCommand(bucketDefaultCmd)
	bucketDefaultCmd.Flags().BoolVar(&bucketDefaultClear, "clear", false, "clear the configured default bucket")
}

var bucketDefaultCmd = &cobra.Command{
	Use:   "default <id>",
	Short: "Set the default bucket id in config",
	Args:  cobra.MaximumNArgs(1),
	RunE:  runBucketDefault,
}

func runBucketDefault(cmd *cobra.Command, args []string) error {
	configPath, err := config.ResolveConfigPath(cfgFile)
	if err != nil {
		return err
	}

	// Load existing config when present; otherwise start from defaults.
	var cfg *config.Config
	if _, statErr := os.Stat(configPath); statErr == nil {
		cfg, err = config.LoadFromFile(configPath)
		if err != nil {
			return err
		}
	} else {
		cfg = config.DefaultConfig()
	}

	if bucketDefaultClear {
		cfg.Client.DefaultBucketID = ""
	} else {
		if len(args) != 1 || args[0] == "" {
			return fmt.Errorf("bucket id is required (or use --clear)")
		}
		cfg.Client.DefaultBucketID = args[0]
	}

	if err := config.WriteToFile(configPath, cfg); err != nil {
		return err
	}

	if cfg.Client.DefaultBucketID == "" {
		fmt.Fprintln(os.Stdout, "default bucket cleared")
	} else {
		fmt.Fprintf(os.Stdout, "default bucket: %s\n", cfg.Client.DefaultBucketID)
	}
	return nil
}


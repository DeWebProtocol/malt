package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/dewebprotocol/malt-client/internal/keyring"
	"github.com/dewebprotocol/malt-client/internal/securefile"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var (
	recoveryExportPassphraseFile string
	recoveryImportPassphraseFile string
)

var recoveryCmd = &cobra.Command{
	Use:   "recovery",
	Short: "Export or import the encrypted backup-key recovery bundle",
}

var recoveryExportCmd = &cobra.Command{
	Use:   "export <bundle>",
	Short: "Export all backup key epochs into a passphrase-encrypted bundle",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		cfg, err := loadRuntimeConfig()
		if err != nil {
			return err
		}
		passphrase, err := recoveryPassphrase(recoveryExportPassphraseFile, true)
		if err != nil {
			return err
		}
		defer clearSecret(passphrase)
		keys, err := keyring.Open(cfg.Backup.KeyringPath)
		if err != nil {
			return err
		}
		if err := keys.ExportRecovery(args[0], passphrase); err != nil {
			return err
		}
		printJSON(map[string]any{
			"bundle": args[0], "active_epoch": keys.ActiveEpoch(),
			"scope": "all-buckets",
		})
		return nil
	},
}

var recoveryImportCmd = &cobra.Command{
	Use:   "import <bundle>",
	Short: "Restore or merge backup key epochs from an encrypted bundle",
	Args:  cobra.ExactArgs(1),
	RunE: func(_ *cobra.Command, args []string) error {
		cfg, err := loadRuntimeConfig()
		if err != nil {
			return err
		}
		passphrase, err := recoveryPassphrase(recoveryImportPassphraseFile, false)
		if err != nil {
			return err
		}
		defer clearSecret(passphrase)
		keys, err := keyring.ImportRecovery(args[0], cfg.Backup.KeyringPath, passphrase)
		if err != nil {
			return err
		}
		printJSON(map[string]any{
			"bundle": args[0], "keyring_path": cfg.Backup.KeyringPath,
			"active_epoch": keys.ActiveEpoch(), "scope": "all-buckets",
		})
		return nil
	},
}

func init() {
	recoveryExportCmd.Flags().StringVar(
		&recoveryExportPassphraseFile, "passphrase-file", "",
		"Read the recovery passphrase from an owner-protected file",
	)
	recoveryImportCmd.Flags().StringVar(
		&recoveryImportPassphraseFile, "passphrase-file", "",
		"Read the recovery passphrase from an owner-protected file",
	)
	recoveryCmd.AddCommand(recoveryExportCmd, recoveryImportCmd)
	rootCmd.AddCommand(recoveryCmd)
}

func recoveryPassphrase(path string, confirm bool) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path != "" {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("stat recovery passphrase file: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("recovery passphrase path must be a regular file, not a symlink")
		}
		if err := securefile.Secure(path); err != nil {
			return nil, fmt.Errorf("protect recovery passphrase file: %w", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read recovery passphrase file: %w", err)
		}
		return trimOneLineEnding(data), nil
	}
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return nil, fmt.Errorf("recovery passphrase requires an interactive terminal or --passphrase-file")
	}
	fmt.Fprint(os.Stderr, "Recovery passphrase: ")
	first, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return nil, err
	}
	if !confirm {
		return first, nil
	}
	fmt.Fprint(os.Stderr, "Confirm recovery passphrase: ")
	second, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		clearSecret(first)
		return nil, err
	}
	defer clearSecret(second)
	if !bytes.Equal(first, second) {
		clearSecret(first)
		return nil, fmt.Errorf("recovery passphrases do not match")
	}
	return first, nil
}

func trimOneLineEnding(value []byte) []byte {
	value = bytes.TrimSuffix(value, []byte("\n"))
	value = bytes.TrimSuffix(value, []byte("\r"))
	return value
}

func clearSecret(value []byte) {
	for i := range value {
		value[i] = 0
	}
}

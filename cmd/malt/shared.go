package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	daemonclient "github.com/dewebprotocol/malt/client"
	"github.com/dewebprotocol/malt/config"
	cid "github.com/ipfs/go-cid"
)

var defaultClient *daemonclient.Client

func loadRuntimeConfig() (*config.Config, error) {
	if cfgFile != "" {
		return config.LoadFromFile(cfgFile)
	}
	return config.Load()
}

func mustDaemonClient() *daemonclient.Client {
	if defaultClient == nil {
		cfg, err := loadRuntimeConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
			os.Exit(1)
		}
		defaultClient = daemonclient.New(cfg)
	}
	return defaultClient
}

// parseCID parses a CID string or returns an error.
func parseCID(s string) (cid.Cid, error) {
	c, err := cid.Decode(s)
	if err != nil {
		return cid.Undef, fmt.Errorf("invalid CID %q: %w", s, err)
	}
	return c, nil
}

// printJSON marshals and prints a value as JSON.
func printJSON(v interface{}) {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

func daemonCommandError(err error) error {
	if err == nil {
		return nil
	}
	var apiErr *daemonclient.Error
	if errors.As(err, &apiErr) {
		return fmt.Errorf("daemon request failed: %s", apiErr.Message)
	}
	return fmt.Errorf("daemon unavailable or config invalid: %w", err)
}

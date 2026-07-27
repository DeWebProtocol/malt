package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/dewebprotocol/malt-client/application"
	clientconfig "github.com/dewebprotocol/malt-client/internal/config"
	"github.com/dewebprotocol/malt-client/internal/deviceauth"
	client "github.com/dewebprotocol/malt-client/transport"
)

func loadRuntimeConfig() (*clientconfig.Config, error) {
	return clientconfig.Load(cfgFile)
}

func gatewayClient() (*client.Client, error) {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return nil, err
	}
	opts, err := gatewayOptions(cfg, cfg.Gateway.Bucket, "")
	if err != nil {
		return nil, err
	}
	return client.New(opts)
}

func gatewayOptions(cfg *clientconfig.Config, bucketID, branch string) (client.Options, error) {
	if cfg == nil {
		return client.Options{}, fmt.Errorf("client config is nil")
	}
	opts := client.Options{
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
		if opts.BucketID == "" {
			return opts, nil
		}
		return client.Options{}, fmt.Errorf("Gateway account is not authenticated; run `malt login`")
	}
	if err != nil {
		return client.Options{}, err
	}
	if strings.TrimRight(value.Gateway, "/") != strings.TrimRight(cfg.GatewayBaseURL(), "/") {
		return client.Options{}, fmt.Errorf("stored device credential belongs to %s; run `malt login` for %s", value.Gateway, cfg.GatewayBaseURL())
	}
	opts.DeviceAuthorizer = provider
	return opts, nil
}

func requiredGatewayOptions(cfg *clientconfig.Config, bucketID, branch string) (client.Options, error) {
	opts, err := gatewayOptions(cfg, bucketID, branch)
	if err != nil {
		return client.Options{}, err
	}
	if opts.TenantBearerToken == "" && opts.DeviceAuthorizer == nil {
		return client.Options{}, fmt.Errorf("Gateway account is not authenticated; run `malt login`")
	}
	return opts, nil
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

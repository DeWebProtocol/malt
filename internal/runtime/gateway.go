package runtime

import (
	"errors"
	"fmt"
	"strings"

	clientconfig "github.com/dewebprotocol/malt-client/internal/config"
	"github.com/dewebprotocol/malt-client/internal/deviceauth"
	client "github.com/dewebprotocol/malt-client/transport"
)

// GatewayOptions selects credentials from one coherent runtime configuration.
func GatewayOptions(cfg *clientconfig.Config, bucketID, branch string) (client.Options, error) {
	if cfg == nil {
		return client.Options{}, fmt.Errorf("runtime config is nil")
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

func RequiredGatewayOptions(cfg *clientconfig.Config, bucketID, branch string) (client.Options, error) {
	opts, err := GatewayOptions(cfg, bucketID, branch)
	if err != nil {
		return client.Options{}, err
	}
	if opts.TenantBearerToken == "" && opts.DeviceAuthorizer == nil {
		return client.Options{}, fmt.Errorf("Gateway account is not authenticated; run `malt login`")
	}
	return opts, nil
}

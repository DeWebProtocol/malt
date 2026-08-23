package main

import (
	clientconfig "github.com/dewebprotocol/malt-client/internal/config"
	localruntime "github.com/dewebprotocol/malt-client/internal/runtime"
	gatewayclient "github.com/dewebprotocol/malt-client/transport"
)

// makeCASClient selects Gateway, local-only, or Gateway-primary hybrid CAS at
// the process composition boundary. Local-only is permitted only for use cases
// that do not publish a managed Gateway dataset.
func makeCASClient(gatewayRequired bool) (*localruntime.CASBinding, error) {
	cfg, err := loadRuntimeConfig()
	if err != nil {
		return nil, err
	}
	if cfg.Transport.CASPolicy == clientconfig.CASPolicyLocal {
		return localruntime.ComposeCAS(cfg, nil, gatewayRequired)
	}
	options, err := gatewayOptions(cfg, cfg.Gateway.Bucket, "")
	if err != nil {
		return nil, err
	}
	remote, err := gatewayclient.New(options)
	if err != nil {
		return nil, err
	}
	return localruntime.ComposeCAS(cfg, remote, gatewayRequired)
}

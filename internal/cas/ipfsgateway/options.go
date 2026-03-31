// Package ipfsgateway provides an IPFS HTTP gateway client.
package ipfsgateway

import "time"

const IPFS_OFFICIAL_GATEWAY = "https://ipfs.io/ipfs"

// Option configures an IPFS gateway client.
type Option func(*options)

type options struct {
	gatewayURL string
	timeout    time.Duration
}

func defaultOptions() *options {
	return &options{
		gatewayURL: IPFS_OFFICIAL_GATEWAY,
		timeout:    30 * time.Second,
	}
}

// WithGatewayURL sets the IPFS gateway URL.
func WithGatewayURL(url string) Option {
	return func(o *options) {
		o.gatewayURL = url
	}
}

// WithTimeout sets the HTTP request timeout.
func WithTimeout(timeout time.Duration) Option {
	return func(o *options) {
		o.timeout = timeout
	}
}

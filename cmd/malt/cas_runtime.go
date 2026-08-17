package main

import gatewayclient "github.com/dewebprotocol/malt-client/transport"

// makeCASClient uses the gateway-owned CAS adapter. The runtime never imports a
// MALT-core storage implementation and verifies the returned CID locally.
func makeCASClient() (*gatewayclient.Client, error) { return gatewayClient() }

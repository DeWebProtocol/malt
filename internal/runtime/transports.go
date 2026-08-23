package runtime

import (
	"errors"
	"fmt"
	"strings"
	"sync"

	clientconfig "github.com/dewebprotocol/malt-client/internal/config"
	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
	hybridtransport "github.com/dewebprotocol/malt-client/transport/hybrid"
	localtransport "github.com/dewebprotocol/malt-client/transport/local"
)

// CASBinding owns any local resources opened while selecting one immutable
// byte topology. Gateway-only bindings do not own the caller-supplied remote;
// local and hybrid bindings close their local store exactly once.
type CASBinding struct {
	transportcap.CAS
	closeMu   sync.Mutex
	closeFunc func() error
	closed    bool
}

// Close deterministically releases owned local directory handles. It is safe
// to call repeatedly after all CAS operations have quiesced.
func (b *CASBinding) Close() error {
	if b == nil {
		return nil
	}
	b.closeMu.Lock()
	defer b.closeMu.Unlock()
	if b.closed {
		return nil
	}
	if b.closeFunc != nil {
		if err := b.closeFunc(); err != nil {
			return err
		}
	}
	b.closed = true
	return nil
}

// ComposeCAS selects immutable-byte topology in the runtime composition root.
// gatewayRequired is true for managed-Bucket operations whose blocks must be
// persisted at the Gateway. A local-only CAS is useful for local Merkle-DAG
// import now and can back a future local Native/Mutations executor later.
func ComposeCAS(cfg *clientconfig.Config, gateway transportcap.CAS, gatewayRequired bool) (*CASBinding, error) {
	if cfg == nil {
		return nil, fmt.Errorf("runtime config is nil")
	}
	policy := strings.ToLower(strings.TrimSpace(cfg.Transport.CASPolicy))
	if policy == "" {
		policy = clientconfig.CASPolicyGateway
	}
	switch policy {
	case clientconfig.CASPolicyGateway:
		if nilInterface(gateway) {
			return nil, fmt.Errorf("Gateway CAS is required by transport policy")
		}
		return &CASBinding{CAS: gateway}, nil
	case clientconfig.CASPolicyLocal:
		if gatewayRequired {
			return nil, fmt.Errorf("local-only CAS cannot satisfy a managed Gateway dataset; select gateway or hybrid transport policy")
		}
		store, err := localtransport.Open(localtransport.Options{Directory: cfg.Transport.LocalCASDir})
		if err != nil {
			return nil, err
		}
		return &CASBinding{CAS: store, closeFunc: store.Close}, nil
	case clientconfig.CASPolicyHybrid:
		if nilInterface(gateway) {
			return nil, fmt.Errorf("Gateway CAS is required by hybrid transport policy")
		}
		cache, err := localtransport.Open(localtransport.Options{Directory: cfg.Transport.LocalCASDir})
		if err != nil {
			return nil, err
		}
		selected, err := hybridtransport.NewCAS(hybridtransport.CASOptions{Primary: gateway, Cache: cache})
		if err != nil {
			return nil, errors.Join(err, cache.Close())
		}
		return &CASBinding{CAS: selected, closeFunc: cache.Close}, nil
	default:
		return nil, fmt.Errorf("unsupported transport CAS policy %q", cfg.Transport.CASPolicy)
	}
}

package transport

import (
	"context"
	"fmt"
	"github.com/dewebprotocol/malt-core/maltcid"
)

// DefaultBackend is an untrusted creation preference. Existing Roots select
// their own verification profile; this response grants no trust or publication.
func (c *Client) DefaultBackend(ctx context.Context) (maltcid.BackendKind, error) {
	health, err := c.Health(ctx)
	if err != nil {
		return maltcid.BackendKindUnknown, err
	}
	backend := maltcid.BackendKind(health.CommitmentProfile)
	if backend != maltcid.BackendKindKZG && backend != maltcid.BackendKindIPA {
		return maltcid.BackendKindUnknown, fmt.Errorf("Gateway returned unsupported default commitment backend %q", health.CommitmentProfile)
	}
	return backend, nil
}

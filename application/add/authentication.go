package add

import (
	"context"
	"fmt"
	"github.com/dewebprotocol/malt-client/unixfs"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
)

// projectionWriter belongs to the UnixFS layout compiler, not the transport.
type projectionWriter interface {
	unixfs.StagedRootWriter
	unixfs.MeasuredPayloadWriter
}

func newProjectionWriter(ctx context.Context, remote Materializer, layout unixfs.LayoutKind) (*unixfs.AuthenticationAdapter, error) {
	backend, err := remote.DefaultBackend(ctx)
	if err != nil {
		return nil, err
	}
	var profile maltcid.ProfileID
	switch backend {
	case maltcid.BackendKindKZG:
		profile = maltcid.KZG4096
	case maltcid.BackendKindIPA:
		profile = maltcid.IPA256
	default:
		return nil, fmt.Errorf("unsupported creation backend %q", backend)
	}
	return unixfs.NewAuthenticationAdapter(layout, remote, nil, profile)
}

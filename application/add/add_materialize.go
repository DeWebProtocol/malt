package add

import (
	"context"
	"fmt"

	unixfs "github.com/dewebprotocol/malt-client/unixfs"
	clientverifier "github.com/dewebprotocol/malt-core/sdk/verifier"
)

type addMaterializeResult = unixfs.StagedMaterializeResult

func loadExistingCurrentTree(ctx context.Context, gateway unixfs.Remote, casClient addCASClient, rootCID string) (*unixfs.StagedNode, error) {
	verifier, err := clientverifier.NewDefault()
	if err != nil {
		return nil, fmt.Errorf("initialize local verifier: %w", err)
	}
	statter, err := unixfs.NewStagedPathStatter(unixfs.ReaderOptions{
		Remote: gateway, Blocks: casClient, Verifier: verifier,
	})
	if err != nil {
		return nil, fmt.Errorf("initialize verified UnixFS path projector: %w", err)
	}
	return unixfs.LoadStagedCurrentTree(ctx, statter, casClient, rootCID)
}

func materializeDirectory(ctx context.Context, gateway unixfs.StagedRootCreator, casClient addCASClient, node *unixfs.StagedNode, kind unixfs.LayoutKind) (*addMaterializeResult, error) {
	layout, err := unixfs.NewLayout(kind)
	if err != nil {
		return nil, err
	}
	return layout.Materialize(ctx, gateway, asAddCASBatcher(casClient), node)
}

func addMaterializeStats(dst *addMaterializeResult, src *addMaterializeResult) {
	unixfs.AddStagedMaterializeStats(dst, src)
}

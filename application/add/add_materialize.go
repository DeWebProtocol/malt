package add

import (
	"context"
	"fmt"

	unixfs "github.com/dewebprotocol/malt-client/unixfs"
	unixfsmodel "github.com/dewebprotocol/malt-client/unixfs/model"
	clientverifier "github.com/dewebprotocol/malt/sdk/verifier"
	cid "github.com/ipfs/go-cid"
)

type addMaterializeResult = unixfs.StagedMaterializeResult

type addStagedPathStatter struct {
	reader unixfs.Reader
}

func (s addStagedPathStatter) StatStagedPath(ctx context.Context, root string, p string) (unixfs.StagedPathStat, error) {
	rootCID, err := cid.Parse(root)
	if err != nil {
		return unixfs.StagedPathStat{}, err
	}
	stat, err := s.reader.Stat(ctx, rootCID, p)
	if err != nil {
		return unixfs.StagedPathStat{}, err
	}
	payload := ""
	if stat.Kind == unixfs.StagedKindDirectory {
		payload = stat.Payload.String()
	}
	return unixfs.StagedPathStat{
		Kind:        stat.Kind,
		StorageKind: stat.StorageKind,
		Key:         stat.NodeRoot.String(),
		Payload:     payload,
	}, nil
}

func classifyStagedTarget(target cid.Cid) (kind, storageKind string, err error) {
	storageKind = unixfsmodel.StorageKindFromCID(target)
	switch storageKind {
	case "map":
		return unixfs.StagedKindDirectory, storageKind, nil
	case "list", "raw":
		return unixfs.StagedKindFile, storageKind, nil
	default:
		return "", "", fmt.Errorf("unsupported UnixFS target CID %s", target)
	}
}

func loadExistingCurrentTree(ctx context.Context, gateway unixfs.Remote, casClient addCASClient, rootCID string) (*unixfs.StagedNode, error) {
	verifier, err := clientverifier.NewDefault()
	if err != nil {
		return nil, fmt.Errorf("initialize local verifier: %w", err)
	}
	reader, err := unixfs.NewReader(unixfs.ReaderOptions{Remote: gateway, Blocks: casClient, Verifier: verifier})
	if err != nil {
		return nil, fmt.Errorf("initialize verified UnixFS reader: %w", err)
	}
	statter := addStagedPathStatter{reader: reader}
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

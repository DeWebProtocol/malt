package main

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt/layout/unixfs"
	daemonclient "github.com/dewebprotocol/malt/sdk/client"
	cid "github.com/ipfs/go-cid"
)

type addMaterializeResult = unixfs.StagedMaterializeResult

type addStagedRootCreator struct {
	daemon *daemonclient.Client
}

type addStagedPathStatter struct {
	daemon *daemonclient.Client
}

func (c addStagedRootCreator) CreateStagedRoot(ctx context.Context, bindings map[string]string) (cid.Cid, error) {
	resp, err := c.daemon.CreateRootStructure(ctx, bindings)
	if err != nil {
		return cid.Undef, err
	}
	rootCID, err := cid.Decode(resp.Root)
	if err != nil {
		return cid.Undef, fmt.Errorf("decode created map root: %w", err)
	}
	return rootCID, nil
}

func (s addStagedPathStatter) StatStagedPath(ctx context.Context, root string, p string) (unixfs.StagedPathStat, error) {
	stat, err := s.daemon.Stat(ctx, root, p)
	if err != nil {
		return unixfs.StagedPathStat{}, err
	}
	return unixfs.StagedPathStat{
		Kind:        stat.Kind,
		StorageKind: stat.StorageKind,
		Key:         stat.Key,
		Payload:     stat.Payload,
	}, nil
}

func loadExistingCurrentTree(ctx context.Context, daemon *daemonclient.Client, casClient addCASClient, rootCID string) (*unixfs.StagedNode, error) {
	return unixfs.LoadStagedCurrentTree(ctx, addStagedPathStatter{daemon: daemon}, casClient, rootCID)
}

func materializeDirectory(ctx context.Context, daemon *daemonclient.Client, casClient addCASClient, node *unixfs.StagedNode) (*addMaterializeResult, error) {
	return unixfs.MaterializeStagedDirectory(ctx, addStagedRootCreator{daemon: daemon}, asAddCASBatcher(casClient), node)
}

func addMaterializeStats(dst *addMaterializeResult, src *addMaterializeResult) {
	unixfs.AddStagedMaterializeStats(dst, src)
}

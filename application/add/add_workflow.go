package add

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/dewebprotocol/malt-client/unixfs"
	"github.com/dewebprotocol/malt-client/writeplan"
	"github.com/dewebprotocol/malt-core/protocol"
	cid "github.com/ipfs/go-cid"
)

type addUnixFSResult struct {
	Files            int
	Bytes            int64
	NewRoot          string
	ImmutableObjects int
	MALTObjects      int
	MALTMaps         int
	MALTLists        int
	ArcSets          int
	Arcs             int
	SymlinkRoots     int
}

type addCASClient = CAS

func addInputsWithUnixFS(ctx context.Context, remote Materializer, casClient addCASClient, rawInputs []string, root string, opts addBuildOptions) (*addUnixFSResult, error) {
	normalized, err := normalizeAddBuildOptions(opts)
	if err != nil {
		return nil, err
	}
	switch normalized.Target {
	case addTargetMALT:
		return addInputsWithMALTUnixFS(ctx, remote, casClient, rawInputs, root, normalized)
	case addTargetMerkleDAG:
		return addInputsWithMerkleDAGUnixFS(ctx, casClient, rawInputs, normalized)
	}
	return nil, fmt.Errorf("unsupported add target/model/layout %q/%q/%q", normalized.Target, normalized.Model, normalized.Layout)
}

func addInputsWithMALTUnixFS(ctx context.Context, remote Materializer, casClient addCASClient, rawInputs []string, root string, opts addBuildOptions) (result *addUnixFSResult, err error) {
	if remote == nil {
		return nil, fmt.Errorf("graph materialization capability is required")
	}
	staging, err := writeplan.NewStaging("", remote, casClient)
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, staging.Close()) }()
	local := preparedMaterializer{Materializer: remote, staging: staging}
	projection, err := newProjectionWriter(ctx, local, unixFSLayoutKind(opts.Layout))
	if err != nil {
		return nil, err
	}
	staged, err := buildAddStagingTree(ctx, staging, projection, rawInputs, opts)
	if err != nil {
		return nil, err
	}

	rootNode := staged.Root
	if strings.TrimSpace(root) != "" {
		existing, err := loadExistingCurrentTree(ctx, remote, casClient, root, unixFSLayoutKind(opts.Layout))
		if err != nil {
			return nil, err
		}
		rootNode = unixfs.MergeStagedNodes(existing, staged.Root)
	}
	mat, err := materializeDirectory(ctx, projection, staging, rootNode, unixFSLayoutKind(opts.Layout))
	if err != nil {
		return nil, err
	}
	base := cid.Undef
	if strings.TrimSpace(root) != "" {
		base, err = cid.Decode(root)
		if err != nil {
			return nil, err
		}
	}
	plan, err := staging.Plan(base, mat.Key)
	if err != nil {
		return nil, err
	}
	if err := plan.Persist(ctx, casClient, remote); err != nil {
		return nil, err
	}
	return &addUnixFSResult{
		Files:            staged.Files,
		Bytes:            staged.Bytes,
		NewRoot:          mat.Key.String(),
		ImmutableObjects: staged.ImmutableObjects + mat.ImmutableObjects,
		MALTObjects:      staged.MALTObjects + mat.MALTObjects,
		MALTMaps:         staged.MALTMaps + mat.MALTMaps,
		MALTLists:        staged.MALTLists + mat.MALTLists,
		ArcSets:          staged.ArcSets + mat.ArcSets,
		Arcs:             staged.Arcs + mat.Arcs,
		SymlinkRoots:     staged.SymlinkRoots,
	}, nil
}

type preparedMaterializer struct {
	Materializer
	staging *writeplan.Staging
}

func (p preparedMaterializer) AuthenticationCandidate(ctx context.Context, root cid.Cid) (*protocol.AuthenticationCandidate, error) {
	return p.staging.AuthenticationCandidate(ctx, root)
}
func (p preparedMaterializer) MaterializeAuthentication(ctx context.Context, candidate protocol.AuthenticationCandidate) (cid.Cid, error) {
	return p.staging.MaterializeAuthentication(ctx, candidate)
}

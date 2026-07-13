package server

import (
	"fmt"

	httpapi "github.com/dewebprotocol/malt/api/http"
	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/graph/resolver/step/explicit"
	"github.com/dewebprotocol/malt/mutation"
	cid "github.com/ipfs/go-cid"
)

func parseArcMap(raw map[string]string) (map[string]cid.Cid, error) {
	out := make(map[string]cid.Cid, len(raw))
	for path, target := range raw {
		parsed, err := parseOptionalCID(target)
		if err != nil {
			return nil, fmt.Errorf("invalid target for %q: %w", path, err)
		}
		out[path] = parsed
	}
	return out, nil
}

func semanticMutationFromRequest(baseRoot cid.Cid, deltaRequests []httpapi.SemanticMutationDelta) (mutation.SemanticMutation, error) {
	deltas := make([]mutation.ArcSetDelta, 0, len(deltaRequests))
	for i, deltaReq := range deltaRequests {
		delta, err := semanticDeltaFromRequest(deltaReq)
		if err != nil {
			return mutation.SemanticMutation{}, fmt.Errorf("delta %d: %w", i, err)
		}
		deltas = append(deltas, delta)
	}

	return mutation.SemanticMutation{
		BaseRoot: baseRoot,
		Deltas:   deltas,
	}, nil
}

func semanticDeltaFromRequest(req httpapi.SemanticMutationDelta) (mutation.ArcSetDelta, error) {
	object := cid.Undef
	if req.Object != "" {
		parsed, err := decodeCID(req.Object)
		if err != nil {
			return mutation.ArcSetDelta{}, fmt.Errorf("invalid object: %w", err)
		}
		object = parsed
	}
	expectedRoot := cid.Undef
	if req.ExpectedRoot != "" {
		parsed, err := decodeCID(req.ExpectedRoot)
		if err != nil {
			return mutation.ArcSetDelta{}, fmt.Errorf("invalid expected root: %w", err)
		}
		expectedRoot = parsed
	}

	kind := arcset.Kind(req.Kind)
	changes := make([]arcset.ArcChange, 0, len(req.Changes))
	for i, changeReq := range req.Changes {
		change, err := semanticChangeFromRequest(kind, changeReq)
		if err != nil {
			return mutation.ArcSetDelta{}, fmt.Errorf("change %d: %w", i, err)
		}
		changes = append(changes, change)
	}

	delta, err := arcset.NewCanonicalArcDelta(kind, changes)
	if err != nil {
		return mutation.ArcSetDelta{}, err
	}
	out := mutation.ArcSetDelta{
		Object:       object,
		ExpectedRoot: expectedRoot,
		Kind:         kind,
		Changes:      delta,
	}
	if req.Commit != nil && req.Commit.FixedList != nil {
		out.Commit.FixedList = &mutation.FixedListCommit{
			TotalSize: req.Commit.FixedList.TotalSize,
			ChunkSize: req.Commit.FixedList.ChunkSize,
		}
	}
	return out, nil
}

func semanticChangeFromRequest(kind arcset.Kind, req httpapi.SemanticMutationChange) (arcset.ArcChange, error) {
	var coord arcset.CanonicalCoordinate
	var err error
	switch kind {
	case arcset.KindMap:
		if req.Path == "" {
			return arcset.ArcChange{}, fmt.Errorf("path is required for map changes")
		}
		coord, err = arcset.NewMapCoordinate(req.Path)
	case arcset.KindList:
		if req.Index == nil {
			return arcset.ArcChange{}, fmt.Errorf("index is required for list changes")
		}
		if *req.Index > uint64(1<<63-1) {
			return arcset.ArcChange{}, fmt.Errorf("index is too large")
		}
		coord, err = arcset.NewListCoordinate(int64(*req.Index))
	default:
		return arcset.ArcChange{}, fmt.Errorf("%w: %q", arcset.ErrInvalidKind, kind)
	}
	if err != nil {
		return arcset.ArcChange{}, err
	}

	change := arcset.ArcChange{Coordinate: coord}
	if req.Before != nil {
		before, err := semanticTargetFromRequest(*req.Before)
		if err != nil {
			return arcset.ArcChange{}, fmt.Errorf("before: %w", err)
		}
		change.Before = &before
	}
	if req.After != nil {
		after, err := semanticTargetFromRequest(*req.After)
		if err != nil {
			return arcset.ArcChange{}, fmt.Errorf("after: %w", err)
		}
		change.After = &after
	}
	return change, nil
}

func semanticTargetFromRequest(req httpapi.SemanticMutationTarget) (arcset.TargetRef, error) {
	target, err := decodeCID(req.Target)
	if err != nil {
		return arcset.TargetRef{}, fmt.Errorf("invalid target: %w", err)
	}
	return semanticTargetRef(req.TargetKind, target)
}

func countSemanticDeltas(deltas []mutation.ArcSetDelta, kind arcset.Kind) int {
	count := 0
	for _, delta := range deltas {
		if delta.Kind == kind {
			count++
		}
	}
	return count
}

func semanticTargetRef(kind string, target cid.Cid) (arcset.TargetRef, error) {
	switch arcset.TargetKind(kind) {
	case "":
		return arcset.NewCIDTarget(target), nil
	case arcset.TargetKindUnknown:
		return arcset.NewUnknownTarget(target), nil
	case arcset.TargetKindCAS:
		return arcset.NewCASTarget(target), nil
	case arcset.TargetKindMap:
		return arcset.NewMapTarget(target), nil
	case arcset.TargetKindList:
		return arcset.NewListTarget(target), nil
	default:
		return arcset.TargetRef{}, fmt.Errorf("%w: %q", arcset.ErrInvalidTargetKind, kind)
	}
}

func buildCreateSnapshot(arcs map[string]cid.Cid) (arcset.ArcSet, int, error) {
	snapshot, err := arcset.NewArcSet(arcs)
	if err != nil {
		return nil, 0, err
	}

	payload, ok := snapshot.Get(explicit.PayloadArc)
	if !ok || !payload.Defined() {
		return nil, 0, fmt.Errorf("@payload binding is required")
	}

	canonical, err := arcset.ToPathMap(snapshot)
	if err != nil {
		return nil, 0, err
	}

	arcCount := 0
	for _, target := range canonical {
		if target.Defined() {
			arcCount++
		}
	}

	return snapshot, arcCount, nil
}

func parseOptionalCID(raw string) (cid.Cid, error) {
	if raw == "" {
		return cid.Undef, nil
	}
	return cid.Decode(raw)
}

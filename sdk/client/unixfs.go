package client

import (
	"context"
	"fmt"
	"strconv"

	"github.com/dewebprotocol/malt/api/http"
	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/graph/writer"
	"github.com/dewebprotocol/malt/layout/unixfs"
	cid "github.com/ipfs/go-cid"
)

// UnixFSClient composes UnixFS layout plans with the graph writer endpoint.
type UnixFSClient struct {
	client *Client
}

// UnixFS returns the client-side UnixFS layout facade.
func (c *Client) UnixFS() *UnixFSClient {
	return &UnixFSClient{client: c}
}

// ApplyPlan materializes a client-side UnixFS layout plan through the graph writer endpoint.
func (u *UnixFSClient) ApplyPlan(ctx context.Context, plan *unixfs.MutationPlan, fallbackRoot cid.Cid) (*httpapi.SemanticMutationResponse, error) {
	if plan == nil {
		return nil, fmt.Errorf("unixfs mutation plan is nil")
	}
	return u.client.ApplySemanticMutation(ctx, plan.WriterMutation(fallbackRoot))
}

// ApplySemanticMutation materializes a writer mutation through the graph writer endpoint.
func (c *Client) ApplySemanticMutation(ctx context.Context, mut writer.SemanticMutation) (*httpapi.SemanticMutationResponse, error) {
	if !mut.BaseRoot.Defined() {
		return nil, fmt.Errorf("semantic mutation base root is undefined")
	}
	req, err := semanticMutationRequestFromWriter(mut)
	if err != nil {
		return nil, err
	}
	return c.ApplyRootSemanticMutation(ctx, mut.BaseRoot.String(), req)
}

func semanticMutationRequestFromWriter(mut writer.SemanticMutation) (*httpapi.SemanticMutationRequest, error) {
	req := &httpapi.SemanticMutationRequest{
		Deltas: make([]httpapi.SemanticMutationDelta, 0, len(mut.Deltas)),
	}
	for i, delta := range mut.Deltas {
		if delta.Changes == nil {
			return nil, fmt.Errorf("delta %d changes are nil", i)
		}
		out := httpapi.SemanticMutationDelta{
			Kind:    string(delta.Kind),
			Changes: make([]httpapi.SemanticMutationChange, 0, delta.Changes.Len()),
		}
		if delta.Object.Defined() {
			out.Object = delta.Object.String()
		}
		if delta.ExpectedRoot.Defined() {
			out.ExpectedRoot = delta.ExpectedRoot.String()
		}
		if delta.Commit.FixedList != nil {
			out.Commit = &httpapi.SemanticCommitDescriptor{
				FixedList: &httpapi.SemanticFixedListCommit{
					TotalSize: delta.Commit.FixedList.TotalSize,
					ChunkSize: delta.Commit.FixedList.ChunkSize,
				},
			}
		}
		for _, change := range delta.Changes.Changes() {
			changeReq, err := semanticMutationChangeFromWriter(delta.Kind, change)
			if err != nil {
				return nil, fmt.Errorf("delta %d: %w", i, err)
			}
			out.Changes = append(out.Changes, changeReq)
		}
		req.Deltas = append(req.Deltas, out)
	}
	return req, nil
}

func semanticMutationChangeFromWriter(kind arcset.Kind, change arcset.ArcChange) (httpapi.SemanticMutationChange, error) {
	out := httpapi.SemanticMutationChange{}
	switch kind {
	case arcset.KindMap:
		out.Path = change.Coordinate.String()
	case arcset.KindList:
		index, err := strconv.ParseUint(change.Coordinate.String(), 10, 64)
		if err != nil {
			return httpapi.SemanticMutationChange{}, fmt.Errorf("invalid list coordinate %q: %w", change.Coordinate.String(), err)
		}
		out.Index = &index
	default:
		return httpapi.SemanticMutationChange{}, fmt.Errorf("%w: %q", arcset.ErrInvalidKind, kind)
	}
	if change.Before != nil {
		out.Before = semanticMutationTargetFromWriter(*change.Before)
	}
	if change.After != nil {
		out.After = semanticMutationTargetFromWriter(*change.After)
	}
	return out, nil
}

func semanticMutationTargetFromWriter(target arcset.TargetRef) *httpapi.SemanticMutationTarget {
	return &httpapi.SemanticMutationTarget{
		Target:     target.CID().String(),
		TargetKind: string(target.Kind()),
	}
}

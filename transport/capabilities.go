package transport

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt-client/nodeapi"
)

// DatasetBinding identifies the logical dataset selected by this Gateway HTTP
// adapter without exposing its URL or routes to application code.
func (c *Client) DatasetBinding() nodeapi.DatasetBinding {
	if c == nil {
		return nodeapi.DatasetBinding{}
	}
	return nodeapi.DatasetBinding{DatasetID: c.SelectedBucket(), Branch: c.SelectedBucketBranch()}
}

// ObserveHead implements the transport-neutral dataset capability.
func (c *Client) ObserveHead(ctx context.Context) (*nodeapi.ObservedHead, error) {
	value, err := c.BucketHead(ctx)
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("gateway returned a nil Bucket head")
	}
	result := observedHeadFromBucketRef(*value)
	return &result, nil
}

// ApplyCandidate implements the transport-neutral dataset capability. The
// Bucket-specific fields remain inside the HTTP adapter.
func (c *Client) ApplyCandidate(ctx context.Context, request nodeapi.ApplyRequest) (*nodeapi.ApplyResult, error) {
	request, err := nodeapi.NormalizeApplyRequest(c.SelectedBucketBranch(), request)
	if err != nil {
		return nil, err
	}
	value, err := c.PushBucket(ctx, BucketPushRequest{
		PushID: request.OperationID, Branch: request.Branch,
		BaseCommit: request.BaseCommit, BaseRoot: request.BaseRoot,
		CandidateRoot: request.CandidateRoot, BaseRevision: request.BaseRevision,
		ChangeSetCID: request.ChangeSetCID, Message: request.Message, MergePolicy: request.MergePolicy,
	})
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("gateway returned a nil Bucket apply result")
	}
	result := applyResultFromBucketPush(*value)
	if err := nodeapi.ValidateApplyResult(c.SelectedBucket(), request, result); err != nil {
		return nil, err
	}
	return &result, nil
}

func observedHeadFromBucketRef(value BucketRef) nodeapi.ObservedHead {
	return nodeapi.ObservedHead{
		DatasetID: value.BucketID, Name: value.Name, Kind: value.Kind, State: value.State,
		CommitID: value.CommitID, Root: value.Root, Revision: value.Revision,
		CreatedBy: value.CreatedBy, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func commitFromBucketCommit(value BucketCommit) nodeapi.Commit {
	return nodeapi.Commit{
		ID: value.ID, DatasetID: value.BucketID, Root: value.Root,
		Parents: append([]string(nil), value.Parents...), BaseRoot: value.BaseRoot,
		Author: value.Author, Credential: value.Credential,
		ChangeSetCID: value.ChangeSetCID, Message: value.Message, CreatedAt: value.CreatedAt,
	}
}

func applyResultFromBucketPush(value BucketPushResult) nodeapi.ApplyResult {
	result := nodeapi.ApplyResult{
		Status: value.Status, Head: observedHeadFromBucketRef(value.Head),
		Candidate: commitFromBucketCommit(value.Candidate), Commit: commitFromBucketCommit(value.Commit),
		MergeBase: value.MergeBase,
		Conflicts: make([]nodeapi.Conflict, len(value.Conflicts)),
	}
	if value.Branch != nil {
		branch := observedHeadFromBucketRef(*value.Branch)
		result.Branch = &branch
	}
	for index, conflict := range value.Conflicts {
		result.Conflicts[index] = nodeapi.Conflict{
			Coordinate: conflict.Coordinate, Base: conflict.Base, Local: conflict.Local, Remote: conflict.Remote,
		}
	}
	return result
}

var _ nodeapi.CAS = (*Client)(nil)
var _ nodeapi.DatasetBranch = (*Client)(nil)

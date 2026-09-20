package transport

import (
	"context"
	"fmt"

	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
)

// DatasetBinding identifies the logical dataset selected by this Gateway HTTP
// adapter without exposing its URL or routes to application code.
func (c *Client) DatasetBinding() transportcap.DatasetBinding {
	if c == nil {
		return transportcap.DatasetBinding{}
	}
	return transportcap.DatasetBinding{DatasetID: c.SelectedBucket(), Branch: c.SelectedBucketBranch()}
}

// ObserveHead implements the transport-neutral dataset capability.
func (c *Client) ObserveHead(ctx context.Context) (*transportcap.ObservedHead, error) {
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
func (c *Client) ApplyCandidate(ctx context.Context, request transportcap.ApplyRequest) (*transportcap.ApplyResult, error) {
	request, err := transportcap.NormalizeApplyRequest(c.SelectedBucketBranch(), request)
	if err != nil {
		return nil, err
	}
	value, err := c.PushBucket(ctx, BucketPushRequest{
		PushID: request.OperationID, Branch: request.Branch,
		BaseCommit: request.BaseCommit, BaseRoot: request.BaseRoot,
		CandidateRoot: request.CandidateRoot, BaseRevision: request.BaseRevision,
		ChangeSetCID: request.ChangeSetCID, Message: request.Message,
	})
	if err != nil {
		return nil, err
	}
	if value == nil {
		return nil, fmt.Errorf("gateway returned a nil Bucket apply result")
	}
	result := applyResultFromBucketPush(*value)
	if err := transportcap.ValidateApplyResult(c.SelectedBucket(), request, result); err != nil {
		return nil, err
	}
	return &result, nil
}

func observedHeadFromBucketRef(value BucketRef) transportcap.ObservedHead {
	return transportcap.ObservedHead{
		DatasetID: value.BucketID, Name: value.Name, Kind: value.Kind, State: value.State,
		CommitID: value.CommitID, Root: value.Root, Revision: value.Revision,
		CreatedBy: value.CreatedBy, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func commitFromBucketCommit(value BucketCommit) transportcap.Commit {
	return transportcap.Commit{
		ID: value.ID, DatasetID: value.BucketID, Root: value.Root,
		Parents: append([]string(nil), value.Parents...), BaseRoot: value.BaseRoot,
		Author: value.Author, Credential: value.Credential,
		ChangeSetCID: value.ChangeSetCID, Message: value.Message, CreatedAt: value.CreatedAt,
	}
}

func applyResultFromBucketPush(value BucketPushResult) transportcap.ApplyResult {
	result := transportcap.ApplyResult{
		Status: value.Status, Head: observedHeadFromBucketRef(value.Head),
		Candidate: commitFromBucketCommit(value.Candidate), Commit: commitFromBucketCommit(value.Commit),
		MergeBase: value.MergeBase,
		Conflicts: make([]transportcap.Conflict, len(value.Conflicts)),
	}
	if value.Branch != nil {
		branch := observedHeadFromBucketRef(*value.Branch)
		result.Branch = &branch
	}
	for index, conflict := range value.Conflicts {
		result.Conflicts[index] = transportcap.Conflict{
			Coordinate: conflict.Coordinate, Base: conflict.Base, Local: conflict.Local, Remote: conflict.Remote,
		}
	}
	return result
}

var _ transportcap.CAS = (*Client)(nil)
var _ transportcap.DatasetBranch = (*Client)(nil)

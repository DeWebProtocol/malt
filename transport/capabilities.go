package transport

import (
	"context"
	"fmt"

	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
	"github.com/dewebprotocol/malt-core/mutation"
	cid "github.com/ipfs/go-cid"
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
// existing Bucket DTO methods remain as compatibility HTTP-adapter APIs.
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

// ApplyMutation converts the Gateway HTTP receipt into a typed, untrusted
// candidate result.
func (c *Client) ApplyMutation(ctx context.Context, mut mutation.SemanticMutation) (transportcap.MutationResult, error) {
	value, err := c.ApplySemanticMutation(ctx, mut)
	if err != nil {
		return transportcap.MutationResult{}, err
	}
	if value == nil {
		return transportcap.MutationResult{}, fmt.Errorf("gateway returned a nil semantic mutation receipt")
	}
	base, err := cid.Parse(value.BaseRoot)
	if err != nil {
		return transportcap.MutationResult{}, fmt.Errorf("decode gateway mutation base root: %w", err)
	}
	candidate, err := cid.Parse(value.NewRoot)
	if err != nil {
		return transportcap.MutationResult{}, fmt.Errorf("decode gateway candidate root: %w", err)
	}
	return transportcap.MutationResult{
		BaseRoot: base, CandidateRoot: candidate,
		DeltaCount: value.DeltaCount, ArcCount: value.ArcCount,
		MALTObjectCount: value.MALTObjectCount, MapCount: value.MapCount, ListCount: value.ListCount,
	}, nil
}

// CreateStructureCandidate returns a typed candidate root from the Gateway
// structure-creation route.
func (c *Client) CreateStructureCandidate(ctx context.Context, arcs map[string]string) (cid.Cid, error) {
	value, err := c.CreateRootStructure(ctx, arcs)
	if err != nil {
		return cid.Undef, err
	}
	if value == nil {
		return cid.Undef, fmt.Errorf("gateway returned a nil structure receipt")
	}
	root, err := cid.Parse(value.Root)
	if err != nil {
		return cid.Undef, fmt.Errorf("decode gateway structure candidate root: %w", err)
	}
	return root, nil
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

var _ transportcap.Native = (*Client)(nil)
var _ transportcap.CAS = (*Client)(nil)
var _ transportcap.Mutations = (*Client)(nil)
var _ transportcap.DatasetBranch = (*Client)(nil)

package bucketsync

import (
	"context"
	"fmt"
	"strings"

	gatewaytransport "github.com/dewebprotocol/malt-client/transport"
	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
)

// Gateway is the legacy managed-Gateway synchronization port. New code should
// implement Remote and use OpenRemote or OpenRemoteBranch.
type Gateway interface {
	BucketHead(context.Context) (*gatewaytransport.BucketRef, error)
	PushBucket(context.Context, gatewaytransport.BucketPushRequest) (*gatewaytransport.BucketPushResult, error)
}

// Open retains the pre-capability API for existing callers.
// Deprecated: use OpenRemote.
func Open(path string, gateway Gateway, bucketID string) (*Service, error) {
	return OpenBranch(path, gateway, bucketID, "main")
}

// OpenBranch retains the pre-capability API for existing callers.
// Deprecated: use OpenRemoteBranch.
func OpenBranch(path string, gateway Gateway, bucketID, branch string) (*Service, error) {
	if gateway == nil {
		return nil, fmt.Errorf("Bucket sync path, Gateway, Bucket ID, and branch are required")
	}
	bucketID = strings.TrimSpace(bucketID)
	return OpenRemoteBranch(path, legacyGatewayRemote{
		gateway: gateway,
		binding: transportcap.DatasetBinding{DatasetID: bucketID, Branch: branch},
	}, bucketID, branch)
}

type legacyGatewayRemote struct {
	gateway Gateway
	binding transportcap.DatasetBinding
}

func (r legacyGatewayRemote) DatasetBinding() transportcap.DatasetBinding {
	return r.binding
}

func (r legacyGatewayRemote) ObserveHead(ctx context.Context) (*transportcap.ObservedHead, error) {
	value, err := r.gateway.BucketHead(ctx)
	if err != nil || value == nil {
		return nil, err
	}
	result := observedHeadFromGateway(*value)
	return &result, nil
}

func (r legacyGatewayRemote) ApplyCandidate(ctx context.Context, request transportcap.ApplyRequest) (*transportcap.ApplyResult, error) {
	value, err := r.gateway.PushBucket(ctx, gatewaytransport.BucketPushRequest{
		PushID: request.OperationID, Branch: request.Branch,
		BaseCommit: request.BaseCommit, BaseRoot: request.BaseRoot,
		CandidateRoot: request.CandidateRoot, BaseRevision: request.BaseRevision,
		ChangeSetCID: request.ChangeSetCID, Message: request.Message,
	})
	if err != nil || value == nil {
		return nil, err
	}
	result := applyResultFromGateway(*value)
	return &result, nil
}

func observedHeadFromGateway(value gatewaytransport.BucketRef) transportcap.ObservedHead {
	return transportcap.ObservedHead{
		DatasetID: value.BucketID, Name: value.Name, Kind: value.Kind, State: value.State,
		CommitID: value.CommitID, Root: value.Root, Revision: value.Revision,
		CreatedBy: value.CreatedBy, CreatedAt: value.CreatedAt, UpdatedAt: value.UpdatedAt,
	}
}

func commitFromGateway(value gatewaytransport.BucketCommit) transportcap.Commit {
	return transportcap.Commit{
		ID: value.ID, DatasetID: value.BucketID, Root: value.Root,
		Parents: append([]string(nil), value.Parents...), BaseRoot: value.BaseRoot,
		Author: value.Author, Credential: value.Credential,
		ChangeSetCID: value.ChangeSetCID, Message: value.Message, CreatedAt: value.CreatedAt,
	}
}

func applyResultFromGateway(value gatewaytransport.BucketPushResult) transportcap.ApplyResult {
	result := transportcap.ApplyResult{
		Status: value.Status, Head: observedHeadFromGateway(value.Head),
		Candidate: commitFromGateway(value.Candidate), Commit: commitFromGateway(value.Commit),
		MergeBase: value.MergeBase,
		Conflicts: make([]transportcap.Conflict, len(value.Conflicts)),
	}
	if value.Branch != nil {
		branch := observedHeadFromGateway(*value.Branch)
		result.Branch = &branch
	}
	for index, conflict := range value.Conflicts {
		result.Conflicts[index] = transportcap.Conflict{
			Coordinate: conflict.Coordinate, Base: conflict.Base, Local: conflict.Local, Remote: conflict.Remote,
		}
	}
	return result
}

var _ Remote = legacyGatewayRemote{}

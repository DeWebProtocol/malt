package add

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt-client/unixfs"
	"github.com/dewebprotocol/malt-core/mutation"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

type rootedMaterializer struct {
	GraphRemote
	authenticationReader
	authenticationWriter
	adapter *unixfs.AuthenticationAdapter
}

func newRootedMaterializer(remote Materializer) (*rootedMaterializer, error) {
	var graph GraphRemote = remote
	if wrapped, ok := remote.(*materializer); ok {
		graph = wrapped.GraphRemote
	}
	reader, ok := graph.(authenticationReader)
	if !ok {
		return nil, fmt.Errorf("rooted-v1 requires typed query transport")
	}
	writer, ok := graph.(authenticationWriter)
	if !ok {
		return nil, fmt.Errorf("rooted-v1 requires typed candidate transport")
	}
	adapter, err := unixfs.NewAuthenticationAdapter(writer, nil, maltcid.KZG4096)
	if err != nil {
		return nil, err
	}
	return &rootedMaterializer{GraphRemote: graph, authenticationReader: reader, authenticationWriter: writer, adapter: adapter}, nil
}
func (r *rootedMaterializer) CreateStagedRoot(ctx context.Context, bindings map[string]string) (cid.Cid, error) {
	return r.adapter.CreateStagedRoot(ctx, bindings)
}
func (r *rootedMaterializer) UpdateStagedRoot(ctx context.Context, base cid.Cid, bindings map[string]string) (cid.Cid, error) {
	return r.adapter.UpdateStagedRoot(ctx, base, bindings)
}
func (r *rootedMaterializer) CreateMeasuredPayload(ctx context.Context, chunks []cid.Cid, total, chunk uint64) (cid.Cid, error) {
	return r.adapter.CreateMeasuredPayload(ctx, chunks, total, chunk)
}
func (r *rootedMaterializer) CreateFixedListBaseRoot(ctx context.Context) (cid.Cid, error) {
	return r.adapter.CreateFixedListBaseRoot(ctx)
}
func (r *rootedMaterializer) ApplyFixedListPayloadMutation(ctx context.Context, mut mutation.SemanticMutation) (cid.Cid, error) {
	return r.adapter.ApplyFixedListPayloadMutation(ctx, mut)
}

type authenticationReader interface {
	Authenticate(context.Context, protocol.AuthenticationRequest) (*protocol.AuthenticationResult, error)
}
type authenticationWriter interface {
	AuthenticationCandidate(context.Context, cid.Cid) (*protocol.AuthenticationCandidate, error)
	MaterializeAuthentication(context.Context, protocol.AuthenticationCandidate) (cid.Cid, error)
}

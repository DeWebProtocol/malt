package server

import (
	"context"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/auth/proof/prooflist"
	listsemantic "github.com/dewebprotocol/malt/auth/semantic/list"
	mappingsemantic "github.com/dewebprotocol/malt/auth/semantic/mapping"
	"github.com/dewebprotocol/malt/graph"
	"github.com/dewebprotocol/malt/graph/querypath"
	"github.com/dewebprotocol/malt/graph/resolver"
	"github.com/dewebprotocol/malt/graph/resolver/step/explicit"
	"github.com/dewebprotocol/malt/graph/writer"
	cid "github.com/ipfs/go-cid"
)

type runtimeGraph interface {
	ID() string
	Namespace() string
	Resolver() graph.Resolver
	Writer() graph.Writer
	Semantic() mappingsemantic.Semantics
	ListSemantic() listsemantic.Semantics
}

type graphService struct {
	runtime runtimeGraph
}

func (s *Server) graphService(ctx context.Context) (graphService, error) {
	runtime, err := s.getOrCreateGraph(ctx)
	if err != nil {
		return graphService{}, err
	}
	return graphService{runtime: runtime}, nil
}

func (svc graphService) ApplyMutation(ctx context.Context, mut writer.SemanticMutation) (writer.WriteReceipt, error) {
	return svc.runtime.Writer().Apply(ctx, svc.runtime.Namespace(), mut)
}

func (svc graphService) CreateStructure(ctx context.Context, snapshot arcset.ArcSet) (cid.Cid, error) {
	return svc.runtime.Writer().CreateStructure(ctx, svc.runtime.Namespace(), snapshot)
}

func (svc graphService) ResolveKey(ctx context.Context, root cid.Cid, rawPath string) (string, *resolver.ResolveResult, error) {
	cleanPath, err := querypath.CanonicalizeQueryPath(rawPath)
	if err != nil {
		return "", nil, err
	}
	result, err := svc.runtime.Resolver().ResolveKey(ctx, root, cleanPath)
	if err != nil {
		return "", nil, err
	}
	if resolveMiss(root, cleanPath, result) || !result.Target.Defined() {
		return "", nil, errPathNotFound
	}
	return cleanPath, result, nil
}

func (svc graphService) ResolveMapPayload(ctx context.Context, root cid.Cid) (*resolver.ResolveResult, error) {
	result, err := svc.runtime.Resolver().ResolveKey(ctx, root, explicit.PayloadArc.String())
	if err != nil {
		return nil, err
	}
	if resolveMiss(root, explicit.PayloadArc.String(), result) || !result.Target.Defined() {
		return nil, errPathNotFound
	}
	return result, nil
}

func (svc graphService) ProofList(root cid.Cid, query string, transcript *resolver.Transcript) (*prooflist.ProofList, error) {
	pl, err := resolver.ProofListFromTranscript(root, transcript)
	if err != nil {
		return nil, err
	}
	pl.Query = query
	return pl, nil
}

package server

import (
	"context"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/auth/proof/prooflist"
	"github.com/dewebprotocol/malt/graph"
	"github.com/dewebprotocol/malt/graph/querypath"
	"github.com/dewebprotocol/malt/graph/resolver"
	"github.com/dewebprotocol/malt/graph/resolver/step/explicit"
	"github.com/dewebprotocol/malt/graph/writer"
	cid "github.com/ipfs/go-cid"
)

type graphService struct {
	runtime graph.Runtime
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

func (svc graphService) ResolveKey(root cid.Cid, rawPath string) (string, *resolver.ResolveResult, error) {
	cleanPath := querypath.CanonicalizeQueryPath(rawPath)
	result, err := svc.runtime.Resolver().ResolveKey(root, cleanPath)
	if err != nil {
		return "", nil, err
	}
	if resolveMiss(root, cleanPath, result) || !result.Target.Defined() {
		return "", nil, errPathNotFound
	}
	return cleanPath, result, nil
}

func (svc graphService) ResolveMapPayload(root cid.Cid) (*resolver.ResolveResult, error) {
	result, err := svc.runtime.Resolver().ResolveKey(root, explicit.PayloadArc.String())
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

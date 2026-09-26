package writeplan

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/dewebprotocol/malt-client/internal/cas/spool"
	"github.com/dewebprotocol/malt-core/protocol"
	cid "github.com/ipfs/go-cid"
)

type CandidateSource interface {
	AuthenticationCandidate(context.Context, cid.Cid) (*protocol.AuthenticationCandidate, error)
}
type BlockSource interface {
	Get(context.Context, cid.Cid) ([]byte, error)
}

// Staging supplies local write ports to existing layout compilers. Its
// read-through sources are untrusted; callers retain their normal verification.
// Close removes the private temporary spool after success or failure.
type Staging struct {
	directory   string
	local       *spool.Store
	source      CandidateSource
	blocks      BlockSource
	keys        []cid.Cid
	seen        map[string]bool
	candidates  []protocol.AuthenticationCandidate
	roots       map[string]int
	sourceRoots map[string]bool
}

func NewStaging(tempDir string, source CandidateSource, blocks BlockSource) (*Staging, error) {
	directory, err := os.MkdirTemp(tempDir, "malt-writeplan-")
	if err != nil {
		return nil, err
	}
	local, err := spool.Open(directory)
	if err != nil {
		_ = os.RemoveAll(directory)
		return nil, err
	}
	return &Staging{directory: directory, local: local, source: source, blocks: blocks, seen: map[string]bool{}, roots: map[string]int{}, sourceRoots: map[string]bool{}}, nil
}

func (s *Staging) Close() error { return os.RemoveAll(s.directory) }
func (s *Staging) Put(ctx context.Context, body []byte) (cid.Cid, error) {
	return s.PutWithCodec(ctx, body, cid.Raw)
}
func (s *Staging) PutWithCodec(ctx context.Context, body []byte, codec uint64) (cid.Cid, error) {
	key, err := s.local.PutWithCodec(ctx, body, codec)
	if err != nil {
		return cid.Undef, err
	}
	if !s.seen[key.KeyString()] {
		s.seen[key.KeyString()] = true
		s.keys = append(s.keys, key)
	}
	return key, nil
}
func (s *Staging) Get(ctx context.Context, key cid.Cid) ([]byte, error) {
	if s.seen[key.KeyString()] {
		return s.local.Get(ctx, key)
	}
	if s.blocks == nil {
		return nil, fmt.Errorf("planned block is unavailable")
	}
	return s.blocks.Get(ctx, key)
}
func cloneCandidate(candidate protocol.AuthenticationCandidate) (protocol.AuthenticationCandidate, error) {
	data, err := json.Marshal(candidate)
	if err != nil {
		return protocol.AuthenticationCandidate{}, err
	}
	var copy protocol.AuthenticationCandidate
	err = json.Unmarshal(data, &copy)
	return copy, err
}
func (s *Staging) AuthenticationCandidate(ctx context.Context, root cid.Cid) (*protocol.AuthenticationCandidate, error) {
	// A source-backed compiler requests installed before-images here. A newly
	// computed sibling with the same Root must not replace that source's lineage.
	// The caller still verifies this untrusted response before compiling an edit.
	if s.source != nil {
		candidate, err := s.source.AuthenticationCandidate(ctx, root)
		if err != nil {
			return nil, err
		}
		if candidate == nil || candidate.Root != root.String() {
			return nil, fmt.Errorf("source substituted planned before-image")
		}
		s.sourceRoots[root.String()] = true
		return candidate, nil
	}
	if index, ok := s.roots[root.String()]; ok {
		copy, err := cloneCandidate(s.candidates[index])
		return &copy, err
	}
	return nil, fmt.Errorf("planned candidate is unavailable")
}
func (s *Staging) MaterializeAuthentication(ctx context.Context, candidate protocol.AuthenticationCandidate) (cid.Cid, error) {
	if err := ctx.Err(); err != nil {
		return cid.Undef, err
	}
	if err := candidate.Validate(); err != nil {
		return cid.Undef, err
	}
	root := cid.MustParse(candidate.Root)
	if _, ok := s.roots[candidate.Root]; !ok {
		copy, err := cloneCandidate(candidate)
		if err != nil {
			return cid.Undef, err
		}
		s.roots[candidate.Root] = len(s.candidates)
		s.candidates = append(s.candidates, copy)
	}
	return root, nil
}
func (s *Staging) Plan(base, root cid.Cid) (Plan, error) {
	p := Plan{Base: base, Root: root}
	// Reusing an installed Root is a checkout, not a new Previous edge. Keep
	// only new candidates reachable from the final Root; this also drops
	// provisional states superseded during compilation.
	needed := map[string]bool{}
	var visit func(string)
	visit = func(key string) {
		if needed[key] || s.sourceRoots[key] {
			return
		}
		index, ok := s.roots[key]
		if !ok {
			return
		}
		needed[key] = true
		candidate := s.candidates[index]
		visit(candidate.Previous)
		for _, entry := range candidate.State.Entries {
			visit(entry.Target.String())
		}
	}
	visit(root.String())
	for _, candidate := range s.candidates {
		if !needed[candidate.Root] {
			continue
		}
		copy, err := cloneCandidate(candidate)
		if err != nil {
			return Plan{}, err
		}
		p.Candidates = append(p.Candidates, copy)
	}
	// The final candidate follows its dependencies. An unchanged operation may
	// select an existing Root without producing any candidate.
	if len(p.Candidates) > 0 && p.Candidates[len(p.Candidates)-1].Root != root.String() {
		return Plan{}, fmt.Errorf("final Root is not the terminal prepared candidate")
	}
	for _, key := range s.keys {
		p.Blocks = append(p.Blocks, Block{CID: key, Read: func(ctx context.Context) ([]byte, error) { return s.local.Get(ctx, key) }})
	}
	return p, p.validate()
}

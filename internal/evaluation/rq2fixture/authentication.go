package rq2fixture

import (
	"context"
	"fmt"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	"github.com/dewebprotocol/malt-client/internal/evaluation/authenticationgraph"
	"github.com/dewebprotocol/malt-core/auth/coordinate"
	"github.com/dewebprotocol/malt-core/derivation"
	"github.com/dewebprotocol/malt-core/engine"
	"github.com/dewebprotocol/malt-core/maltcid"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	cid "github.com/ipfs/go-cid"
)

// Candidates builds measured Positional file objects before their Prefix AA1
// directory root. Source paths are explicit opaque labels at the Core boundary.
func (s *SourceDefinition) Candidates(ctx context.Context, e *engine.Engine, backend string) ([]protocol.AuthenticationCandidate, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	profile, err := BackendProfile(backend)
	if err != nil {
		return nil, err
	}
	candidates := make([]protocol.AuthenticationCandidate, 0, len(s.ListFiles)+1)
	seen := map[string]bool{}
	bindings := make([]engine.Entry, 0, len(s.DirectFiles)+len(s.ListFiles))
	for _, file := range s.DirectFiles {
		target, err := clientcas.CIDForBlock(clientcas.Block{Codec: cid.Raw, Data: file.Bytes})
		if err != nil {
			return nil, err
		}
		bindings = append(bindings, engine.Entry{Label: []byte(file.Path), Target: target})
	}
	for _, file := range s.ListFiles {
		state := engine.State{Descriptor: maltcid.RootDescriptor{DerivationProfile: uint8(derivation.Direct), Layout: maltcid.Positional, Profile: profile}, ChunkSize: file.ChunkSize, TotalSize: file.TotalSize, Entries: make([]engine.Entry, len(file.Chunks))}
		for i, chunk := range file.Chunks {
			target, err := clientcas.CIDForBlock(clientcas.Block{Codec: cid.Raw, Data: chunk.Bytes})
			if err != nil {
				return nil, err
			}
			state.Entries[i] = engine.Entry{Label: coordinate.EncodeIndex(chunk.Index), Target: target}
		}
		candidate, err := authentication.Prepare(ctx, e, state)
		if err != nil {
			return nil, err
		}
		if !seen[candidate.Root] {
			candidates = append(candidates, candidate)
			seen[candidate.Root] = true
		}
		target, _ := cid.Parse(candidate.Root)
		bindings = append(bindings, engine.Entry{Label: []byte(file.Path), Target: target})
	}
	candidate, err := authentication.Prepare(ctx, e, engine.State{Descriptor: maltcid.RootDescriptor{Layout: maltcid.Prefix, DerivationProfile: uint8(derivation.SHA256), Profile: profile}, Entries: bindings})
	if err != nil {
		return nil, err
	}
	return append(candidates, candidate), nil
}

// Candidates independently reconstructs the fixture's exact pinned Root and
// checks the complete source oracle before returning bootstrap candidates.
func (f *Fixture) Candidates(ctx context.Context, e *engine.Engine, backend string) ([]protocol.AuthenticationCandidate, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	source := SourceDefinition{SchemaVersion: SourceSchemaVersion, FixtureID: f.FixtureID, MutationSeedSHA256: f.MutationSeedSHA256, Operations: f.Operations}
	for _, file := range f.DirectFiles {
		source.DirectFiles = append(source.DirectFiles, SourceDirectFile{Path: file.Path, Coordinate: file.Coordinate, Bytes: file.Bytes})
	}
	for _, file := range f.ListFiles {
		out := SourceListFile{Path: file.Path, Coordinate: file.Coordinate, ChunkSize: file.ChunkSize, TotalSize: file.TotalSize}
		for _, chunk := range file.Chunks {
			out.Chunks = append(out.Chunks, SourceListChunk{Index: chunk.Index, Bytes: chunk.Bytes})
		}
		source.ListFiles = append(source.ListFiles, out)
	}
	candidates, err := source.Candidates(ctx, e, backend)
	if err != nil {
		return nil, err
	}
	root, _ := cid.Parse(candidates[len(candidates)-1].Root)
	view := authenticationgraph.View{Root: root, States: make(map[string]engine.State, len(candidates))}
	for _, candidate := range candidates {
		view.States[candidate.Root] = candidate.State
	}
	if err := f.ValidateInitialGraph(view, backend); err != nil {
		return nil, fmt.Errorf("reconstructed fixture: %w", err)
	}
	return candidates, nil
}

package unixfs

import (
	"context"
	"fmt"
	"sort"

	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
	"github.com/dewebprotocol/malt-core/auth/commitment/ipa"
	"github.com/dewebprotocol/malt-core/auth/commitment/kzg"
	"github.com/dewebprotocol/malt-core/auth/engine"
	"github.com/dewebprotocol/malt-core/auth/input"
	"github.com/dewebprotocol/malt-core/mutation"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

// AuthenticationAdapter compiles the rooted-v1 UnixFS schema to typed ArcSets.
// Directory payloads are system bindings; names are application labels. File
// block sequences carry measured structural metadata and no system payload.
// It computes candidates locally and checks the exact remote receipt.
type AuthenticationAdapter struct {
	remote  transportcap.AuthenticationWriter
	engine  *engine.Engine
	profile maltcid.ProfileID
}

func NewAuthenticationAdapter(remote transportcap.AuthenticationWriter, e *engine.Engine, profile maltcid.ProfileID) (*AuthenticationAdapter, error) {
	if remote == nil {
		return nil, fmt.Errorf("authentication writer is nil")
	}
	if e == nil {
		if profile == 0 {
			profile = maltcid.KZG4096
		}
		if profile != maltcid.KZG4096 {
			return nil, fmt.Errorf("non-default backend requires an injected engine")
		}
		scheme, err := kzg.NewScheme()
		if err != nil {
			return nil, err
		}
		profiles := engine.NewRegistry()
		if err := profiles.Register(scheme); err != nil {
			return nil, err
		}
		i, err := ipa.NewCommitterScheme(ipa.ProfileCompact)
		if err != nil {
			return nil, err
		}
		if err := profiles.Register(i); err != nil {
			return nil, err
		}
		e = engine.New(input.DefaultRegistry(), profiles)
	}
	if _, err := maltcid.Profile(profile); err != nil {
		return nil, err
	}
	return &AuthenticationAdapter{remote: remote, engine: e, profile: profile}, nil
}
func (a *AuthenticationAdapter) CreateStagedRoot(ctx context.Context, bindings map[string]string) (cid.Cid, error) {
	return a.UpdateStagedRoot(ctx, cid.Undef, bindings)
}
func (a *AuthenticationAdapter) UpdateStagedRoot(ctx context.Context, previous cid.Cid, bindings map[string]string) (cid.Cid, error) {
	state := engine.State{Descriptor: maltcid.RootDescriptor{Layout: maltcid.Prefix, InputRule: uint8(input.UnixFSNameSHA256), Profile: a.profile}, Entries: []engine.Entry{}}
	names := make([]string, 0, len(bindings))
	for name := range bindings {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		target, err := cid.Decode(bindings[name])
		if err != nil {
			return cid.Undef, err
		}
		selector := input.LabelValue([]byte(name))
		if name == "@payload" {
			selector = input.SystemValue(input.Payload)
		} else {
			parts, err := ParseCanonicalStagedPath(name)
			if err != nil || len(parts) != 1 || parts[0] != name {
				return cid.Undef, fmt.Errorf("rooted-v1 requires one canonical directory component: %q", name)
			}
		}
		state.Entries = append(state.Entries, engine.Entry{Input: selector, Target: target})
	}
	return a.materialize(ctx, previous, state)
}
func (a *AuthenticationAdapter) materialize(ctx context.Context, previous cid.Cid, state engine.State) (cid.Cid, error) {
	var candidate protocol.AuthenticationCandidate
	var err error
	if previous.Defined() {
		base, loadErr := a.remote.AuthenticationCandidate(ctx, previous)
		if loadErr != nil {
			return cid.Undef, loadErr
		}
		if base == nil {
			return cid.Undef, fmt.Errorf("nil authentication candidate")
		}
		returned, parseErr := cid.Decode(base.Root)
		if parseErr != nil || !returned.Equals(previous) {
			return cid.Undef, fmt.Errorf("writer base changed selected Root")
		}
		state.Descriptor.Profile = base.State.Descriptor.Profile
		candidate, err = authentication.PrepareUpdate(ctx, a.engine, *base, state)
	} else {
		candidate, err = authentication.Prepare(ctx, a.engine, state)
	}
	if err != nil {
		return cid.Undef, err
	}
	expected, err := cid.Decode(candidate.Root)
	if err != nil {
		return cid.Undef, err
	}
	got, err := a.remote.MaterializeAuthentication(ctx, candidate)
	if err != nil {
		return cid.Undef, err
	}
	if !got.Equals(expected) {
		return cid.Undef, fmt.Errorf("authentication receipt changed candidate Root")
	}
	return expected, nil
}
func (a *AuthenticationAdapter) CreateMeasuredPayload(ctx context.Context, chunks []cid.Cid, total, chunkSize uint64) (cid.Cid, error) {
	state := engine.State{Descriptor: maltcid.RootDescriptor{Layout: maltcid.Positional, Profile: a.profile}, ChunkSize: chunkSize, TotalSize: total, Entries: make([]engine.Entry, len(chunks))}
	for i, target := range chunks {
		state.Entries[i] = engine.Entry{Input: input.IndexValue(uint64(i)), Target: target}
	}
	return a.materialize(ctx, cid.Undef, state)
}

// These compatibility methods are never used by the typed sequence capability.
func (a *AuthenticationAdapter) CreateFixedListBaseRoot(context.Context) (cid.Cid, error) {
	return cid.Undef, fmt.Errorf("typed sequence requires CreateMeasuredPayload")
}
func (a *AuthenticationAdapter) ApplyFixedListPayloadMutation(context.Context, mutation.SemanticMutation) (cid.Cid, error) {
	return cid.Undef, fmt.Errorf("typed sequence does not accept compatibility mutations")
}

package clientroot

import (
	"context"
	"fmt"

	"github.com/dewebprotocol/malt-client/journal"
	"github.com/dewebprotocol/malt-client/unixfs"
	unixfsmodel "github.com/dewebprotocol/malt-client/unixfs/model"
	"github.com/dewebprotocol/malt-core/auth/engine"
	"github.com/dewebprotocol/malt-core/auth/input"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

// AuthenticationPlanner projects rooted-v1 filesystem changes to complete
// typed candidates. It reads and verifies every before-image locally, emits
// children before parents, and never persists or accepts candidate Roots.
type AuthenticationPlanner struct {
	blocks BlockStore
	remote CandidateSource
	engine *engine.Engine
}

func NewAuthentication(blocks BlockStore, remote CandidateSource, e *engine.Engine) (*AuthenticationPlanner, error) {
	if blocks == nil || remote == nil || e == nil {
		return nil, fmt.Errorf("typed planner capabilities are required")
	}
	return &AuthenticationPlanner{blocks, remote, e}, nil
}
func (p *AuthenticationPlanner) PlanRooted(ctx context.Context, base cid.Cid, operations []journal.Operation) ([]protocol.AuthenticationCandidate, []cid.Cid, cid.Cid, error) {
	if err := validateOperations(base, operations); err != nil {
		return nil, nil, cid.Undef, err
	}
	legacy := &Planner{blocks: p.blocks}
	bases := map[string]protocol.AuthenticationCandidate{}
	visiting := map[string]bool{}
	objects, entries := 0, 0
	var load func(cid.Cid, int) (*treeNode, error)
	load = func(root cid.Cid, depth int) (*treeNode, error) {
		objects++
		if objects > 4096 || depth > 256 {
			return nil, fmt.Errorf("rooted directory traversal bound exceeded")
		}
		if visiting[root.KeyString()] {
			return nil, fmt.Errorf("cyclic rooted directory")
		}
		visiting[root.KeyString()] = true
		defer delete(visiting, root.KeyString())
		candidate, err := p.remote.AuthenticationCandidate(ctx, root)
		if err != nil {
			return nil, err
		}
		if candidate == nil {
			return nil, fmt.Errorf("nil directory candidate")
		}
		got, err := cid.Decode(candidate.Root)
		if err != nil || !got.Equals(root) {
			return nil, fmt.Errorf("directory source substituted selected Root")
		}
		entries += len(candidate.State.Entries)
		if entries > 65536 {
			return nil, fmt.Errorf("rooted entry bound exceeded")
		}
		d := candidate.State.Descriptor
		if d.Layout != maltcid.Prefix || d.InputRule != uint8(input.UnixFSNameSHA256) {
			return nil, fmt.Errorf("directory does not use rooted-v1 schema")
		}
		if err := authentication.ValidateCandidate(ctx, p.engine, *candidate); err != nil {
			return nil, err
		}
		bases[root.KeyString()] = *candidate
		targets := map[string]cid.Cid{}
		payload := cid.Undef
		for _, binding := range candidate.State.Entries {
			switch binding.Input.Kind {
			case input.System:
				if binding.Input.Number != input.Payload || payload.Defined() {
					return nil, fmt.Errorf("unexpected directory system binding")
				}
				payload = binding.Target
			case input.Label:
				name := string(binding.Input.Data)
				parts, err := unixfs.ParseCanonicalStagedPath(name)
				if err != nil || len(parts) != 1 || parts[0] != name {
					return nil, fmt.Errorf("invalid directory component")
				}
				targets[name] = binding.Target
			default:
				return nil, fmt.Errorf("unexpected directory selector")
			}
		}
		manifest, err := legacy.readManifest(ctx, payload)
		if err != nil {
			return nil, err
		}
		if len(manifest.Entries) != len(targets) {
			return nil, fmt.Errorf("directory manifest and ArcSet differ")
		}
		node := &treeNode{kind: unixfsmodel.DirectoryEntryTypeDir, key: root, manifest: payload, children: map[string]*treeNode{}}
		for _, entry := range manifest.Entries {
			target, ok := targets[entry.Name]
			if !ok {
				return nil, fmt.Errorf("manifest child missing")
			}
			if entry.Type == unixfsmodel.DirectoryEntryTypeDir {
				child, err := load(target, depth+1)
				if err != nil {
					return nil, err
				}
				node.children[entry.Name] = child
			} else if entry.Type == unixfsmodel.DirectoryEntryTypeFile {
				node.children[entry.Name] = &treeNode{kind: entry.Type, key: target}
			} else {
				return nil, fmt.Errorf("unsupported directory entry type")
			}
		}
		return node, nil
	}
	tree, err := load(base, 0)
	if err != nil {
		return nil, nil, cid.Undef, err
	}
	if err := applyOperations(tree, operations); err != nil {
		return nil, nil, cid.Undef, err
	}
	descriptor := bases[base.KeyString()].State.Descriptor
	candidates := []protocol.AuthenticationCandidate{}
	required := []cid.Cid{}
	var build func(*treeNode) error
	build = func(node *treeNode) error {
		if node.kind != unixfsmodel.DirectoryEntryTypeDir {
			if node.key.Prefix().Codec == cid.Raw {
				required = append(required, node.key)
			}
			return nil
		}
		for _, name := range sortedChildNames(node) {
			if err := build(node.children[name]); err != nil {
				return err
			}
		}
		if !node.dirty {
			return nil
		}
		if err := legacy.storeManifest(ctx, node); err != nil {
			return err
		}
		state := engine.State{Descriptor: descriptor, Entries: []engine.Entry{{Input: input.SystemValue(input.Payload), Target: node.manifest}}}
		for _, name := range sortedChildNames(node) {
			state.Entries = append(state.Entries, engine.Entry{Input: input.LabelValue([]byte(name)), Target: node.children[name].key})
		}
		var candidate protocol.AuthenticationCandidate
		var err error
		if old, ok := bases[node.key.KeyString()]; ok {
			state.Descriptor = old.State.Descriptor
			candidate, err = authentication.PrepareUpdate(ctx, p.engine, old, state)
		} else {
			candidate, err = authentication.Prepare(ctx, p.engine, state)
		}
		if err != nil {
			return err
		}
		root, err := cid.Decode(candidate.Root)
		if err != nil {
			return err
		}
		if !root.Equals(node.key) {
			candidates = append(candidates, candidate)
		}
		node.key = root
		return nil
	}
	if err := build(tree); err != nil {
		return nil, nil, cid.Undef, err
	}
	return candidates, required, tree.key, nil
}

// CandidateSource supplies untrusted complete directory state; the planner
// verifies it against the selected Root before applying filesystem intent.
type CandidateSource interface {
	AuthenticationCandidate(context.Context, cid.Cid) (*protocol.AuthenticationCandidate, error)
}

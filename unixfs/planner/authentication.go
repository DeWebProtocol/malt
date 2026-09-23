package planner

import (
	"context"
	"fmt"
	"strings"

	"github.com/dewebprotocol/malt-client/journal"
	"github.com/dewebprotocol/malt-client/unixfs"
	unixfsmodel "github.com/dewebprotocol/malt-client/unixfs/model"
	"github.com/dewebprotocol/malt-core/derivation"
	"github.com/dewebprotocol/malt-core/engine"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

// Planner projects filesystem changes in the selected UnixFS layout to complete
// typed candidates. It reads and verifies every before-image locally, emits
// children before parents, and never persists or accepts candidate Roots.
type Planner struct {
	layout unixfs.LayoutKind
	blocks BlockStore
	remote CandidateSource
	engine *engine.Engine
}

func New(layout unixfs.LayoutKind, blocks BlockStore, remote CandidateSource, e *engine.Engine) (*Planner, error) {
	if _, err := unixfs.NewLayout(layout); err != nil {
		return nil, err
	}
	if blocks == nil || remote == nil || e == nil {
		return nil, fmt.Errorf("typed planner capabilities are required")
	}
	return &Planner{layout, blocks, remote, e}, nil
}
func (p *Planner) Plan(ctx context.Context, base cid.Cid, operations []journal.Operation) ([]protocol.AuthenticationCandidate, []cid.Cid, cid.Cid, error) {
	if p == nil || p.blocks == nil || p.remote == nil || p.engine == nil || ctx == nil {
		return nil, nil, cid.Undef, fmt.Errorf("typed planner and context are required")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, cid.Undef, err
	}
	if err := validateOperations(base, operations); err != nil {
		return nil, nil, cid.Undef, err
	}
	manifests := &manifestStore{blocks: p.blocks}
	bases := map[string]protocol.AuthenticationCandidate{}
	visiting := map[string]bool{}
	objects, entries := 0, 0
	var load func(cid.Cid, int) (*treeNode, error)
	load = func(root cid.Cid, depth int) (*treeNode, error) {
		objects++
		if objects > 4096 || depth > 256 {
			return nil, fmt.Errorf("directory traversal bound exceeded")
		}
		if visiting[root.KeyString()] {
			return nil, fmt.Errorf("cyclic directory")
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
			return nil, fmt.Errorf("directory entry bound exceeded")
		}
		d := candidate.State.Descriptor
		rule := uint8(derivation.SHA256)
		if d.Layout != maltcid.Prefix || d.DerivationProfile != rule {
			return nil, fmt.Errorf("directory input rule differs from selected layout")
		}
		if err := authentication.ValidateCandidate(ctx, p.engine, *candidate); err != nil {
			return nil, err
		}
		bases[root.KeyString()] = *candidate
		targets := map[string]cid.Cid{}
		payload := cid.Undef
		for _, binding := range candidate.State.Entries {
			name := string(binding.Label)
			if name == "@payload" {
				if payload.Defined() {
					return nil, fmt.Errorf("duplicate directory payload label")
				}
				payload = binding.Target
				continue
			}
			parts, err := unixfs.ParseCanonicalStagedPath(name)
			if err != nil || len(parts) == 0 || strings.Join(parts, "/") != name || (p.layout == unixfs.LayoutRootedV1 && len(parts) != 1) {
				return nil, fmt.Errorf("invalid directory label")
			}
			targets[name] = binding.Target
		}
		if p.layout == unixfs.LayoutFlatV1 {
			node := &treeNode{kind: unixfsmodel.DirectoryEntryTypeDir, key: root, manifest: payload, children: map[string]*treeNode{}}
			if err := loadFlatDirectory(ctx, manifests, node, "", targets, depth, &objects); err != nil {
				return nil, err
			}
			if err := requireAuthenticationBindings(node, p.layout, targets); err != nil {
				return nil, err
			}
			return node, nil
		}
		manifest, err := manifests.readManifest(ctx, payload)
		if err != nil {
			return nil, err
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
		if err := requireAuthenticationBindings(node, p.layout, targets); err != nil {
			return nil, err
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
	produced := map[string]bool{}
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
		if err := manifests.storeManifest(ctx, node); err != nil {
			return err
		}
		if p.layout == unixfs.LayoutFlatV1 && node != tree {
			node.key = node.manifest
			return nil
		}
		state := engine.State{Descriptor: descriptor, Entries: authenticationEntries(node, p.layout)}
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
		if persisted, ok := bases[root.KeyString()]; ok {
			// Reuse a verified before-image instead of adding a new lineage edge.
			// Swapping sibling states must not emit B previous=A and A previous=B.
			// A changed final Root still needs its original candidate in the batch
			// so the durable receipt names the exact selected Root.
			if node == tree && !root.Equals(base) {
				candidates = append(candidates, persisted)
			}
		} else if !produced[root.KeyString()] {
			candidates = append(candidates, candidate)
			produced[root.KeyString()] = true
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

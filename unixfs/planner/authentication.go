package planner

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/dewebprotocol/malt-client/journal"
	"github.com/dewebprotocol/malt-client/unixfs"
	unixfsmodel "github.com/dewebprotocol/malt-client/unixfs/model"
	"github.com/dewebprotocol/malt-client/writeplan"
	"github.com/dewebprotocol/malt-core/derivation"
	"github.com/dewebprotocol/malt-core/engine"
	"github.com/dewebprotocol/malt-core/maltcid"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	cid "github.com/ipfs/go-cid"
)

// Planner projects filesystem changes in the selected UnixFS layout to complete
// typed candidates. It reads and verifies affected before-images locally, emits
// children before parents, and never persists or accepts candidate Roots.
type Planner struct {
	layout  unixfs.LayoutKind
	blocks  BlockStore
	remote  CandidateSource
	engine  *engine.Engine
	mu      sync.Mutex
	writers writerCache
}

func New(layout unixfs.LayoutKind, blocks BlockStore, remote CandidateSource, e *engine.Engine) (*Planner, error) {
	if _, err := unixfs.NewLayout(layout); err != nil {
		return nil, err
	}
	if blocks == nil || remote == nil || e == nil {
		return nil, fmt.Errorf("typed planner capabilities are required")
	}
	return &Planner{layout: layout, blocks: blocks, remote: remote, engine: e}, nil
}

// Prepare performs only local computation and reads. Manifest uploads are part
// of the returned plan, after all filesystem intent has passed validation.
func (p *Planner) Prepare(ctx context.Context, base cid.Cid, operations []journal.Operation) (writeplan.Plan, error) {
	if p == nil || ctx == nil {
		return writeplan.Plan{}, fmt.Errorf("typed planner and context are required")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return writeplan.Plan{}, err
	}
	if err := validateOperations(base, operations); err != nil {
		return writeplan.Plan{}, err
	}
	manifests := &manifestStore{blocks: p.blocks}
	bases := map[string]*authentication.Writer{}
	knownRoots := map[string]bool{base.KeyString(): true}
	objects, entries := 0, 0
	var load func(*treeNode, int, map[string]bool) error
	load = func(node *treeNode, depth int, ancestors map[string]bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		objects++
		if objects > 4096 || depth > 256 {
			return fmt.Errorf("directory traversal bound exceeded")
		}
		if ancestors[node.key.KeyString()] {
			return fmt.Errorf("cyclic directory")
		}
		lineage := make(map[string]bool, len(ancestors)+1)
		for k := range ancestors {
			lineage[k] = true
		}
		lineage[node.key.KeyString()] = true
		targets := node.retained
		if p.layout != unixfs.LayoutFlatV1 || node.key.Equals(base) {
			writer, err := p.loadWriter(ctx, node.key)
			if err != nil {
				return err
			}
			bases[node.key.KeyString()] = writer
			state, err := writer.State(ctx)
			if err != nil {
				return err
			}
			if state.Descriptor.Layout != maltcid.Prefix || state.Descriptor.DerivationProfile != uint8(derivation.SHA256) {
				return fmt.Errorf("directory input rule differs from selected layout")
			}
			entries += len(state.Entries)
			if entries > 65536 {
				return fmt.Errorf("directory entry bound exceeded")
			}
			targets = map[string]cid.Cid{}
			for _, binding := range state.Entries {
				name := string(binding.Label)
				if name == "@payload" {
					node.manifest = binding.Target
					continue
				}
				parts, err := unixfs.ParseCanonicalStagedPath(name)
				if err != nil || len(parts) == 0 || strings.Join(parts, "/") != name || (p.layout == unixfs.LayoutRootedV1 && len(parts) != 1) {
					return fmt.Errorf("invalid directory label")
				}
				targets[name] = binding.Target
			}
			// Hybrid redundant descendant labels must agree when a child is opened.
			if node.retained != nil && p.layout == unixfs.LayoutHybridV1 {
				if err := equalBindings(node.retained, targets); err != nil {
					return err
				}
			}
		}
		manifest, err := manifests.readManifest(ctx, node.manifest)
		if err != nil {
			return err
		}
		node.children = map[string]*treeNode{}
		for _, entry := range manifest.Entries {
			target, ok := targets[entry.Name]
			if !ok {
				return fmt.Errorf("manifest child missing")
			}
			child := &treeNode{kind: entry.Type, key: target}
			switch entry.Type {
			case unixfsmodel.DirectoryEntryTypeDir:
				if p.layout == unixfs.LayoutFlatV1 {
					if !unixfsmodel.IsDirectoryManifestCID(target) {
						return fmt.Errorf("flat directory must target its manifest")
					}
					child.manifest = target
				} else {
					descriptor, _, err := maltcid.ParseRoot(target)
					if err != nil || descriptor.Layout != maltcid.Prefix || descriptor.DerivationProfile != uint8(derivation.SHA256) {
						return fmt.Errorf("directory target differs from selected layout")
					}
					knownRoots[target.KeyString()] = true
				}
				if p.layout != unixfs.LayoutRootedV1 {
					child.retained = map[string]cid.Cid{}
					prefix := entry.Name + "/"
					for label, value := range targets {
						if strings.HasPrefix(label, prefix) {
							child.retained[strings.TrimPrefix(label, prefix)] = value
						}
					}
				}
				child.load = func() error { return load(child, depth+1, lineage) }
			case unixfsmodel.DirectoryEntryTypeFile:
			default:
				return fmt.Errorf("unsupported directory entry type")
			}
			node.children[entry.Name] = child
		}
		// Verify immediate manifest coverage, including stray descendants below a
		// file, before retaining the immutable projection for untouched subtrees.
		old := node.retained
		node.retained = nil
		err = requireAuthenticationBindings(node, p.layout, targets)
		node.retained = old
		if err != nil {
			return err
		}
		node.retained = targets
		return nil
	}
	tree := &treeNode{kind: unixfsmodel.DirectoryEntryTypeDir, key: base}
	if err := load(tree, 0, map[string]bool{}); err != nil {
		return writeplan.Plan{}, err
	}
	if err := applyOperations(tree, operations); err != nil {
		return writeplan.Plan{}, err
	}
	initial, err := bases[base.KeyString()].State(ctx)
	if err != nil {
		return writeplan.Plan{}, err
	}
	plan := writeplan.Plan{Base: base}
	produced := map[string]bool{}
	var build func(*treeNode) error
	build = func(node *treeNode) error {
		if node.kind != unixfsmodel.DirectoryEntryTypeDir {
			if node.key.Prefix().Codec == cid.Raw {
				plan.Required = append(plan.Required, node.key)
			}
			return nil
		}
		if !node.dirty {
			return nil
		}
		for _, name := range sortedChildNames(node) {
			if err := build(node.children[name]); err != nil {
				return err
			}
		}
		if err := manifests.storeManifest(ctx, node); err != nil {
			return err
		}
		if p.layout == unixfs.LayoutFlatV1 && node != tree {
			node.key = node.manifest
			return nil
		}
		state := engine.State{Descriptor: initial.Descriptor, Entries: authenticationEntries(node, p.layout)}
		var writer *authentication.Writer
		var err error
		if old := bases[node.key.KeyString()]; old != nil {
			before, stateErr := old.State(ctx)
			if stateErr != nil {
				return stateErr
			}
			state.Descriptor = before.Descriptor
			writer, err = old.Update(ctx, state)
		} else {
			writer, err = authentication.BuildWriter(ctx, p.engine, state)
		}
		if err != nil {
			return err
		}
		root := writer.Root()
		persisted := bases[root.KeyString()]
		if persisted == nil && knownRoots[root.KeyString()] {
			persisted, err = p.loadWriter(ctx, root)
			if err != nil {
				return err
			}
			bases[root.KeyString()] = persisted
		}
		if persisted != nil {
			if node == tree && !root.Equals(base) {
				candidate, err := persisted.Export(ctx)
				if err != nil {
					return err
				}
				plan.Candidates = append(plan.Candidates, candidate)
			}
		} else if !produced[root.KeyString()] {
			candidate, err := writer.Export(ctx)
			if err != nil {
				return err
			}
			plan.Candidates = append(plan.Candidates, candidate)
			produced[root.KeyString()] = true
		}
		node.key = root
		return nil
	}
	if err := build(tree); err != nil {
		return writeplan.Plan{}, err
	}
	plan.Root, plan.Blocks = tree.key, manifests.planned
	return plan, nil
}

func equalBindings(want, got map[string]cid.Cid) error {
	if len(want) != len(got) {
		return fmt.Errorf("directory descendant projections differ")
	}
	for label, value := range want {
		if !got[label].Equals(value) {
			return fmt.Errorf("directory descendant projection differs at %q", label)
		}
	}
	return nil
}

// CandidateSource supplies untrusted complete directory state; the planner
// verifies it against the selected Root before applying filesystem intent.
type CandidateSource interface {
	AuthenticationCandidate(context.Context, cid.Cid) (*protocol.AuthenticationCandidate, error)
}

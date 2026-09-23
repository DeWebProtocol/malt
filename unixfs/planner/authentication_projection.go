package planner

import (
	"fmt"
	"slices"

	"github.com/dewebprotocol/malt-client/unixfs"
	model "github.com/dewebprotocol/malt-client/unixfs/model"
	"github.com/dewebprotocol/malt-core/engine"
	cid "github.com/ipfs/go-cid"
)

// authenticationBindings is the application projection, before input hashing.
// Flat directories bind manifest CIDs; hybrid directories bind child Roots and
// include descendant labels. Rooted directories bind immediate children only.
func authenticationBindings(node *treeNode, layout unixfs.LayoutKind) map[string]cid.Cid {
	out := map[string]cid.Cid{}
	var collect func(*treeNode, string)
	collect = func(parent *treeNode, prefix string) {
		for _, name := range sortedChildNames(parent) {
			child := parent.children[name]
			label := prefix + name
			target := child.key
			if layout == unixfs.LayoutFlatV1 && child.kind == model.DirectoryEntryTypeDir {
				target = child.manifest
			}
			out[label] = target
			if layout != unixfs.LayoutRootedV1 && child.kind == model.DirectoryEntryTypeDir {
				collect(child, label+"/")
			}
		}
	}
	collect(node, "")
	return out
}

func authenticationEntries(node *treeNode, layout unixfs.LayoutKind) []engine.Entry {
	entries := []engine.Entry{{Label: []byte("@payload"), Target: node.manifest}}
	bindings := authenticationBindings(node, layout)
	names := make([]string, 0, len(bindings))
	for name := range bindings {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		entries = append(entries, engine.Entry{Label: []byte(name), Target: bindings[name]})
	}
	return entries
}

func requireAuthenticationBindings(node *treeNode, layout unixfs.LayoutKind, actual map[string]cid.Cid) error {
	expected := authenticationBindings(node, layout)
	if len(expected) != len(actual) {
		return fmt.Errorf("directory manifest and bindings differ")
	}
	for name, target := range expected {
		got, ok := actual[name]
		if !ok || !got.Equals(target) {
			return fmt.Errorf("directory projection differs at %q", name)
		}
	}
	return nil
}

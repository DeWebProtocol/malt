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
	children := make([]unixfs.DirectoryChild, 0, len(node.children))
	for _, name := range sortedChildNames(node) {
		child := node.children[name]
		projected := unixfs.DirectoryChild{
			Name: name, Directory: child.kind == model.DirectoryEntryTypeDir,
			Root: child.key, Manifest: child.manifest,
		}
		if projected.Directory && layout != unixfs.LayoutRootedV1 {
			projected.Descendants = authenticationBindings(child, layout)
		}
		children = append(children, projected)
	}
	return unixfs.ProjectDirectoryBindings(layout, children)
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

package unixfs

import cid "github.com/ipfs/go-cid"

// DirectoryChild describes an already materialized child. Manifest is the
// immutable directory payload; Root is the authenticated object (or file).
// Descendants contains child-relative bindings without @payload.
type DirectoryChild struct {
	Name        string
	Directory   bool
	Root        cid.Cid
	Manifest    cid.Cid
	Descendants map[string]cid.Cid
}

// ProjectDirectoryBindings is the shared application projection used by both
// staged materialization and typed mutation planning. Callers validate names,
// targets, and layout before constructing the projection.
func ProjectDirectoryBindings(layout LayoutKind, children []DirectoryChild) map[string]cid.Cid {
	bindings := make(map[string]cid.Cid, len(children))
	for _, child := range children {
		target := child.Root
		if layout == LayoutFlatV1 && child.Directory {
			target = child.Manifest
		}
		bindings[child.Name] = target
		if layout != LayoutRootedV1 && child.Directory {
			for name, target := range child.Descendants {
				bindings[child.Name+"/"+name] = target
			}
		}
	}
	return bindings
}

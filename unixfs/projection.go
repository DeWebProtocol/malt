package unixfs

import (
	"strings"

	model "github.com/dewebprotocol/malt-client/unixfs/model"
	cid "github.com/ipfs/go-cid"
)

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
		name := child.Name
		if layout == LayoutFlatV1 {
			label, _ := model.FlatPathLabel(name)
			name = string(label)
		}
		bindings[name] = target
		if layout != LayoutRootedV1 && child.Directory {
			for name, target := range child.Descendants {
				if layout == LayoutFlatV1 {
					name = strings.TrimPrefix(name, "/")
				}
				label := child.Name + "/" + name
				if layout == LayoutFlatV1 {
					encoded, _ := model.FlatPathLabel(label)
					label = string(encoded)
				}
				bindings[label] = target
			}
		}
	}
	return bindings
}

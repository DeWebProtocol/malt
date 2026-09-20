package planner

import (
	"context"
	"fmt"

	model "github.com/dewebprotocol/malt-client/unixfs/model"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
)

// loadFlatDirectory reads CID-checked manifests and binds every child to one
// explicit label in the already-verified top-level authentication state.
func loadFlatDirectory(ctx context.Context, manifests *manifestStore, node *treeNode, prefix string, targets map[string]cid.Cid, depth int, objects *int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > 256 {
		return fmt.Errorf("flat directory depth exceeded")
	}
	manifest, err := manifests.readManifest(ctx, node.manifest)
	if err != nil {
		return err
	}
	for _, entry := range manifest.Entries {
		label := prefix + entry.Name
		target, ok := targets[label]
		if !ok {
			return fmt.Errorf("flat manifest child %q is missing", label)
		}
		child := &treeNode{kind: entry.Type, key: target}
		switch entry.Type {
		case model.DirectoryEntryTypeDir:
			if maltcid.IsMaltCid(target) {
				return fmt.Errorf("flat directory %q must target its manifest", label)
			}
			*objects++
			if *objects > 4096 {
				return fmt.Errorf("flat directory count exceeded")
			}
			child.manifest = target
			child.children = map[string]*treeNode{}
			if err := loadFlatDirectory(ctx, manifests, child, label+"/", targets, depth+1, objects); err != nil {
				return err
			}
		case model.DirectoryEntryTypeFile:
		default:
			return fmt.Errorf("directory entry requires an explicit type")
		}
		node.children[entry.Name] = child
	}
	return nil
}

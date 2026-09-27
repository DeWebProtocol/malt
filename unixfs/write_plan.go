package unixfs

import (
	"context"
	"errors"

	"github.com/dewebprotocol/malt-client/writeplan"
	cid "github.com/ipfs/go-cid"
)

// Custom injected root/payload compilers retain their explicit lifecycle.
// The native writer compiles the entire operation against private local ports.
func (w *verifiedWriter) canPrepare() bool {
	roots, rootsOK := w.roots.(*AuthenticationAdapter)
	lists, listsOK := w.lists.(*AuthenticationAdapter)
	return !w.preparing && rootsOK && listsOK && roots == lists
}

func (w *verifiedWriter) runPrepared(ctx context.Context, base cid.Cid, run func(*verifiedWriter) (cid.Cid, error)) (err error) {
	rootAdapter := w.roots.(*AuthenticationAdapter)
	listAdapter := w.lists.(*AuthenticationAdapter)
	staging, err := writeplan.NewStaging(w.tempDir, rootAdapter.remote, w.store)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, staging.Close()) }()
	roots, lists := *rootAdapter, *listAdapter
	roots.remote, lists.remote = staging, staging
	local := *w
	local.preparing, local.store, local.roots, local.lists = true, staging, &roots, &lists
	root, err := run(&local)
	if err != nil {
		return err
	}
	plan, err := staging.Plan(base, root)
	if err != nil {
		return err
	}
	if err := plan.Persist(ctx, w.store, rootAdapter.remote); err != nil {
		return err
	}
	return w.verifyStagedCandidate(ctx, root, local.preparedTree)
}

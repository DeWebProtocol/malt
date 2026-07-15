package writer

import (
	"fmt"
	"reflect"
	"sync"

	cid "github.com/ipfs/go-cid"
)

// Legacy root-consumption freshness is isolated from semantic mutation logic.
// It protects overwrite-like reference materializers; authoritative head and
// multi-writer policy remain outside MALT core.
var sharedFreshnessGuards sync.Map

type rootFreshnessGuard struct {
	mu       sync.Mutex
	consumed map[string]cid.Cid
}

func newRootFreshnessGuard() *rootFreshnessGuard {
	return &rootFreshnessGuard{consumed: make(map[string]cid.Cid)}
}

func sharedRootFreshnessGuard(table Materializer) *rootFreshnessGuard {
	key, ok := materializerFreshnessIdentity(table)
	if !ok {
		return newRootFreshnessGuard()
	}
	guard, _ := sharedFreshnessGuards.LoadOrStore(key, newRootFreshnessGuard())
	return guard.(*rootFreshnessGuard)
}

func materializerFreshnessIdentity(table Materializer) (any, bool) {
	if table == nil || !reflect.TypeOf(table).Comparable() {
		return nil, false
	}
	return table, true
}

func rootFreshnessGuardFor(table Materializer) *rootFreshnessGuard {
	if supportsConcurrentBranches(table) {
		return nil
	}
	return sharedRootFreshnessGuard(table)
}

func supportsConcurrentBranches(table Materializer) bool {
	if table == nil {
		return false
	}
	branching, ok := table.(BranchingMaterializer)
	return ok && branching.SupportsConcurrentBranches()
}

func freshnessKey(namespace string, root cid.Cid) string {
	return namespace + "\x00" + root.String()
}

func (g *rootFreshnessGuard) beginUpdate(namespace string, root cid.Cid) (func(), error) {
	key := freshnessKey(namespace, root)
	g.mu.Lock()
	advancedTo, ok := g.consumed[key]
	if !ok {
		return g.mu.Unlock, nil
	}
	g.mu.Unlock()
	return nil, fmt.Errorf("%w: root %s in namespace %q already advanced to %s", ErrStaleRoot, root, namespace, advancedTo)
}

func (g *rootFreshnessGuard) markAdvanced(namespace string, oldRoot, newRoot cid.Cid) {
	if !oldRoot.Defined() || !newRoot.Defined() || oldRoot.Equals(newRoot) {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.markAdvancedLocked(namespace, oldRoot, newRoot)
}

func (g *rootFreshnessGuard) markAdvancedLocked(namespace string, oldRoot, newRoot cid.Cid) {
	if !oldRoot.Defined() || !newRoot.Defined() || oldRoot.Equals(newRoot) {
		return
	}
	oldKey := freshnessKey(namespace, oldRoot)
	newKey := freshnessKey(namespace, newRoot)
	g.consumed[oldKey] = newRoot
	delete(g.consumed, newKey)
}

func (g *rootFreshnessGuard) markCurrent(namespace string, root cid.Cid) {
	if !root.Defined() {
		return
	}
	g.mu.Lock()
	delete(g.consumed, freshnessKey(namespace, root))
	g.mu.Unlock()
}

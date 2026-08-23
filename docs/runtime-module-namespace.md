# Runtime module namespace decision gate

Status: accepted on 2026-08-23; namespace cutover deferred.

## Decision

The repository, product, and binary are named `malt`, but the Go module remains
`github.com/dewebprotocol/malt-client`. No runtime release tag may be published
from this repository while that temporary namespace is active.

This is a deliberate compatibility gate, not the final public module name.
`github.com/dewebprotocol/malt` was already used by historical MALT Core
versions. Reusing it without a separately reviewed pre-v1 reset could make Go
proxies and consumers resolve two unrelated package families under one module
identity. A repository rename therefore does not authorize a module rename.

The current decision changes no wire profile, Root or CID codec, ProofList,
mutation or receipt encoding, local state path, daemon IPC name, or Core module.
The historical runtime `v0.0.1` tag and every historical Core tag remain intact.

## Enforced state

The active GitHub repository ruleset named
`Block runtime tags until module namespace cutover` is the primary enforcement
boundary. It targets all tags, has no bypass actor, and blocks tag creation,
update, and deletion. Unlike a tag-triggered workflow, the ruleset evaluates
the ref change itself and also covers a new tag aimed at an old commit.

`scripts/verify-module-namespace.sh` and the `Go` workflow provide defense in
depth and enforce both source-tree rules:

1. the active module must remain exactly
   `github.com/dewebprotocol/malt-client`; and
2. a tag context fails before test/build jobs until a dedicated
   namespace-cutover change replaces this gate.

The workflow alone cannot prevent GitHub from creating a ref and an old commit
may not contain the workflow at all; it must never be described as a substitute
for the active ruleset. Maintainers must not remove or weaken the ruleset, nor
bypass a failing workflow with an out-of-band GitHub Release. A branch build,
commit, or repository rename is not release evidence.

## Dedicated cutover requirements

A future cutover PR must make and document one explicit namespace choice:

- reuse `github.com/dewebprotocol/malt` as a clearly announced pre-v1 namespace
  reset with a disjoint runtime version line; or
- provision a resolvable repository or stable vanity import path dedicated to
  the runtime.

That PR must not combine the import-path change with a wire, protocol, schema,
or local-state migration. It must include all of the following:

1. inventory every historical Core and runtime version already visible through
   public Go proxies and direct VCS resolution;
2. select a first runtime version that cannot collide with an existing tag and
   state why old Core consumers will not be silently upgraded to runtime code;
3. update `go.mod`, every internal import, examples, API documentation, CI,
   provenance metadata, and downstream consumers in one auditable migration;
4. build an external consumer against the candidate module using a fresh,
   isolated `GOMODCACHE`, once through the public proxy and once with
   `GOPROXY=direct`;
5. run full runtime tests, race, vet, Linux/macOS/Windows builds, browser WASM,
   Gateway verified read/write and malicious-response Product E2E;
6. migrate and pin Gateway and evaluation consumers before publishing the
   runtime release;
7. merge first, create an annotated tag from the exact final merge, rebuild
   release assets from that tag, and publish notes that call the change a
   pre-v1 namespace reset; and
8. replace this deferred gate with a guard for the selected module path and
   approved first version line; then change the tag ruleset through an audited
   maintainer operation that permits only the approved release procedure and
   restores immutable update/deletion protection immediately after creation.

If any prerequisite is missing, rollback is to leave this repository untagged
and retain `github.com/dewebprotocol/malt-client`. No force-push, tag rewrite,
release deletion, or historical module retraction is permitted.

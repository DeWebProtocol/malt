// Package nodeapi defines the public, transport-independent MALT node service
// contract. In-process services, RPC clients, applications, and synchronization
// engines consume the same narrow capabilities.
//
// Authentication requests, candidates, and receipts are canonical Core values.
// CAS and dataset operations are node services above Core. Implementations bind
// their own storage, execution scope, caller authority, and lifecycle; adapters
// map the same operations to a wire protocol without owning their semantics.
// A capability is not evidence that its implementation is trusted.
//
// Successful materialization acknowledges storage at the selected service, not
// replication to another node. Dataset publication is a separate operation;
// neither operation accepts a Root on behalf of a user. Trusted-root policy,
// key custody, local filesystem control, and hosted account administration are
// deliberately separate services.
//
// Implementations honor context cancellation and preserve exact Root, CID,
// dataset, branch, and operation identities. RPC adapters may add transport
// limits and authentication but must not change those meanings. Optional batch
// capabilities preserve ordered results and allow partial persistence on error;
// they do not imply a transaction across content, relationships, and publication.
package nodeapi

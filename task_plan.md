# Task Plan: Refactor Resolver Architecture (Phase 2)

## Goal
Define Resolver interface and implement multiple resolver types with proper Evidence types.

## Target Architecture

```
core/resolver/
├── resolver.go      # Resolver interface + Evidence types
├── explicit/        # Explicit arc resolver (MALT arcs)
├── implicit/        # Implicit resolver (Merkle DAG via CAS)
└── hamt/            # HAMT resolver (future)

core/types/
├── arcset/          # ArcSet types
├── kvstore/         # KVStore types
└── evidence/        # Evidence types (like key/)
```

## Evidence Design (like Key)

```go
// Evidence represents proof of a resolution step
type Evidence interface {
    Bytes() []byte
    String() string
    Equals(other Evidence) bool
    Kind() EvidenceKind
}

type EvidenceKind int

const (
    EvidenceKindExplicit EvidenceKind = iota  // MALT arc proof
    EvidenceKindImplicit                       // Block content hash
    EvidenceKindHAMT                           // HAMT proof
)

// ExplicitEvidence - cryptographic proof from SCE
type ExplicitEvidence struct {
    proof []byte  // KZG/Verkle/IPA proof
}

// ImplicitEvidence - block content for Merkle DAG
type ImplicitEvidence struct {
    blockContent []byte
}

// HAMTEvidence - HAMT proof
type HAMTEvidence struct {
    proof []byte
}
```

## Resolver Interface

```go
// Resolver resolves a single step from a root key.
type Resolver interface {
    // Resolve finds the longest matching prefix and returns evidence.
    Resolve(root key.Key, path string) (matchedPath string, target key.Key, evidence Evidence, err error)

    // Verify verifies the evidence for a resolution step.
    Verify(root key.Key, path string, target key.Key, evidence Evidence) (bool, error)
}
```

## Phases

### Phase 5: Create Evidence types
Status: `pending`

- Create `core/types/evidence/evidence.go`
- Define Evidence interface and implementations
- Move arcset.Proof to evidence package

### Phase 6: Define Resolver interface
Status: `pending`

- Update `core/resolver/resolver.go` with interface
- Create `core/resolver/explicit/` package
- Move current implementation to explicit/

### Phase 7: Implement Implicit resolver
Status: `pending`

- Create `core/resolver/implicit/`
- Implement Merkle DAG resolution via CAS

### Phase 8: Update dependencies
Status: `pending`

- Update gateway to use Resolver interface
- Update Node to compose resolvers
- Update all tests

## Files to Modify

| File | Action |
|------|--------|
| `core/types/evidence/evidence.go` | Create new |
| `core/resolver/resolver.go` | Define interface |
| `core/resolver/explicit/explicit.go` | Move from resolver.go |
| `core/resolver/implicit/implicit.go` | Create new |
| `gateway/gateway.go` | Use Resolver interface |

## Decisions

| Decision | Choice | Reason |
|----------|--------|--------|
| Evidence location | `core/types/evidence/` | Similar to key package |
| Resolver interface | `core/resolver/` | Core abstraction |
| Proof type | Removed | Replaced by Evidence |
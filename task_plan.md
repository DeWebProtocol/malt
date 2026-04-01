# Task Plan: Refactor Resolver Architecture

## Goal
Separate concerns between `core/resolver` (MALT-specific resolution) and `gateway/` (hybrid resolution with prefix consumption).

## Background

### Current Problem
`core/resolver/resolver.go` mixes multiple responsibilities:
- EAT longest-prefix matching ✓ (should stay)
- SCE proof generation ✓ (should stay)
- Path consumption loop ✗ (should move to gateway)
- CAS implicit resolution ✗ (should move to gateway)
- Transcript management ✗ (should move to gateway)

### Target Architecture

```
core/resolver/     - MALT-specific: longest prefix match + proof generation
gateway/           - Hybrid resolution: path consumption loop, CAS, Transcript
```

## Phases

### Phase 1: Define core/resolver responsibilities
Status: `complete`

**Scope:**
- Only longest-prefix matching in EAT
- Only SCE proof generation
- Single-step resolution (no loop)
- No CAS dependency
- No Transcript

**API:**
```go
// Resolver resolves a single step from EAT using longest-prefix match.
type Resolver struct {
    eat eat.EAT
    sce *sce.Engine
}

// ResolveStep finds longest matching prefix and generates proof.
// Returns: matchedPath, target, proof, error
func (r *Resolver) ResolveStep(root key.Key, path string) (matchedPath string, target key.Key, proof arcset.Proof, err error)

// VerifyStep verifies a single step's proof.
func (r *Resolver) VerifyStep(root key.Key, path string, target key.Key, proof arcset.Proof) (bool, error)
```

### Phase 2: Create gateway package
Status: `complete`

**Scope:**
- Path consumption loop
- CAS integration for implicit resolution
- Transcript management
- Hybrid resolution (explicit + implicit)

**API:**
```go
// Gateway handles hybrid resolution with prefix consumption.
type Gateway struct {
    resolver *resolver.Resolver
    cas      cas.Client
}

// Resolve resolves a full path, consuming prefixes step by step.
func (g *Gateway) Resolve(root key.Key, path string) (*ResolveResult, error)

// VerifyTranscript verifies all steps in a transcript.
func (g *Gateway) VerifyTranscript(root key.Key, transcript *Transcript) (bool, error)
```

### Phase 3: Refactor existing code
Status: `complete`

- Simplify `core/resolver/resolver.go` to single-step logic
- Create `gateway/gateway.go` with path loop logic
- Move Transcript types to `gateway/`
- Update tests

### Phase 4: Update tests
Status: `complete`

- Update `core/resolver/resolver_test.go` for single-step tests
- Create `gateway/gateway_test.go` for full path resolution tests

## Files to Modify

| File | Action |
|------|--------|
| `core/resolver/resolver.go` | Simplify to single-step |
| `core/resolver/resolver_test.go` | Update tests |
| `gateway/gateway.go` | Create new |
| `gateway/gateway_test.go` | Create new |
| `gateway/transcript.go` | Move Transcript types here |

## Decisions

| Decision | Choice | Reason |
|----------|--------|--------|
| Transcript location | `gateway/` | Only gateway needs it |
| CAS dependency | `gateway/` only | Core resolver should be pure |
| VerifyTranscript | `gateway/` | Requires full context |

## Errors Encountered

| Error | Attempt | Resolution |
|-------|---------|------------|
| (none yet) | - | - |
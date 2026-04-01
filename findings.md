# Findings: Resolver Refactor

## Current Implementation Analysis

### core/resolver/resolver.go (227 lines)

**Dependencies:**
- `eat.EAT` - for path → key lookup
- `sce.Engine` - for proof generation
- `cas.Client` - for implicit resolution (should not be here)

**Types:**
- `Resolver` - main struct
- `ResolveResult` - result with target + transcript
- `Transcript` - list of step evidence
- `StepEvidence` - single step record
- `StepKind` - explicit vs implicit

**Methods:**
- `Resolve()` - main loop, handles both key types
- `resolveExplicitStep()` - longest prefix match + proof
- `VerifyTranscript()` - verify all steps

**What belongs in core/resolver:**
- `resolveExplicitStep()` logic (longest prefix matching)
- SCE proof generation
- Single step resolution

**What should move to gateway:**
- CAS dependency
- `Resolve()` loop logic
- `Transcript`, `StepEvidence`, `StepKind` types
- `ResolveResult` type
- `VerifyTranscript()` method
- Implicit resolution logic

## Design Decisions

1. **Single-step vs Multi-step**: core/resolver should only do single step
2. **CAS integration**: Only gateway needs CAS
3. **Transcript management**: Gateway owns the full resolution context
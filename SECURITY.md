# Security Policy

MALT is an experimental reference implementation. It is runnable end to end, but
its public APIs, ProofList schemas, wire formats, and deployment policies may
change. It is not production-ready.

Security reports are still important because the project deals with proof
verification, authenticated structure, daemon APIs, untrusted storage, and local
persistence.

## Supported Versions

Until the first tagged release, security review applies to the `main` branch.
After releases begin, this file will name supported release lines.

## Reporting a Vulnerability

Do not open a public issue for a suspected vulnerability.

Preferred reporting path:

1. Use GitHub private vulnerability reporting or open a private GitHub Security
   Advisory for this repository.
2. Include a short description, reproduction steps, affected commit or version,
   and any proof-of-concept data needed to reproduce the issue.
3. If private vulnerability reporting is not enabled yet, open a minimal public
   issue asking for a private contact path. Do not include vulnerability details
   in that issue.

Maintainers should acknowledge reports within 7 days when practical and keep the
reporter updated on triage, fix, and disclosure timing.

## Useful Report Categories

High-value areas include:

- ProofList verification accepting invalid evidence
- root or target mismatches in read verification
- daemon routes that expose admin or mutation behavior unintentionally
- CORS behavior that allows unintended browser access
- unsafe file handling in `malt add`, evaluator replay, or local state paths
- dependency vulnerabilities with reachable impact

## Current Experimental Limits

The current implementation does not provide production guarantees for:

- head publication and freshness
- multi-writer merge or arbitration
- tenant isolation, quota, pinning, or garbage collection
- stable public API compatibility

Reports in those areas are useful, but they may be handled as design issues
rather than confidential vulnerabilities unless they expose a concrete bug in
the current implementation.

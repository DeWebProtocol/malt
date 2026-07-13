# Security Policy

MALT is an experimental reference implementation. It is runnable end to end, but
its public APIs, ProofList schemas, wire formats, and deployment policies may
change. It is not production-ready.

Security reports are still important because the project deals with proof
verification, authenticated structure, executor APIs, untrusted storage, and local
persistence.

## Supported Versions

| Version | Security support |
| --- | --- |
| `main` | Best-effort review of current integration code |
| `v0.0.4` | Current supported experimental source release |
| `v0.0.3` | Previous experimental source release |
| `v0.0.2` and earlier | Not supported |

MALT remains pre-`v1.0.0` and experimental. Security fixes may require
breaking API, proof, root, or wire-format changes. Reproducible consumers
should pin `v0.0.4` rather than depend on `main`.

## Reporting a Vulnerability

Do not open a public issue for a suspected vulnerability.

Current security contact: security@deweb.world

Preferred reporting path:

1. Use GitHub private vulnerability reporting or open a private GitHub Security
   Advisory for this repository.
2. If private vulnerability reporting is not enabled yet, contact
   security@deweb.world privately with `SECURITY` in the subject or opening
   line.
3. Include a short description, reproduction steps, affected commit or version,
   and any proof-of-concept data needed to reproduce the issue.

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

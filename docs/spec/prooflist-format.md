# ProofList Format

ProofList is the verifier-facing read artifact returned by MALT reads.

## Scope

This document should define:

- the ProofList envelope
- proof-step ordering
- terminal target binding
- body/header binding for HTTP responses
- range-read evidence for large files

## Current References

Current implementation references include:

- `auth/proof/prooflist`
- `server/service_verify.go`
- `server/routes_content.go`
- `layout/unixfs/prooflist.go`
- [MIP-1003](../mips/mip-1003-prooflist-verification-schema.md)

## Status

Experimental and implementation-bound.

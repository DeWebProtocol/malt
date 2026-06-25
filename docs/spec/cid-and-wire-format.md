# CID and Wire Format

MALT uses typed root CIDs and related wire encodings for map/list roots and
their verifier-facing metadata.

## Scope

This document should define:

- typed MALT root CID forms
- multicodec and codec helper behavior
- canonical wire encoding for roots and related proof artifacts
- compatibility expectations for serialized forms

## Current References

Current implementation references include:

- `wire/maltcid`
- `api/http`
- `auth/encoding`
- [MIP-1010](../mips/mip-1010-data-authentication-core-boundary.md)

## Status

Experimental and implementation-bound.

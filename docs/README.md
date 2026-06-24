# MALT Implementation Documentation

This directory is the implementation-bound technical documentation surface for
`DeWebProtocol/malt`.

Use these documents as the source of truth for behavior that must stay aligned
with code, tests, schemas, wire formats, and evaluator artifacts. Public
website pages in `DeWebProtocol/malt-web` may summarize this material, but they
should link back here for protocol and compatibility details.

## Core Docs

- [Threat model](./threat-model.md)
- [Compatibility policy](./compatibility.md)
- [Evaluation](./evaluation.md)
- [Release process](./releasing.md)

## MALT Improvement Proposals

MALT Improvement Proposals live in [docs/mips](./mips/). MIPs are the review
path for semantic, verifier-facing, API, layout, tooling, and evaluation
changes before they become implementation work.

The previous `documents/MIPs` directory in the research-paper workspace is kept
as historical research context. New implementation-bound MIP work should happen
here.

## Future Docs

The following documents should be added when their contents can be grounded in
current code and tests:

- `specification.md`
- `prooflist-format.md`
- `cid-and-wire-format.md`
- `http-api.md`

Until those files exist, use `ARCHITECTURE.md`, package-level Go documentation,
and current DTOs/routes as the source of truth for details.

# `malt.artifact/v0alpha2` Conformance Fixtures

These fixtures exercise the zero-segment root-identity case without relying on
a commitment backend or mutable runtime state. They are stable byte-level
examples for resolve, remote diagnostic verify, and local trusted-root plus
caller-expectation verifier request/response codecs.

The fixture proves only identity: the trusted root is the returned target and
the ProofList has no traversal steps. Non-empty resolve and primitive prove
artifacts require real backend evidence and are covered by Go integration tests.

`resolve-root-artifact-v004.json` preserves the exact zero-segment query shape
emitted by v0.0.4 (`{"kind":"path"}`). Decoders for the same profile normalize
the missing `segments` field to `[]`; canonical encoders always emit the array.

Consumers should validate the JSON shape against the checked-in schemas and
then run semantic/cryptographic verification. Passing schema validation alone
is not conformance.

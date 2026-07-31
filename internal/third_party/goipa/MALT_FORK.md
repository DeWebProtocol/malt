# MALT go-ipa fork

This directory is a source copy of
`github.com/crate-crypto/go-ipa@53bbb0ceb27adb011950fd0fce885ad6d4516f84`.
The upstream project is licensed under the included Apache-2.0 and MIT
licenses.

The source is compiled as packages inside the MALT module (rather than as a
nested module or a relative `replace`) so tagged and pseudo-version consumers
receive the exact audited code.

MALT carries the copy because upstream exposes only one fixed-base MSM setup:
a roughly 334 MiB Verkle-optimized table. The local patch adds verifier-only,
direct, compact 4-bit, and original fast settings while preserving the SRS,
transcripts, commitment bytes, and proof encoding. Keep changes narrow and
validate all profiles against the upstream fast output before updating the
source snapshot.

The behavioral patch is limited to `ipa/config.go`,
`banderwagon/precomp.go`, and the error-returning commitment calls in
`multiproof.go`. Import paths are mechanically rewritten to the internal MALT
location. Upstream test files are not copied; MALT owns profile equivalence,
32-bit, and real-WASM conformance coverage at the integration boundary.

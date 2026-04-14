// Package interfaces defines the shared interfaces used by the MALT codebase.
//
// The architectural center of MALT is the graph-scoped explicit structure layer:
//
//   - Graph provides the main read and write operations over structure roots.
//   - Proof and Transcript model verifiable resolution results.
//   - UpdateDelta records localized structural change.
//
// Other interfaces in this package exist to support concrete implementations,
// adapters, and optional deployment/tooling code. They should not be read as a
// claim that generic storage injection or deployment factories are the primary
// abstraction of MALT itself.
package interfaces

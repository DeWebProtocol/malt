# Evaluation Routing

The evaluator application, workload plans, comparison adapters, and schemas
were removed from the SDK-only core in v0.0.6. Their executable home is now
[`DeWebProtocol/malt-evaluation`](https://github.com/DeWebProtocol/malt-evaluation).
The initial repository preserves the complete v0.0.5 runner against its exact
historical dependency; current v0.0.6 adapters migrate independently.

Gateway-owned CAS-to-gateway-to-client end-to-end tests validate the current
product boundary. Paper-facing result interpretation, figures, and research
narrative belong in `DeWebProtocol/documents`.

Core retains conformance and benchmark tests only when they directly exercise
protocol, commitment, proof, semantic, or verifier behavior. Historical MIPs
may link to this page when describing the former in-tree evaluator.

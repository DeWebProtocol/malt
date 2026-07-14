# Evaluation Routing

The evaluator application, workload plans, result artifacts, and paper-facing
analysis were removed from the SDK-only core in v0.0.6. Current research and
paper evaluation material belongs in `DeWebProtocol/documents`.

Core retains conformance and benchmark tests only when they directly exercise
protocol, commitment, proof, semantic, or verifier behavior. Historical MIPs
may link to this page when describing the former in-tree evaluator.

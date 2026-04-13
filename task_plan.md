# Task Plan: CAS Layer Refactor

## Goal
Refactor `core/cas/` to address two issues:
1. Merge `ipfsgateway` and `ipfslocal` into a single unified IPFS client (they both use HTTP, just different endpoints)
2. Rewrite `mock` CAS to use KVStore-backed block storage with simulated latency (instead of in-memory `map[string][]byte`)

## Phases

### Phase 1: Analyze Current State
- [x] Read all CAS files (cas.go, ipfsgateway, ipfslocal, mock)
- [x] Read KVStore interface
- [x] Identify all usages across codebase

### Phase 2: Unify IPFS into single `core/cas/ipfs/` package
- [x] Create `core/cas/ipfs/` with merged ipfslocal code + timeout options
- [x] Delete `core/cas/ipfsgateway/` and `core/cas/ipfslocal/`
- [x] Update `api/node.go`: import `cas/ipfs` instead of `cas/ipfsgateway`
- [x] Update `cmd/gateway/main.go`: import `cas/ipfs` instead of `cas/ipfslocal`

### Phase 3: Rewrite Mock CAS on KVStore
- [x] Rewrite `mock/mock.go` backed by `*memory.KV` instead of `map[string][]byte`
- [x] Add configurable latency simulation (default 100µs ± 50µs jitter)
- [x] Preserve `AddBlock` helper for pre-seeding test data
- [x] `go build ./...` ✅ `go vet ./...` ✅ `go test ./...` ✅ (all pass)

## Errors Encountered
| Error | Attempt | Resolution |
|-------|---------|------------|
| | | |

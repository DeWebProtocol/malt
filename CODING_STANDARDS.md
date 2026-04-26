# MALT Go Implementation Coding Standards

## File Naming Conventions

### Test Files

| Type | Naming Pattern | Example |
|------|---------------|---------|
| Unit Tests | `*_test.go` | `bloom_test.go` |
| Benchmark Tests | `*_benchmark_test.go` | `bloom_benchmark_test.go` |
| Integration Tests | `*_integration_test.go` | `arctable_integration_test.go` |

### Why `_benchmark_test.go`?

1. **Easy filtering**: `go test -bench=. -run=^$ ./...` can skip unit tests
2. **Clear separation**: Benchmarks are performance tests, distinct from correctness tests
3. **IDE support**: Most IDEs can filter by file pattern
4. **Consistency**: All benchmark files follow the same pattern across the codebase

### Implementation Files

| Type | Naming Pattern | Example |
|------|---------------|---------|
| Interface/Package | `package.go` or `interface.go` | `bloom.go` |
| Implementation | `descriptive.go` | `standard.go`, `cache.go` |
| Options/Config | `options.go` or `config.go` | `options.go` |

### Package Surface Naming

- The primary interface or package surface file should match the package name.
- Examples:
  - package `list` -> `list.go`
  - package `mapping` -> `mapping.go`
  - package `tree` -> `tree.go`
- Avoid generic primary implementation filenames like `semantic.go` when the
  package already provides the semantic context.
- Exported concrete types must be descriptive. Avoid names like `Semantic`,
  `Manager`, or `Impl` when the package has multiple possible implementations.
- Constructors must describe what they build. Prefer `NewList`,
  `NewTreeList` or `NewResolver` over a bare `New` when the
  call site would otherwise hide the constructed type.
- Runtime scope such as `bucketID`, `graphID`, or request-local parameters
  should be passed into operations, not captured as long-lived semantic object
  fields, unless the object is explicitly intended to be bound to that runtime
  scope.
- Reserve the term `graph` for the abstract authenticated read/write contract
  unless the package explicitly documents that it is current runtime metadata or
  compatibility code.
- Treat `map` and `list` packages as graph implementations. Resolver and writer
  packages may adapt those implementations, but they should not redefine their
  read/write semantics.

## Code Style

### Go Standard Practices

- Follow [Effective Go](https://go.dev/doc/effective_go) guidelines
- Use `gofmt` for formatting
- Run `go vet` before commits

### Comments

```go
// Package bloom provides Bloom Filter implementations for ArcTable.
// Bloom filters provide fast negative membership tests.
package bloom

// Add adds an item to the bloom filter.
// The item is hashed using murmur3 and k positions are set in the bitset.
func (b *StandardBloom) Add(item []byte) {
    // implementation
}
```

- Package comment at top of file
- Function comments describe purpose, not implementation details
- Complex logic explained inline

### Error Handling

```go
// Good: wrap errors with context
if err := bc.kv.Put(ctx, key, data); err != nil {
    return fmt.Errorf("failed to persist bloom filter: %w", err)
}

// Bad: lose error context
if err != nil {
    return err
}
```

### Constants vs Config

```go
// Default values as constants
const (
    DefaultExpectedItems     = 10000
    DefaultFalsePositiveRate = 0.01
)

// User-configurable via struct
type BucketConfig struct {
    ExpectedItems     int     `json:"expectedItems"`
    FalsePositiveRate float64 `json:"falsePositiveRate"`
}
```

## Testing Standards

### Unit Tests

- Test one behavior per test function
- Use table-driven tests for multiple cases
- Name tests: `Test<FunctionName><Scenario>`

```go
func TestStandardBloomAdd(t *testing.T) {
    tests := []struct {
        name     string
        items    []string
        expected int
    }{
        {"single_item", []string{"a"}, 1},
        {"multiple_items", []string{"a", "b", "c"}, 3},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            bf := bloom.NewStandardBloom(100, 0.01)
            for _, item := range tt.items {
                bf.Add([]byte(item))
            }
            if bf.Size() != uint64(tt.expected) {
                t.Errorf("expected %d, got %d", tt.expected, bf.Size())
            }
        })
    }
}
```

### Benchmark Tests

- Use `b.ResetTimer()` after setup
- Report memory allocations with `-benchmem`
- Name benchmarks: `Benchmark<FunctionName><Scenario>`

```go
func BenchmarkStandardBloomAdd(b *testing.B) {
    bf := bloom.NewStandardBloom(10000, 0.01)
    item := []byte("test/path/123")

    b.ResetTimer()
    for i := 0; i < b.N; i++ {
        bf.Add(item)
    }
}
```

### Integration Tests

- Use `//go:build integration` build tag
- Require external resources (database, network)
- Can be skipped with `go test -tags=!integration`

## Git Workflow

### Commit Messages

```
<type>: <short description>

<optional detailed description>

Co-Authored-By: Claude Opus 4.6 <noreply@anthropic.com>
```

Types: `feat`, `fix`, `refactor`, `test`, `docs`, `style`, `chore`

### Branch Names

- Feature: `feat/bloom-cache`
- Fix: `fix/memory-leak`
- Refactor: `refactor/arctable-architecture`

## Checklist Before Commit

1. `gofmt -s -w .` - Code formatted
2. `go test ./...` - All tests pass
3. `go vet ./...` - No vet warnings when the touched area justifies it
4. Commit message follows convention

## File Organization

```
malt/
├── cmd/
│   ├── malt/
│   │   ├── main.go                  # CLI root
│   │   ├── daemon.go                # Daemon command
│   │   ├── bucket*.go               # Bucket commands
│   │   ├── add.go                   # File/directory ingest
│   │   ├── cat.go                   # Bucket content read
│   │   └── get.go                   # Bucket export
├── config/
│   └── config.go                    # Configuration
├── core/
│   ├── api/
│   │   ├── node.go                  # MALT Node (entry point)
│   │   ├── options.go               # Functional options
│   ├── cas/
│   │   ├── cas.go                   # CAS interface
│   │   ├── mock/
│   │   │   └── mock.go              # Mock CAS impl
│   │   └── ipfs/
│   │       └── ipfs.go              # IPFS/Kubo HTTP CAS adapter
│   ├── arctable/
│   │   ├── arctable.go                   # ArcTable interface
│   │   ├── bloom/
│   │   │   ├── bloom.go             # Bloom filter interface
│   │   │   ├── standard.go          # StandardBloom impl
│   │   │   ├── cache.go             # BloomCache impl
│   │   │   ├── bloom_test.go        # Unit tests
│   │   │   └── bloom_benchmark_test.go  # Benchmark tests
│   │   ├── overwrite/
│   │   │   ├── arctable.go               # Overwrite ArcTable impl
│   │   │   └── arctable_test.go     # Unit tests
│   │   └── versioned/
│   │       ├── versioned.go         # Versioned ArcTable impl
│   │       └── versioned_test.go    # Unit tests
│   ├── bucketpath/
│   │   └── path.go                  # Current bucket path boundary helper
│   ├── kvstore/
│   │   ├── kv.go                    # KVStore interface
│   │   ├── memory/
│   │   │   └── memory.go            # In-memory impl
│   │   ├── badger/
│   │   │   └── badger.go            # BadgerDB impl
│   │   └── fs/
│   │       └── fs.go                # Filesystem KV impl
│   ├── graph/
│   │   ├── graph.go                 # Current runtime composition
│   │   └── manager.go               # Current metadata lifecycle
│   ├── layout/
│   │   └── unixfs/
│   │       ├── layout.go             # Map/list-based UnixFS prototype
│   │       └── layout_test.go        # Layout tests
│   ├── lineage/
│   │   └── lineage.go               # Auxiliary version-history metadata
│   ├── manifest/
│   │   └── directory.go             # Current directory manifest helper
│   ├── resolver/
│   │   ├── resolver.go              # Current read adapter loop
│   │   ├── resolver_test.go         # Unit tests
│   │   └── step/
│   │       ├── step.go              # Step interface
│   │       ├── explicit/
│   │       │   └── explicit.go      # MALT explicit step
│   │       ├── implicit/
│   │       │   └── implicit.go      # Merkle DAG implicit step
│   │       └── hamt/
│   │           └── hamt.go          # HAMT step
│   ├── commitment/
│   │   ├── commitment.go            # Primitive commitment interface
│   │   ├── kzg/
│   │   │   └── kzg.go               # KZG backend
│   │   └── ipa/
│   │       └── ipa.go               # IPA backend
│   ├── structure/
│   │   ├── list/
│   │   │   ├── list.go              # List graph contract
│   │   │   └── tree/
│   │   │       └── tree.go          # Tree list implementation
│   │   └── mapping/
│   │       ├── mapping.go           # Map graph contract
│   │       ├── radix/
│   │       │   └── radix.go         # Radix map implementation
│   │       └── indexed/
│   │           └── indexed.go       # Indexed baseline map
│   ├── codec/
│   │   └── codec.go                 # MALT CID codecs
│   └── types/
│       ├── arcset/
│       │   └── arcset.go            # Arc set types
│       └── evidence/
│           └── evidence.go          # Evidence types
├── httpapi/
│   └── types.go                     # Daemon API payload types
├── server/
│   └── server.go                    # Daemon HTTP server
└── logger/
    └── logger.go                    # Logging utilities
```

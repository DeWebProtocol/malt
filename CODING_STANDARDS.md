# MALT Go Implementation Coding Standards

## File Naming Conventions

### Test Files

| Type | Naming Pattern | Example |
|------|---------------|---------|
| Unit Tests | `*_test.go` | `bloom_test.go` |
| Benchmark Tests | `*_benchmark_test.go` | `bloom_benchmark_test.go` |
| Integration Tests | `*_integration_test.go` | `eat_integration_test.go` |

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

## Code Style

### Go Standard Practices

- Follow [Effective Go](https://go.dev/doc/effective_go) guidelines
- Use `gofmt` for formatting
- Run `go vet` before commits

### Comments

```go
// Package bloom provides Bloom Filter implementations for EAT.
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
- Refactor: `refactor/eat-architecture`

## Checklist Before Commit

1. `go build ./...` - No compilation errors
2. `go test ./...` - All tests pass
3. `go vet ./...` - No vet warnings
4. `gofmt -s -w .` - Code formatted
5. Commit message follows convention

## File Organization

```
malt/
├── cmd/
│   ├── malt/
│   │   └── main.go                  # CLI tool
│   └── gateway/
│       └── main.go                  # HTTP Gateway
├── config/
│   └── config.go                    # Configuration
├── core/
│   ├── api/
│   │   ├── node.go                  # MALT Node (entry point)
│   │   ├── options.go               # Functional options
│   │   ├── structure.go             # Structure API
│   │   └── structure_test.go        # Unit tests
│   ├── cas/
│   │   ├── cas.go                   # CAS interface
│   │   ├── ipld.go                  # IPLD utilities
│   │   ├── mock/
│   │   │   └── mock.go              # Mock CAS impl
│   │   └── ipfsgateway/
│   │       └── ipfsgateway.go       # IPFS Gateway CAS impl
│   ├── eat/
│   │   ├── eat.go                   # EAT interface
│   │   ├── bloom/
│   │   │   ├── bloom.go             # Bloom filter interface
│   │   │   ├── standard.go          # StandardBloom impl
│   │   │   ├── cache.go             # BloomCache impl
│   │   │   ├── bloom_test.go        # Unit tests
│   │   │   └── bloom_benchmark_test.go  # Benchmark tests
│   │   ├── overwrite/
│   │   │   ├── eat.go               # Overwrite EAT impl
│   │   │   └── eat_test.go          # Unit tests
│   │   └── versioned/
│   │       ├── versioned.go         # Versioned EAT impl
│   │       └── versioned_test.go    # Unit tests
│   ├── kvstore/
│   │   ├── kv.go                    # KVStore interface
│   │   ├── memory/
│   │   │   └── memory.go            # In-memory impl
│   │   ├── badger/
│   │   │   └── badger.go            # BadgerDB impl
│   │   └── fs/
│   │       └── fs.go                # Filesystem impl
│   ├── resolver/
│   │   ├── resolver.go              # Hybrid Resolver
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
│   ├── codec/
│   │   └── codec.go                 # MALT CID codecs
│   └── types/
│       ├── arcset/
│       │   └── arcset.go            # Arc set types
│       └── evidence/
│           └── evidence.go          # Evidence types
├── eval/
│   └── benchmark.go                 # Evaluation benchmarks
├── examples/
│   └── basic/
│       └── main.go                  # Basic usage example
└── logger/
    └── logger.go                    # Logging utilities
```

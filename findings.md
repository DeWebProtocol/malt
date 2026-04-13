# Findings

## CAS Current State

### Interface (`cas.Client`)
- `Get(ctx, cid) → ([]byte, error)`
- `Put(ctx, data) → (cid.Cid, error)`
- `Has(ctx, cid) → (bool, error)`

### `ipfsgateway` Client
- **Protocol**: HTTP path-based (`GET {gatewayURL}/{cid}`)
- **Has**: HTTP HEAD to same path, check 200
- **Put**: Returns error — gateway is read-only
- **URL pattern**: `https://ipfs.io/ipfs/{cid}` (path-based routing)

### `ipfslocal` Client
- **Protocol**: IPFS daemon API (`POST /api/v0/block/get?arg={cid}`)
- **Has**: `POST /api/v0/block/stat?arg={cid}`, check 200
- **Put**: `POST /api/v0/block/put` with multipart form
- **URL pattern**: `http://localhost:5001/api/v0/block/*`

### Key Difference: Not Just Endpoint
The two use **fundamentally different HTTP protocols**:
- **ipfsgateway**: Standard HTTP GET/HEAD on CID path — any HTTP client works, no multipart, no POST
- **ipfslocal**: IPFS daemon RPC API — all operations are POST with specific query params or multipart bodies

You cannot merge these by simply changing an endpoint parameter. The request methods, URL paths, and response formats are different.

### Options for Unification
1. **Single `ipfs` package with mode parameter**: One package, two modes ("gateway" vs "api"), mode determines which HTTP protocol to use
2. **Keep separate packages but extract shared options**: Extract timeout/http.Client config into a shared base, keep implementations separate
3. **Unify under "api" mode only**: Remove gateway path-based mode entirely (simpler but loses the public gateway use case)

### Mock CAS Current State
- Uses `map[string][]byte` — O(1) zero-latency
- No persistence, no KVStore
- No realistic behavior simulation

### Mock CAS Redesign
- Wrap `kvstore.KVStore` as storage backend
- Add `Latency` and `LatencyJitter` config fields
- Use `time.Sleep` with configurable latency on Get/Put/Has
- This makes tests more realistic and catches timing-sensitive bugs

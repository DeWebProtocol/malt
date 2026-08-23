package transport_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	casmemory "github.com/dewebprotocol/malt-client/internal/cas/memory"
	client "github.com/dewebprotocol/malt-client/transport"
	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
	"github.com/dewebprotocol/malt-client/transport/capabilitytest"
	"github.com/dewebprotocol/malt-client/transport/hybrid"
	localtransport "github.com/dewebprotocol/malt-client/transport/local"
	cid "github.com/ipfs/go-cid"
)

func TestCASCapabilityContractAcrossTransports(t *testing.T) {
	t.Run("mock", func(t *testing.T) {
		capabilitytest.RunCAS(t, func(*testing.T) transportcap.CAS { return casmemory.New() })
	})
	t.Run("gateway-http", func(t *testing.T) {
		capabilitytest.RunCAS(t, newContractGateway)
	})
	t.Run("local", func(t *testing.T) {
		capabilitytest.RunCAS(t, func(t *testing.T) transportcap.CAS {
			store, err := localtransport.Open(localtransport.Options{Directory: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			return store
		})
	})
	t.Run("hybrid", func(t *testing.T) {
		capabilitytest.RunCAS(t, func(t *testing.T) transportcap.CAS {
			cache, err := localtransport.Open(localtransport.Options{Directory: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = cache.Close() })
			store, err := hybrid.NewCAS(hybrid.CASOptions{Primary: casmemory.New(), Cache: cache})
			if err != nil {
				t.Fatal(err)
			}
			return store
		})
	})
	t.Run("peer-ready-loopback", func(t *testing.T) {
		capabilitytest.RunCAS(t, func(t *testing.T) transportcap.CAS {
			store, err := localtransport.Open(localtransport.Options{Directory: t.TempDir()})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			// This adapter deliberately depends only on the semantic capability.
			// A future peer transport can replace the loopback exchange with its
			// network without changing the contract or application callers.
			return peerCASLoopback{remote: store}
		})
	})
}

type peerCASLoopback struct{ remote transportcap.BatchCAS }

func (p peerCASLoopback) Put(ctx context.Context, body []byte) (cid.Cid, error) {
	return p.remote.Put(ctx, append([]byte(nil), body...))
}

func (p peerCASLoopback) PutWithCodec(ctx context.Context, body []byte, codec uint64) (cid.Cid, error) {
	return p.remote.PutWithCodec(ctx, append([]byte(nil), body...), codec)
}

func (p peerCASLoopback) Get(ctx context.Context, key cid.Cid) ([]byte, error) {
	body, err := p.remote.Get(ctx, key)
	return append([]byte(nil), body...), err
}

func (p peerCASLoopback) Has(ctx context.Context, key cid.Cid) (bool, error) {
	return p.remote.Has(ctx, key)
}

func (p peerCASLoopback) PutBatch(ctx context.Context, blocks []transportcap.Block) ([]transportcap.PutResult, error) {
	cloned := make([]transportcap.Block, len(blocks))
	for index, block := range blocks {
		cloned[index] = transportcap.Block{Data: append([]byte(nil), block.Data...), Codec: block.Codec}
	}
	return p.remote.PutBatch(ctx, cloned)
}

func (p peerCASLoopback) HasBatch(ctx context.Context, keys []cid.Cid) ([]bool, error) {
	return p.remote.HasBatch(ctx, append([]cid.Cid(nil), keys...))
}

type contractGatewayStore struct {
	mu     sync.RWMutex
	blocks map[string][]byte
}

func newContractGateway(t *testing.T) transportcap.CAS {
	t.Helper()
	store := &contractGatewayStore{blocks: make(map[string][]byte)}
	server := httptest.NewServer(http.HandlerFunc(store.serveHTTP))
	t.Cleanup(server.Close)
	remote, err := client.New(client.Options{
		BaseURL: server.URL, TenantBearerToken: "contract-token", BucketID: "contract-dataset",
	})
	if err != nil {
		t.Fatal(err)
	}
	return remote
}

func (s *contractGatewayStore) serveHTTP(response http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Authorization") != "Bearer contract-token" {
		http.Error(response, "missing contract authorization", http.StatusUnauthorized)
		return
	}
	const base = "/v1/buckets/contract-dataset/cas"
	switch {
	case request.Method == http.MethodPost && request.URL.Path == base:
		body, err := io.ReadAll(request.Body)
		if err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		codec, err := strconv.ParseUint(request.URL.Query().Get("codec"), 10, 64)
		if err != nil {
			http.Error(response, "invalid codec", http.StatusBadRequest)
			return
		}
		key, err := clientcas.CIDForBlock(transportcap.Block{Data: body, Codec: codec})
		if err != nil {
			http.Error(response, err.Error(), http.StatusBadRequest)
			return
		}
		s.put(key, body)
		response.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(response).Encode(map[string]string{"cid": key.String()})
	case request.Method == http.MethodPost && request.URL.Path == base+"/batch":
		var payload struct {
			Profile string               `json:"profile"`
			Blocks  []transportcap.Block `json:"blocks"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload.Profile != client.CASPutBatchProfile {
			http.Error(response, "invalid batch", http.StatusBadRequest)
			return
		}
		results := make([]map[string]string, len(payload.Blocks))
		seen := make(map[string]struct{}, len(payload.Blocks))
		for index, block := range payload.Blocks {
			key, err := clientcas.CIDForBlock(block)
			if err != nil {
				http.Error(response, err.Error(), http.StatusBadRequest)
				return
			}
			status := string(transportcap.PutStatusStored)
			if _, duplicate := seen[key.String()]; duplicate {
				status = string(transportcap.PutStatusDuplicateInRequest)
			} else if s.has(key) {
				status = string(transportcap.PutStatusAlreadyPresent)
			}
			seen[key.String()] = struct{}{}
			s.put(key, block.Data)
			results[index] = map[string]string{"cid": key.String(), "status": status}
		}
		response.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(response).Encode(map[string]any{"profile": client.CASPutBatchProfile, "results": results})
	case request.Method == http.MethodPost && request.URL.Path == base+"/has":
		var payload struct {
			Profile string   `json:"profile"`
			CIDs    []string `json:"cids"`
		}
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil || payload.Profile != client.CASHasBatchProfile {
			http.Error(response, "invalid has batch", http.StatusBadRequest)
			return
		}
		results := make([]map[string]any, len(payload.CIDs))
		for index, raw := range payload.CIDs {
			key, err := cid.Parse(raw)
			if err != nil {
				http.Error(response, "invalid CID", http.StatusBadRequest)
				return
			}
			results[index] = map[string]any{"cid": key.String(), "present": s.has(key)}
		}
		_ = json.NewEncoder(response).Encode(map[string]any{"profile": client.CASHasBatchProfile, "results": results})
	case (request.Method == http.MethodGet || request.Method == http.MethodHead) && strings.HasPrefix(request.URL.Path, base+"/"):
		raw, err := url.PathUnescape(strings.TrimPrefix(request.URL.Path, base+"/"))
		if err != nil {
			http.Error(response, "invalid path", http.StatusBadRequest)
			return
		}
		key, err := cid.Parse(raw)
		if err != nil {
			http.Error(response, "invalid CID", http.StatusBadRequest)
			return
		}
		body, ok := s.get(key)
		if !ok {
			http.NotFound(response, request)
			return
		}
		if request.Method == http.MethodGet {
			_, _ = response.Write(body)
		}
	default:
		http.NotFound(response, request)
	}
}

func (s *contractGatewayStore) put(key cid.Cid, body []byte) {
	s.mu.Lock()
	s.blocks[key.String()] = append([]byte(nil), body...)
	s.mu.Unlock()
}

func (s *contractGatewayStore) get(key cid.Cid) ([]byte, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	body, ok := s.blocks[key.String()]
	return append([]byte(nil), body...), ok
}

func (s *contractGatewayStore) has(key cid.Cid) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.blocks[key.String()]
	return ok
}

var _ transportcap.BatchCAS = peerCASLoopback{}

package ipfs

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/dewebprotocol/malt/storage/cas"
	cashttpapi "github.com/dewebprotocol/malt/storage/cas/httpapi"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

const testTypedCodec = 0x300005

// fakeCID creates a deterministic CID from a string seed.
func fakeCID(seed string) cid.Cid {
	mhash, _ := mh.Sum([]byte(seed), mh.SHA2_256, -1)
	return cid.NewCidV1(cid.Raw, mhash)
}

func TestNewClient(t *testing.T) {
	c := NewClient("http://localhost:5001")
	if c == nil {
		t.Fatal("client should not be nil")
	}
	if c.apiURL != "http://localhost:5001" {
		t.Errorf("expected apiURL http://localhost:5001, got %s", c.apiURL)
	}
}

func TestClientGetWithoutDaemon(t *testing.T) {
	// Test that Get returns a proper error when IPFS daemon is not running
	c := NewClient("http://localhost:59999") // non-existent daemon
	ctx := context.Background()
	testCID := fakeCID("test")

	_, err := c.Get(ctx, testCID)
	if err == nil {
		t.Error("expected error when IPFS daemon is not running")
	}
	t.Logf("Get returned expected error: %v", err)
}

func TestClientHasWithoutDaemon(t *testing.T) {
	// Test that Has returns a proper error when IPFS daemon is not running
	c := NewClient("http://localhost:59999") // non-existent daemon
	ctx := context.Background()
	testCID := fakeCID("test")

	_, err := c.Has(ctx, testCID)
	if err == nil {
		t.Error("expected error when IPFS daemon is not running")
	}
	t.Logf("Has returned expected error: %v", err)
}

func TestClientPutWithoutDaemon(t *testing.T) {
	// Test that Put returns a proper error when IPFS daemon is not running
	c := NewClient("http://localhost:59999") // non-existent daemon
	ctx := context.Background()

	_, err := c.Put(ctx, []byte("test data"))
	if err == nil {
		t.Error("expected error when IPFS daemon is not running")
	}
	t.Logf("Put returned expected error: %v", err)
}

func TestClientImplementsInterface(t *testing.T) {
	// Verify that Client implements the cas.Client interface
	var _ interface {
		Get(ctx context.Context, c cid.Cid) ([]byte, error)
		Put(ctx context.Context, data []byte) (cid.Cid, error)
		Has(ctx context.Context, c cid.Cid) (bool, error)
	} = NewClient("http://localhost:5001")
}

func TestClientPutBatchUploadsOnlyMissingBlocks(t *testing.T) {
	ctx := context.Background()
	presentBlock := cas.Block{Data: []byte("present")}
	presentCID, err := cas.CIDForBlock(presentBlock)
	if err != nil {
		t.Fatalf("CIDForBlock present: %v", err)
	}
	uploadedBlocks := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v0/malt/block/has-batch":
			var req cashttpapi.HasBatchRequest
			decodeJSON(t, r, &req)
			resp := cashttpapi.HasBatchResponse{Results: make([]cashttpapi.HasBatchResult, len(req.CIDs))}
			for i, raw := range req.CIDs {
				resp.Results[i] = cashttpapi.HasBatchResult{
					CID:     raw,
					Present: raw == presentCID.String(),
				}
			}
			encodeJSON(t, w, resp)
		case "/api/v0/malt/block/put-batch":
			var req cashttpapi.PutBatchRequest
			decodeJSON(t, r, &req)
			uploadedBlocks += len(req.Blocks)
			resp := cashttpapi.PutBatchResponse{Results: make([]cashttpapi.PutBatchResult, len(req.Blocks))}
			for i, block := range req.Blocks {
				c := cidForHTTPBlock(t, block)
				resp.Results[i] = cashttpapi.PutBatchResult{CID: c.String(), Status: string(cas.PutStatusStored)}
			}
			encodeJSON(t, w, resp)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	client := NewClient(ts.URL)
	results, err := client.PutBatch(ctx, []cas.Block{
		presentBlock,
		{Data: []byte("missing-a")},
		{Data: []byte("missing-a")},
		{Data: []byte("missing-b")},
	})
	if err != nil {
		t.Fatalf("PutBatch: %v", err)
	}
	if uploadedBlocks != 2 {
		t.Fatalf("uploaded blocks = %d, want 2", uploadedBlocks)
	}
	if results[0].Status != cas.PutStatusAlreadyPresent {
		t.Fatalf("present status = %q, want already_present", results[0].Status)
	}
	if results[1].Status != cas.PutStatusStored || results[3].Status != cas.PutStatusStored {
		t.Fatalf("stored statuses = %q/%q, want stored", results[1].Status, results[3].Status)
	}
	if results[2].Status != cas.PutStatusDuplicate {
		t.Fatalf("duplicate status = %q, want duplicate", results[2].Status)
	}
}

func TestClientPutBatchFallsBackToSingleBlockPut(t *testing.T) {
	ctx := context.Background()
	var singlePuts int
	var statCalls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v0/malt/block/has-batch", "/api/v0/malt/block/put-batch":
			http.NotFound(w, r)
		case "/api/v0/block/stat":
			statCalls++
			t.Fatalf("PutBatch fallback must not call block/stat")
		case "/api/v0/block/put":
			singlePuts++
			blockCID := cidForMultipartPut(t, r)
			encodeJSON(t, w, struct {
				Key  string `json:"Key"`
				Size int    `json:"Size"`
			}{Key: blockCID.String()})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	client := NewClient(ts.URL)
	results, err := client.PutBatch(ctx, []cas.Block{{Data: []byte("a")}, {Data: []byte("b")}})
	if err != nil {
		t.Fatalf("PutBatch fallback: %v", err)
	}
	if singlePuts != 2 {
		t.Fatalf("single puts = %d, want 2", singlePuts)
	}
	if statCalls != 0 {
		t.Fatalf("stat calls = %d, want 0", statCalls)
	}
	for i, result := range results {
		if result.Status != cas.PutStatusStored {
			t.Fatalf("result[%d] status = %q, want stored", i, result.Status)
		}
	}
}

func TestClientPutBatchUnsupportedFallbackDeduplicates(t *testing.T) {
	ctx := context.Background()
	var singlePuts int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v0/malt/block/has-batch", "/api/v0/malt/block/put-batch":
			http.NotFound(w, r)
		case "/api/v0/block/stat":
			t.Fatalf("PutBatch fallback must not call block/stat")
		case "/api/v0/block/put":
			singlePuts++
			blockCID := cidForMultipartPut(t, r)
			encodeJSON(t, w, struct {
				Key  string `json:"Key"`
				Size int    `json:"Size"`
			}{Key: blockCID.String()})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	client := NewClient(ts.URL)
	results, err := client.PutBatch(ctx, []cas.Block{
		{Data: []byte("same")},
		{Data: []byte("same")},
		{Data: []byte("other")},
	})
	if err != nil {
		t.Fatalf("PutBatch fallback duplicates: %v", err)
	}
	if singlePuts != 2 {
		t.Fatalf("single puts = %d, want 2", singlePuts)
	}
	if results[0].Status != cas.PutStatusStored || results[2].Status != cas.PutStatusStored {
		t.Fatalf("stored statuses = %q/%q, want stored", results[0].Status, results[2].Status)
	}
	if results[1].Status != cas.PutStatusDuplicate {
		t.Fatalf("duplicate status = %q, want duplicate", results[1].Status)
	}
}

func TestClientPutBatchUnsupportedFallbackPreservesTypedCodec(t *testing.T) {
	ctx := context.Background()
	var gotFormat string
	var gotMHType string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v0/malt/block/has-batch", "/api/v0/malt/block/put-batch":
			http.NotFound(w, r)
		case "/api/v0/block/stat":
			t.Fatalf("PutBatch fallback must not call block/stat")
		case "/api/v0/block/put":
			gotFormat = r.URL.Query().Get("format")
			gotMHType = r.URL.Query().Get("mhtype")
			blockCID := cidForMultipartPut(t, r)
			encodeJSON(t, w, struct {
				Key  string `json:"Key"`
				Size int    `json:"Size"`
			}{Key: blockCID.String()})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	client := NewClient(ts.URL)
	results, err := client.PutBatch(ctx, []cas.Block{{
		Data:  []byte(`{"entries":["a.txt"]}`),
		Codec: testTypedCodec,
	}})
	if err != nil {
		t.Fatalf("PutBatch typed fallback: %v", err)
	}
	if gotFormat != strconv.FormatUint(testTypedCodec, 10) {
		t.Fatalf("format = %q, want %d", gotFormat, testTypedCodec)
	}
	if gotMHType != "sha2-256" {
		t.Fatalf("mhtype = %q, want sha2-256", gotMHType)
	}
	if results[0].CID.Prefix().Codec != testTypedCodec {
		t.Fatalf("result codec = %x, want %x", results[0].CID.Prefix().Codec, testTypedCodec)
	}
}

func TestClientPutBatchUploadsDuplicatePayloadOnce(t *testing.T) {
	ctx := context.Background()
	uploadedBlocks := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v0/malt/block/has-batch":
			var req cashttpapi.HasBatchRequest
			decodeJSON(t, r, &req)
			resp := cashttpapi.HasBatchResponse{Results: make([]cashttpapi.HasBatchResult, len(req.CIDs))}
			for i, raw := range req.CIDs {
				resp.Results[i] = cashttpapi.HasBatchResult{CID: raw}
			}
			encodeJSON(t, w, resp)
		case "/api/v0/malt/block/put-batch":
			var req cashttpapi.PutBatchRequest
			decodeJSON(t, r, &req)
			uploadedBlocks += len(req.Blocks)
			resp := cashttpapi.PutBatchResponse{Results: make([]cashttpapi.PutBatchResult, len(req.Blocks))}
			for i, block := range req.Blocks {
				c := cidForHTTPBlock(t, block)
				resp.Results[i] = cashttpapi.PutBatchResult{CID: c.String(), Status: string(cas.PutStatusStored)}
			}
			encodeJSON(t, w, resp)
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	client := NewClient(ts.URL)
	results, err := client.PutBatch(ctx, []cas.Block{
		{Data: []byte("same")},
		{Data: []byte("same")},
		{Data: []byte("other")},
	})
	if err != nil {
		t.Fatalf("PutBatch duplicates: %v", err)
	}
	if uploadedBlocks != 2 {
		t.Fatalf("uploaded blocks = %d, want 2", uploadedBlocks)
	}
	if results[1].Status != cas.PutStatusDuplicate {
		t.Fatalf("duplicate status = %q, want duplicate", results[1].Status)
	}
}

func TestClientPutBatchPreservesTypedCodecInBatchRequest(t *testing.T) {
	ctx := context.Background()
	var gotCodec uint64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v0/malt/block/has-batch":
			var req cashttpapi.HasBatchRequest
			decodeJSON(t, r, &req)
			resp := cashttpapi.HasBatchResponse{Results: make([]cashttpapi.HasBatchResult, len(req.CIDs))}
			for i, raw := range req.CIDs {
				resp.Results[i] = cashttpapi.HasBatchResult{CID: raw}
			}
			encodeJSON(t, w, resp)
		case "/api/v0/malt/block/put-batch":
			var req cashttpapi.PutBatchRequest
			decodeJSON(t, r, &req)
			gotCodec = req.Blocks[0].Codec
			blockCID := cidForHTTPBlock(t, req.Blocks[0])
			encodeJSON(t, w, cashttpapi.PutBatchResponse{Results: []cashttpapi.PutBatchResult{{
				CID:    blockCID.String(),
				Status: string(cas.PutStatusStored),
			}}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer ts.Close()

	client := NewClient(ts.URL)
	results, err := client.PutBatch(ctx, []cas.Block{{Data: []byte(`{"entries":["a.txt"]}`), Codec: testTypedCodec}})
	if err != nil {
		t.Fatalf("PutBatch typed: %v", err)
	}
	if gotCodec != testTypedCodec {
		t.Fatalf("request codec = %x, want %x", gotCodec, testTypedCodec)
	}
	if results[0].CID.Prefix().Codec != testTypedCodec {
		t.Fatalf("result codec = %x, want %x", results[0].CID.Prefix().Codec, testTypedCodec)
	}
}

func decodeJSON(t *testing.T, r *http.Request, out any) {
	t.Helper()
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		t.Fatalf("decode request: %v", err)
	}
}

func encodeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

func cidForHTTPBlock(t *testing.T, block cashttpapi.PutBatchBlock) cid.Cid {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(block.Data)
	if err != nil {
		t.Fatalf("decode block base64: %v", err)
	}
	c, err := cas.CIDForBlock(cas.Block{Data: data, Codec: block.Codec})
	if err != nil {
		t.Fatalf("CIDForBlock: %v", err)
	}
	return c
}

func cidForMultipartPut(t *testing.T, r *http.Request) cid.Cid {
	t.Helper()
	file, _, err := r.FormFile("file")
	if err != nil {
		t.Fatalf("FormFile: %v", err)
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read multipart: %v", err)
	}
	codecValue := uint64(cid.Raw)
	if raw := r.URL.Query().Get("format"); raw != "" {
		codecValue, err = strconv.ParseUint(raw, 10, 64)
		if err != nil {
			t.Fatalf("parse format: %v", err)
		}
	}
	c, err := cas.CIDForBlock(cas.Block{Data: data, Codec: codecValue})
	if err != nil {
		t.Fatalf("CIDForBlock multipart: %v", err)
	}
	return c
}

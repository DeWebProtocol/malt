package cas_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/dewebprotocol/malt/storage/cas"
	"github.com/dewebprotocol/malt/storage/cas/mock"
	cid "github.com/ipfs/go-cid"
	mh "github.com/multiformats/go-multihash"
)

// newPayloadCID creates a CID from data for testing.
func newPayloadCID(data []byte) (cid.Cid, error) {
	mhash, err := mh.Sum(data, mh.SHA2_256, -1)
	if err != nil {
		return cid.Cid{}, err
	}
	return cid.NewCidV1(cid.Raw, mhash), nil
}

func TestIPLDParserRaw(t *testing.T) {
	store := mock.NewCAS()
	parser := cas.NewIPLDParser(store)

	// Create raw data
	data := []byte("hello world")
	k, err := newPayloadCID(data)
	if err != nil {
		t.Fatalf("newPayloadCID failed: %v", err)
	}

	// Parse
	node, err := parser.ParseBlock(k, data)
	if err != nil {
		t.Fatalf("ParseBlock failed: %v", err)
	}

	if string(node.Data) != "hello world" {
		t.Errorf("Expected 'hello world', got %s", node.Data)
	}
}

func TestIPLDParserDagJSON(t *testing.T) {
	store := mock.NewCAS()
	parser := cas.NewIPLDParser(store)

	// Create DAG-JSON data with a link
	targetCID, _ := cid.Decode("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")
	jsonData := []byte(`{"name": "test", "link": {"/": "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"}}`)

	// Create key with DAG-JSON codec (0x0201)
	mhash, _ := mh.Sum(jsonData, mh.SHA2_256, -1)
	c := cid.NewCidV1(0x0201, mhash) // DAG-JSON codec

	// Parse
	node, err := parser.ParseBlock(c, jsonData)
	if err != nil {
		t.Fatalf("ParseBlock failed: %v", err)
	}

	if node.Fields["name"] != "test" {
		t.Errorf("Expected 'test', got %v", node.Fields["name"])
	}

	// Check link was extracted
	if len(node.Links) == 0 {
		t.Error("Expected at least one link")
	} else {
		if node.Links[0].Name != "link" {
			t.Errorf("Expected link name 'link', got %s", node.Links[0].Name)
		}
		if !node.Links[0].CID.Equals(targetCID) {
			t.Errorf("Link CID mismatch")
		}
	}
}

func TestIPLDParserDagJSONWithArray(t *testing.T) {
	store := mock.NewCAS()
	parser := cas.NewIPLDParser(store)

	// Create DAG-JSON data with array of links
	jsonData := []byte(`{"items": [{"/": "bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi"}, {"/": "bafybeihdwdcefgh4dqkjv67ozcmhfqp46vd4swimutfj3lkq2qhwbg64vc"}]}`)

	mhash, _ := mh.Sum(jsonData, mh.SHA2_256, -1)
	c := cid.NewCidV1(0x0201, mhash) // DAG-JSON codec

	// Parse
	node, err := parser.ParseBlock(c, jsonData)
	if err != nil {
		t.Fatalf("ParseBlock failed: %v", err)
	}

	if len(node.Links) < 2 {
		t.Errorf("Expected at least 2 links, got %d", len(node.Links))
	}
}

func TestIPLDParserCBOR(t *testing.T) {
	store := mock.NewCAS()
	parser := cas.NewIPLDParser(store)

	// Create simple CBOR data
	// CBOR map {"a": 1, "b": 2}
	cborData := []byte{0xa2, 0x61, 0x61, 0x01, 0x61, 0x62, 0x02}

	// Create key with CBOR codec (0x71)
	mhash, _ := mh.Sum(cborData, mh.SHA2_256, -1)
	c := cid.NewCidV1(0x71, mhash) // DAG-CBOR codec

	// Parse
	node, err := parser.ParseBlock(c, cborData)
	if err != nil {
		t.Fatalf("ParseBlock failed: %v", err)
	}

	// Should parse as CBOR map
	if node.Fields["a"] != uint64(1) {
		t.Errorf("Expected a=1, got %v", node.Fields["a"])
	}
}

func TestIPLDParserCBORArray(t *testing.T) {
	store := mock.NewCAS()
	parser := cas.NewIPLDParser(store)

	// CBOR array [1, 2, 3]
	cborData := []byte{0x83, 0x01, 0x02, 0x03}

	mhash, _ := mh.Sum(cborData, mh.SHA2_256, -1)
	c := cid.NewCidV1(0x71, mhash) // DAG-CBOR codec

	// Parse
	node, err := parser.ParseBlock(c, cborData)
	if err != nil {
		t.Fatalf("ParseBlock failed: %v", err)
	}

	if node.Fields["0"] != uint64(1) {
		t.Errorf("Expected [0]=1, got %v", node.Fields["0"])
	}
	if node.Fields["2"] != uint64(3) {
		t.Errorf("Expected [2]=3, got %v", node.Fields["2"])
	}
}

func TestIPLDParserCBORRejectsMaliciousInputs(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{
			name: "array declared length exceeds remaining data",
			data: []byte{0x9a, 0x00, 0x10, 0x00, 0x01},
		},
		{
			name: "map declared length exceeds remaining data",
			data: []byte{0xba, 0x00, 0x10, 0x00, 0x01},
		},
		{
			name: "byte string length exceeds remaining data",
			data: []byte{0x5a, 0xff, 0xff, 0xff, 0xff},
		},
		{
			name: "text string length exceeds remaining data",
			data: []byte{0x7a, 0xff, 0xff, 0xff, 0xff},
		},
		{
			name: "negative integer overflow",
			data: []byte{0x3b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff},
		},
		{
			name: "nesting exceeds maximum depth",
			data: nestedCBORArrays(65),
		},
	}

	store := mock.NewCAS()
	parser := cas.NewIPLDParser(store)
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mhash, _ := mh.Sum(tc.data, mh.SHA2_256, -1)
			c := cid.NewCidV1(0x71, mhash)
			if _, err := parser.ParseBlock(c, tc.data); err == nil {
				t.Fatal("ParseBlock succeeded, want error")
			}
		})
	}
}

func TestIPLDParserFollowLinkUsesContext(t *testing.T) {
	targetData := []byte("target data")
	targetCID, err := newPayloadCID(targetData)
	if err != nil {
		t.Fatalf("newPayloadCID target failed: %v", err)
	}

	rootData := []byte(fmt.Sprintf(`{"link": {"/": %q}}`, targetCID.String()))
	rootHash, err := mh.Sum(rootData, mh.SHA2_256, -1)
	if err != nil {
		t.Fatalf("root hash failed: %v", err)
	}
	rootCID := cid.NewCidV1(0x0201, rootHash)

	store := &recordingReader{
		blocks: map[string][]byte{
			rootCID.String():   rootData,
			targetCID.String(): targetData,
		},
	}
	parser := cas.NewIPLDParser(store)
	ctx := context.WithValue(context.Background(), contextKey("test"), "marker")
	node, err := parser.FollowLink(ctx, rootCID, "link")
	if err != nil {
		t.Fatalf("FollowLink failed: %v", err)
	}
	if string(node.Data) != string(targetData) {
		t.Fatalf("FollowLink data = %q, want %q", node.Data, targetData)
	}
	if store.gets != 2 {
		t.Fatalf("Get calls = %d, want 2", store.gets)
	}
	if store.nilContext {
		t.Fatal("FollowLink passed nil context to CAS")
	}
	if !store.sawMarker {
		t.Fatal("FollowLink did not pass caller context to CAS")
	}
}

func TestIPLDParserFollowLinkNilContextUsesBackground(t *testing.T) {
	store := &recordingReader{blocks: map[string][]byte{}}
	parser := cas.NewIPLDParser(store)
	_, _ = parser.FollowLink(nil, mustDecodeCID("bafkqaaa"), "missing")
	if store.nilContext {
		t.Fatal("FollowLink passed nil context to CAS")
	}
}

func TestIPLDResolveLink(t *testing.T) {
	store := mock.NewCAS()
	parser := cas.NewIPLDParser(store)

	// Create node with links
	node := &cas.IPLDNode{
		Fields: make(map[string]any),
		Links: []cas.LinkInfo{
			{Name: "data", CID: mustDecodeCID("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")},
			{Name: "meta", CID: mustDecodeCID("bafybeihdwdcefgh4dqkjv67ozcmhfqp46vd4swimutfj3lkq2qhwbg64vc")},
		},
	}

	// Resolve existing link
	c, ok := parser.ResolveLink(node, "data")
	if !ok {
		t.Error("Expected to find 'data' link")
	}
	if !c.Equals(mustDecodeCID("bafybeigdyrzt5sfp7udm7hu76uh7y26nf3efuylqabf3oclgtqy55fbzdi")) {
		t.Error("CID mismatch for 'data' link")
	}

	// Resolve non-existing link
	_, ok = parser.ResolveLink(node, "nonexistent")
	if ok {
		t.Error("Should not find 'nonexistent' link")
	}
}

func TestCreateDAGJSON(t *testing.T) {
	// Create DAG-JSON block
	fields := map[string]any{
		"name":  "test",
		"value": 42,
	}

	data, c, err := cas.CreateDAGJSON(fields)
	if err != nil {
		t.Fatalf("CreateDAGJSON failed: %v", err)
	}

	if len(data) == 0 {
		t.Error("Data should not be empty")
	}

	if !c.Defined() {
		t.Error("CID should be defined")
	}
}

func TestCreateRawBlock(t *testing.T) {
	data := []byte("raw data")

	rawData, c, err := cas.CreateRawBlock(data)
	if err != nil {
		t.Fatalf("CreateRawBlock failed: %v", err)
	}

	if string(rawData) != "raw data" {
		t.Error("Raw data should be preserved")
	}

	if !c.Defined() {
		t.Error("CID should be defined")
	}
}

func mustDecodeCID(s string) cid.Cid {
	c, err := cid.Decode(s)
	if err != nil {
		panic(err)
	}
	return c
}

func nestedCBORArrays(depth int) []byte {
	data := make([]byte, 0, depth+1)
	for i := 0; i < depth; i++ {
		data = append(data, 0x81)
	}
	data = append(data, 0x00)
	return data
}

type contextKey string

type recordingReader struct {
	blocks     map[string][]byte
	gets       int
	nilContext bool
	sawMarker  bool
}

func (r *recordingReader) Get(ctx context.Context, c cid.Cid) ([]byte, error) {
	r.gets++
	if ctx == nil {
		r.nilContext = true
	} else if ctx.Value(contextKey("test")) == "marker" {
		r.sawMarker = true
	}
	data, ok := r.blocks[c.String()]
	if !ok {
		return nil, fmt.Errorf("missing block %s", c)
	}
	return data, nil
}

func (r *recordingReader) Has(ctx context.Context, c cid.Cid) (bool, error) {
	if ctx == nil {
		r.nilContext = true
	}
	_, ok := r.blocks[c.String()]
	return ok, nil
}

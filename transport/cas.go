package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	transportcap "github.com/dewebprotocol/malt-client/transport/capability"
	cid "github.com/ipfs/go-cid"
)

const (
	CASPutBatchProfile = "cas.put-batch/v0alpha1"
	CASHasBatchProfile = "cas.has-batch/v0alpha1"
	MaxCASBatchBlocks  = 4096
	MaxCASBatchBytes   = 64 << 20
)

type Block = transportcap.Block
type PutBatchResult = transportcap.PutResult

// PutBatchMeasurement is the exact HTTP-message boundary for one CAS batch.
// RequestWireBytes and ResponseWireBytes are kept directionally separate;
// RoundTripNS starts immediately before the HTTP exchange and ends after the
// complete bounded response body has arrived. Local response/CID validation is
// deliberately outside that duration.
type PutBatchMeasurement struct {
	Results           []PutBatchResult
	RoundTripNS       uint64
	RequestWireBytes  uint64
	ResponseWireBytes uint64
}

type HasBatchResult struct {
	CID     cid.Cid
	Present bool
}

// PutBatch stores an ordered, bounded group of immutable blocks. The runtime
// recomputes every returned CID before exposing the result.
func (c *Client) PutBatch(ctx context.Context, blocks []Block) ([]PutBatchResult, error) {
	measurement, err := c.PutBatchMeasured(ctx, blocks)
	if err != nil {
		return nil, err
	}
	return measurement.Results, nil
}

// PutBatchMeasured stores a batch and exposes exact request/response body
// sizes plus the HTTP round trip for evaluator phase accounting. Ordinary
// application callers should continue to use PutBatch.
func (c *Client) PutBatchMeasured(ctx context.Context, blocks []Block) (PutBatchMeasurement, error) {
	if len(blocks) == 0 {
		return PutBatchMeasurement{Results: []PutBatchResult{}}, nil
	}
	if len(blocks) > MaxCASBatchBlocks {
		return PutBatchMeasurement{}, fmt.Errorf("CAS batch must contain 1 to %d blocks", MaxCASBatchBlocks)
	}
	total := 0
	wants := make([]cid.Cid, len(blocks))
	wireBlocks := make([]struct {
		Codec uint64 `json:"codec,omitempty"`
		Data  []byte `json:"data"`
	}, len(blocks))
	for i, block := range blocks {
		if len(block.Data) > MaxCASBatchBytes || total > MaxCASBatchBytes-len(block.Data) {
			return PutBatchMeasurement{}, fmt.Errorf("CAS batch exceeds %d decoded bytes", MaxCASBatchBytes)
		}
		total += len(block.Data)
		want, err := clientcas.CIDForBlock(clientcas.Block{Data: block.Data, Codec: block.Codec})
		if err != nil {
			return PutBatchMeasurement{}, fmt.Errorf("compute CAS batch CID %d before persistence: %w", i, err)
		}
		wants[i] = want
		wireBlocks[i].Codec = block.Codec
		wireBlocks[i].Data = block.Data
	}
	request := struct {
		Profile string `json:"profile"`
		Blocks  any    `json:"blocks"`
	}{Profile: CASPutBatchProfile, Blocks: wireBlocks}
	requestData, err := json.Marshal(request)
	if err != nil {
		return PutBatchMeasurement{}, fmt.Errorf("encode CAS put-batch request: %w", err)
	}
	u, err := c.endpoint(c.nativeRoute("/v1/cas/batch"))
	if err != nil {
		return PutBatchMeasurement{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(requestData))
	if err != nil {
		return PutBatchMeasurement{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	started := time.Now()
	httpResponse, err := c.send(httpRequest, c.bucketID != "")
	if err != nil {
		return PutBatchMeasurement{}, err
	}
	defer httpResponse.Body.Close()
	if httpResponse.StatusCode < 200 || httpResponse.StatusCode >= 300 {
		return PutBatchMeasurement{}, c.responseError(httpResponse)
	}
	responseData, err := readBounded(httpResponse.Body, c.maxJSONResponseBytes, "Gateway CAS put-batch response")
	roundTripNS := casDurationNS(time.Since(started))
	if err != nil {
		return PutBatchMeasurement{}, err
	}
	var response struct {
		Profile string `json:"profile"`
		Results []struct {
			CID    string `json:"cid"`
			Status string `json:"status"`
		} `json:"results"`
	}
	if err := json.Unmarshal(responseData, &response); err != nil {
		return PutBatchMeasurement{}, fmt.Errorf("decode Gateway CAS put-batch response: %w", err)
	}
	if response.Profile != CASPutBatchProfile || len(response.Results) != len(blocks) {
		return PutBatchMeasurement{}, fmt.Errorf("%w: invalid CAS put-batch response", clientcas.ErrCorruptedBlock)
	}
	results := make([]clientcas.PutResult, len(blocks))
	for i, raw := range response.Results {
		got, err := cid.Parse(raw.CID)
		if err != nil {
			return PutBatchMeasurement{}, fmt.Errorf("%w: decode CAS batch result %d: %v", clientcas.ErrCorruptedBlock, i, err)
		}
		want := wants[i]
		if !got.Equals(want) {
			return PutBatchMeasurement{}, fmt.Errorf("%w: CAS batch result %d returned CID %s, want %s", clientcas.ErrCorruptedBlock, i, got, want)
		}
		status := clientcas.PutStatus(raw.Status)
		if !transportcap.IsValidPutStatus(status) {
			return PutBatchMeasurement{}, fmt.Errorf("%w: CAS batch result %d has unsupported status %q", clientcas.ErrCorruptedBlock, i, raw.Status)
		}
		results[i] = clientcas.PutResult{CID: got, Status: status}
	}
	return PutBatchMeasurement{
		Results: results, RoundTripNS: roundTripNS,
		RequestWireBytes: uint64(len(requestData)), ResponseWireBytes: uint64(len(responseData)),
	}, nil
}

func casDurationNS(value time.Duration) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}

// HasBatch checks an ordered group of immutable CIDs and rejects reordered or
// otherwise malformed gateway responses.
func (c *Client) HasBatchDetailed(ctx context.Context, keys []cid.Cid) ([]HasBatchResult, error) {
	if len(keys) == 0 {
		return []HasBatchResult{}, nil
	}
	if len(keys) > MaxCASBatchBlocks {
		return nil, fmt.Errorf("CAS has batch must contain 1 to %d CIDs", MaxCASBatchBlocks)
	}
	rawKeys := make([]string, len(keys))
	for i, key := range keys {
		if !key.Defined() {
			return nil, fmt.Errorf("%w: CAS has batch CID %d is undefined", clientcas.ErrCorruptedBlock, i)
		}
		rawKeys[i] = key.String()
	}
	request := struct {
		Profile string   `json:"profile"`
		CIDs    []string `json:"cids"`
	}{Profile: CASHasBatchProfile, CIDs: rawKeys}
	var response struct {
		Profile string `json:"profile"`
		Results []struct {
			CID     string `json:"cid"`
			Present bool   `json:"present"`
		} `json:"results"`
	}
	if err := c.doNative(ctx, "POST", "/v1/cas/has", nil, request, &response); err != nil {
		return nil, err
	}
	if response.Profile != CASHasBatchProfile || len(response.Results) != len(keys) {
		return nil, fmt.Errorf("%w: invalid CAS has-batch response", clientcas.ErrCorruptedBlock)
	}
	results := make([]HasBatchResult, len(keys))
	for i, raw := range response.Results {
		got, err := cid.Parse(raw.CID)
		if err != nil {
			return nil, fmt.Errorf("%w: CAS has-batch result %d returned an invalid CID: %v", clientcas.ErrCorruptedBlock, i, err)
		}
		if !got.Equals(keys[i]) {
			return nil, fmt.Errorf("%w: CAS has-batch result %d returned CID %s, want %s", clientcas.ErrCorruptedBlock, i, got, keys[i])
		}
		results[i] = HasBatchResult{CID: got, Present: raw.Present}
	}
	return results, nil
}

// HasBatch is the compact compatibility surface consumed by streaming CAS
// writers. Use HasBatchDetailed when ordered CIDs are useful to the caller.
func (c *Client) HasBatch(ctx context.Context, keys []cid.Cid) ([]bool, error) {
	detailed, err := c.HasBatchDetailed(ctx, keys)
	if err != nil {
		return nil, err
	}
	result := make([]bool, len(detailed))
	for i := range detailed {
		result[i] = detailed[i].Present
	}
	return result, nil
}

package merkledag

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	cid "github.com/ipfs/go-cid"
	car "github.com/ipld/go-car/v2"
)

const (
	// The decoded evidence profile is capped at 32 MiB. This additional
	// envelope allowance covers one CARv1 header, section varints, and bounded
	// CIDs without inheriting the transport's larger generic blob limit.
	maxMerkleDAGCARBytes      = maxMerkleDAGEvidenceRaw + (2 << 20)
	maxMerkleDAGCARHeader     = 1024
	maxMerkleDAGCARSection    = maxMerkleDAGReadBytes + 512
	maxMerkleDAGCARCIDBytes   = 256
	MerkleDAGCARReadMediaType = "application/vnd.ipld.car; version=1"
)

// VerifyMerkleDAGCARRead validates a raw CARv1 response and performs local
// path/payload replay. Duplicate blocks are rejected rather than deduplicated,
// and every included block must actually be consumed by the selected read.
func VerifyMerkleDAGCARRead(ctx context.Context, request MerkleDAGReadRequest, encoded []byte) (*VerifiedReadResult, error) {
	if err := validateMerkleDAGReadRequest(request); err != nil {
		return nil, err
	}
	if request.Offset != nil || request.Length != nil {
		return nil, fmt.Errorf("Merkle-DAG CAR read supports only a complete payload")
	}
	if len(encoded) == 0 {
		return nil, fmt.Errorf("Merkle-DAG CAR response is empty")
	}
	if len(encoded) > maxMerkleDAGCARBytes {
		return nil, fmt.Errorf("Merkle-DAG CAR exceeds %d-byte profile limit", maxMerkleDAGCARBytes)
	}
	requestedRoot, err := cid.Parse(request.Root)
	if err != nil {
		return nil, fmt.Errorf("invalid caller-selected Merkle-DAG root: %w", err)
	}
	decodeStarted := time.Now()
	reader, err := car.NewBlockReader(
		bytes.NewReader(encoded),
		car.MaxAllowedHeaderSize(maxMerkleDAGCARHeader),
		car.MaxAllowedSectionSize(maxMerkleDAGCARSection),
		car.ZeroLengthSectionAsEOF(false),
	)
	if err != nil {
		return nil, fmt.Errorf("decode Merkle-DAG CAR header: %w", err)
	}
	if reader.Version != 1 {
		return nil, fmt.Errorf("Merkle-DAG response must be CARv1, got version %d", reader.Version)
	}
	if len(reader.Roots) != 1 || !reader.Roots[0].Equals(requestedRoot) {
		return nil, fmt.Errorf("Merkle-DAG CAR must contain exactly the caller-selected root %s", requestedRoot)
	}

	evidence := make([]MerkleDAGBlock, 0, 64)
	seen := make(map[string]struct{})
	totalBytes := 0
	for {
		block, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("decode Merkle-DAG CAR block %d: %w", len(evidence), err)
		}
		if len(evidence) >= maxMerkleDAGEvidence {
			return nil, fmt.Errorf("Merkle-DAG CAR exceeds %d-block profile limit", maxMerkleDAGEvidence)
		}
		key := block.Cid()
		if key.ByteLen() > maxMerkleDAGCARCIDBytes {
			return nil, fmt.Errorf("Merkle-DAG CAR block CID exceeds %d-byte profile limit", maxMerkleDAGCARCIDBytes)
		}
		if !isSupportedMerkleDAGCodec(key.Type()) {
			return nil, fmt.Errorf("unsupported Merkle-DAG CAR codec %d", key.Type())
		}
		if _, duplicate := seen[key.KeyString()]; duplicate {
			return nil, fmt.Errorf("duplicate Merkle-DAG CAR block %s", key)
		}
		seen[key.KeyString()] = struct{}{}
		data := block.RawData()
		if len(data) > maxMerkleDAGEvidenceRaw-totalBytes {
			return nil, fmt.Errorf("Merkle-DAG CAR exceeds %d decoded bytes", maxMerkleDAGEvidenceRaw)
		}
		totalBytes += len(data)
		evidence = append(evidence, MerkleDAGBlock{
			CID: key.String(), Codec: key.Type(), Data: append([]byte(nil), data...),
		})
	}
	if len(evidence) == 0 {
		return nil, fmt.Errorf("Merkle-DAG CAR contains no blocks")
	}
	decodeDuration := time.Since(decodeStarted)
	dag, err := newEvidenceDAG(evidence)
	if err != nil {
		return nil, err
	}
	result, err := readLocallyVerified(ctx, dag, request)
	if err != nil {
		return nil, err
	}
	if err := dag.requireAllUsed(); err != nil {
		return nil, err
	}
	localMetrics := result.Metrics
	cidVerifyNS, payloadBindingNS := dag.verificationDurations()
	result.Metrics = VerifiedReadMetrics{
		CriticalSequentialRounds: 1,
		BlockLoadCalls:           dag.blockLoadCount(),
		BlocksVerified:           uint64(len(evidence)),
		CARBytes:                 uint64(len(encoded)),
		CARBlocks:                uint64(len(evidence)),
		CARDecodeDurationNS:      durationNanoseconds(decodeDuration),
		CIDVerifyDurationNS:      cidVerifyNS,
		PayloadBindingDurationNS: payloadBindingNS,
		PathReplayDurationNS:     localMetrics.PathReplayDurationNS,
		PayloadReadDurationNS:    localMetrics.PayloadReadDurationNS,
	}
	return result, nil
}

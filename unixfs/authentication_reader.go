package unixfs

import (
	"context"
	"fmt"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	"math"
	"strings"

	"github.com/dewebprotocol/malt-core/auth/engine"
	"github.com/dewebprotocol/malt-core/auth/input"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	cid "github.com/ipfs/go-cid"
)

func (r *verifiedReader) authenticate(ctx context.Context, q protocol.AuthenticationRequest) (*protocol.AuthenticationVerification, error) {
	result, err := r.authentication.Authenticate(ctx, q)
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("nil authentication response")
	}
	valid, err := authentication.Verify(r.authenticationVerifier, q, *result)
	if err != nil {
		return nil, fmt.Errorf("verify typed UnixFS query: %w", err)
	}
	if !valid {
		return nil, fmt.Errorf("typed UnixFS proof does not match selected Root/query")
	}
	return &protocol.AuthenticationVerification{Request: q, Result: *result}, nil
}
func (r *verifiedReader) resolveSegments(ctx context.Context, root cid.Cid, segments []string) (*Resolution, error) {
	key := stagedProjectionKey(root, segments)
	if r.stagedResolutions != nil {
		if cached := r.stagedResolutions[key]; cached != nil {
			return cached, nil
		}
	}
	steps := make([]input.Value, len(segments))
	for i, segment := range segments {
		steps[i] = input.LabelValue([]byte(segment))
		// Public paths reject @-prefixed names. Only the internal payload projection
		// appends this selector; Core receives an explicit system value.
		if segment == "@payload" && i == len(segments)-1 {
			steps[i] = input.SystemValue(input.Payload)
		}
	}
	descriptor, _, err := maltcid.ParseRoot(root)
	if err != nil {
		return nil, err
	}
	if descriptor.Layout == maltcid.Prefix && descriptor.InputRule == uint8(input.BytesSHA256) {
		// Flat and hybrid layouts authenticate one opaque full-path label.
		// A terminal payload projection is a separate explicit system step.
		count := len(segments)
		payload := count > 0 && segments[count-1] == "@payload"
		if payload {
			count--
		}
		steps = []input.Value{}
		if count > 0 {
			steps = append(steps, input.LabelValue([]byte(strings.Join(segments[:count], "/"))))
		}
		if payload {
			steps = append(steps, input.SystemValue(input.Payload))
		}
	}
	q := protocol.AuthenticationRequest{Profile: protocol.AuthenticationPathProfile, Root: root.String(), Steps: steps, Operation: "resolve"}
	verified, err := r.authenticate(ctx, q)
	if err != nil {
		return nil, err
	}
	if verified.Result.AbsentStep != nil {
		return nil, fmt.Errorf("%w: missing component %d", ErrNotFound, *verified.Result.AbsentStep)
	}
	target, err := cid.Decode(verified.Result.Resolved)
	if err != nil {
		return nil, err
	}
	result := &Resolution{Target: target, Authentication: verified}
	if r.stagedResolutions != nil {
		r.stagedResolutions[key] = result
	}
	return result, nil
}
func (r *verifiedReader) readTypedMetadata(ctx context.Context, root cid.Cid) (*protocol.AuthenticationVerification, engine.Metadata, error) {
	selector := input.IndexValue(math.MaxUint64)
	q := protocol.AuthenticationRequest{Profile: protocol.AuthenticationPathProfile, Root: root.String(), Steps: []input.Value{}, Operation: "binding", Input: &selector}
	verified, err := r.authenticate(ctx, q)
	if err != nil {
		return nil, engine.Metadata{}, err
	}
	if verified.Result.Binding == nil {
		return nil, engine.Metadata{}, fmt.Errorf("missing verified structural metadata")
	}
	meta, err := verified.Result.Binding.Proof.RootMetadata()
	if err != nil {
		return nil, meta, err
	}
	if meta.ChunkSize == 0 {
		return nil, meta, fmt.Errorf("UnixFS bytes require a measured Positional ArcSet")
	}
	return verified, meta, nil
}
func (r *verifiedReader) readTypedRange(ctx context.Context, root cid.Cid, start uint64, length *uint64, metadata *protocol.AuthenticationVerification) (*ReadResult, error) {
	var meta engine.Metadata
	var err error
	if metadata == nil {
		metadata, meta, err = r.readTypedMetadata(ctx, root)
	} else {
		// This proof is retained in an internal Stat, but re-bind it rather than
		// treating a caller-mutated metadata field as authoritative.
		selector := input.IndexValue(math.MaxUint64)
		q := protocol.AuthenticationRequest{Profile: protocol.AuthenticationPathProfile, Root: root.String(), Steps: []input.Value{}, Operation: "binding", Input: &selector}
		var valid bool
		valid, err = authentication.Verify(r.authenticationVerifier, q, metadata.Result)
		if err == nil && !valid {
			err = fmt.Errorf("retained metadata does not match selected Root")
		}
		if err == nil {
			meta, err = metadata.Result.Binding.Proof.RootMetadata()
		}
	}
	if err != nil {
		return nil, err
	}
	if meta.ChunkSize == 0 {
		return nil, fmt.Errorf("unmeasured sequence")
	}
	result := &ReadResult{Target: root, Offset: start, TotalSize: meta.TotalSize, ChunkSize: meta.ChunkSize, Authentication: metadata}
	if start >= meta.TotalSize || (length != nil && *length == 0) {
		result.End = start
		return result, nil
	}
	end := meta.TotalSize
	if length != nil {
		end = min(end, saturatingAdd(start, *length))
	}
	q := protocol.AuthenticationRequest{Profile: protocol.AuthenticationPathProfile, Root: root.String(), Steps: []input.Value{}, Operation: "range", Start: &start, End: &end}
	verified, err := r.authenticate(ctx, q)
	if err != nil {
		return nil, err
	}
	if verified.Result.Range == nil || verified.Result.Range.Metadata != meta {
		return nil, fmt.Errorf("range metadata differs from verified file metadata")
	}
	result.End = end
	result.Authentication = verified
	blocks := make(map[string][]byte, len(verified.Result.Range.Segments))
	for i, segment := range verified.Result.Range.Segments {
		key := segment.Target.KeyString()
		data, ok := blocks[key]
		if !ok {
			var err error
			data, err = r.getBoundBlock(ctx, segment.Target)
			if err != nil {
				return nil, err
			}
			blocks[key] = data
		}
		index := start/meta.ChunkSize + uint64(i)
		offset := index * meta.ChunkSize
		expected := min(meta.ChunkSize, meta.TotalSize-offset)
		if uint64(len(data)) != expected {
			return nil, fmt.Errorf("payload chunk length differs from authenticated measurements")
		}
		first := uint64(0)
		if start > offset {
			first = start - offset
		}
		last := min(expected, end-offset)
		result.Body = append(result.Body, data[first:last]...)
	}
	if uint64(len(result.Body)) != end-start {
		return nil, fmt.Errorf("incomplete authenticated byte range")
	}
	return result, nil
}

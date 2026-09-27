// Package writeplan separates local candidate preparation from untrusted remote
// persistence. It owns no application layout, publication, journal or trust.
package writeplan

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/dewebprotocol/malt-core/maltcid"
	"github.com/dewebprotocol/malt-core/protocol"
	cid "github.com/ipfs/go-cid"
)

// Block refers to owner-local bytes. Read must remain usable for an exact retry.
// Each read is CID-checked before any remote write of that block.
type Block struct {
	CID  cid.Cid
	Read func(context.Context) ([]byte, error)
}

func Bytes(key cid.Cid, body []byte) Block {
	frozen := append([]byte(nil), body...)
	return Block{CID: key, Read: func(context.Context) ([]byte, error) { return append([]byte(nil), frozen...), nil }}
}

type Plan struct {
	Base, Root    cid.Cid
	TransactionID string
	Candidates    []protocol.AuthenticationCandidate
	Blocks        []Block
	Required      []cid.Cid
}

type CandidateWriter interface {
	MaterializeAuthentication(context.Context, protocol.AuthenticationCandidate) (cid.Cid, error)
}
type BatchWriter interface {
	MaterializeAuthenticationBatch(context.Context, protocol.AuthenticationBatch) (protocol.AuthenticationReceipt, error)
}

func (p Plan) validate() error {
	if _, _, err := maltcid.ParseRoot(p.Root); err != nil {
		return err
	}
	if p.Base.Defined() {
		if _, _, err := maltcid.ParseRoot(p.Base); err != nil {
			return err
		}
	}
	seen := map[string]int{}
	for i, candidate := range p.Candidates {
		if err := candidate.Validate(); err != nil {
			return err
		}
		if _, ok := seen[candidate.Root]; ok {
			return fmt.Errorf("duplicate planned candidate")
		}
		seen[candidate.Root] = i
	}
	for i, candidate := range p.Candidates {
		check := func(root string) error {
			if position, ok := seen[root]; ok && position >= i {
				return fmt.Errorf("write plan dependency is not child-before-parent")
			}
			return nil
		}
		if err := check(candidate.Previous); err != nil {
			return err
		}
		for _, entry := range candidate.State.Entries {
			if err := check(entry.Target.String()); err != nil {
				return err
			}
		}
	}
	if len(p.Candidates) > 0 && p.Candidates[len(p.Candidates)-1].Root != p.Root.String() {
		return fmt.Errorf("write plan final candidate differs from Root")
	}
	for _, block := range p.Blocks {
		if !block.CID.Defined() || block.Read == nil {
			return fmt.Errorf("write plan has an unavailable block")
		}
	}
	return nil
}

// Persist installs child-before-parent candidates with exact acknowledgements.
// This initial/import path does not claim a cross-object atomic receipt.
func (p Plan) Persist(ctx context.Context, blocks BlockWriter, remote CandidateWriter) error {
	if err := p.validate(); err != nil {
		return err
	}
	if remote == nil {
		return fmt.Errorf("write plan requires a candidate writer")
	}
	if err := p.persistBlocks(ctx, blocks); err != nil {
		return err
	}
	for _, candidate := range p.Candidates {
		outbound, err := cloneCandidate(candidate)
		if err != nil {
			return err
		}
		got, err := remote.MaterializeAuthentication(ctx, outbound)
		if err != nil {
			return err
		}
		if got.String() != candidate.Root {
			return fmt.Errorf("remote graph substituted planned Root %s with %s", candidate.Root, got)
		}
	}
	return nil
}

// PersistBatch binds the exact journal transaction, base and final Root to the
// durable authentication receipt. Uploaded CAS blocks have a separate boundary.
func (p Plan) PersistBatch(ctx context.Context, blocks BlockWriter, remote BatchWriter) (protocol.AuthenticationReceipt, error) {
	if err := p.validate(); err != nil {
		return protocol.AuthenticationReceipt{}, err
	}
	batch := protocol.AuthenticationBatch{Profile: protocol.AuthenticationBatchProfile, TransactionID: p.TransactionID, Base: p.Base.String(), Root: p.Root.String(), Candidates: p.Candidates}
	if err := batch.Validate(); err != nil {
		return protocol.AuthenticationReceipt{}, err
	}
	if remote == nil {
		return protocol.AuthenticationReceipt{}, fmt.Errorf("write plan requires a batch writer")
	}
	if err := p.persistBlocks(ctx, blocks); err != nil {
		return protocol.AuthenticationReceipt{}, err
	}
	// Keep the expected request independent of an adapter's mutable argument.
	data, err := json.Marshal(batch)
	if err != nil {
		return protocol.AuthenticationReceipt{}, err
	}
	var outbound protocol.AuthenticationBatch
	if err := json.Unmarshal(data, &outbound); err != nil {
		return protocol.AuthenticationReceipt{}, err
	}
	receipt, err := remote.MaterializeAuthenticationBatch(ctx, outbound)
	if err != nil {
		return protocol.AuthenticationReceipt{}, err
	}
	if err := receipt.Validate(batch); err != nil {
		return protocol.AuthenticationReceipt{}, err
	}
	return receipt, nil
}

package main

import (
	"context"
	"fmt"

	clientcas "github.com/dewebprotocol/malt-client/internal/cas"
	"github.com/dewebprotocol/malt-client/internal/evaluation/gatewaytransport"
	"github.com/dewebprotocol/malt-client/transport"
	"github.com/dewebprotocol/malt-core/auth/commitment/kzg"
	"github.com/dewebprotocol/malt-core/derivation"
	"github.com/dewebprotocol/malt-core/engine"
	"github.com/dewebprotocol/malt-core/maltcid"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	cid "github.com/ipfs/go-cid"
)

const (
	flatSentinelCause = "canonical-empty-setup:flat-layout-sentinel"
	flatSentinelJSON  = `{"schema_version":"malt-eval-rq3-flat-layout/v1"}`
)

// The unmeasured oracle computes expected Roots before each Gateway request.
// Core Writers own a closed immutable node DAG: retaining only the newest
// Writer shares unchanged descendants without retaining obsolete ancestors.
type flatRootOracle struct {
	writer *authentication.Writer
	root   cid.Cid
}

func newFlatRootOracle(ctx context.Context, initial []gatewaytransport.FlatPrefixChange) (*flatRootOracle, error) {
	if len(initial) == 0 {
		return nil, fmt.Errorf("flat oracle requires an initial state")
	}
	scheme, err := kzg.NewScheme()
	if err != nil {
		return nil, err
	}
	profiles := engine.NewRegistry()
	if err := profiles.Register(scheme); err != nil {
		return nil, err
	}
	state := engine.State{Descriptor: maltcid.RootDescriptor{Layout: maltcid.Prefix, DerivationProfile: uint8(derivation.SHA256), Profile: maltcid.KZG4096}, Entries: make([]engine.Entry, len(initial))}
	for i, change := range initial {
		if len(change.Label) == 0 || change.Before.Defined() || !change.After.Defined() {
			return nil, fmt.Errorf("invalid flat oracle initial binding %d", i)
		}
		state.Entries[i] = engine.Entry{Label: change.Label, Target: change.After}
	}
	writer, err := authentication.BuildWriter(ctx, engine.New(profiles), state)
	if err != nil {
		return nil, err
	}
	return &flatRootOracle{writer: writer, root: writer.Root()}, nil
}
func (o *flatRootOracle) apply(ctx context.Context, changes []gatewaytransport.FlatPrefixChange) (cid.Cid, error) {
	if o == nil || o.writer == nil {
		return cid.Undef, fmt.Errorf("flat oracle is not initialized")
	}
	delta := authentication.Delta{Changes: make([]engine.Change, len(changes))}
	for i, change := range changes {
		if len(change.Label) == 0 {
			return cid.Undef, fmt.Errorf("flat change requires an explicit label")
		}
		delta.Changes[i] = engine.Change{Label: change.Label, Before: change.Before, After: change.After}
	}
	writer, err := o.writer.Apply(ctx, delta)
	if err != nil {
		return cid.Undef, err
	}
	o.writer, o.root = writer, writer.Root()
	return o.root, nil
}

// fullFlatRoot reconstructs the complete post-image from live source files,
// without using the mutation deltas or any Gateway response/state.
func fullFlatRoot(ctx context.Context, files map[string]logicalFile) (cid.Cid, error) {
	initial, err := directFlatSnapshotChanges(files)
	if err != nil {
		return cid.Undef, err
	}
	oracle, err := newFlatRootOracle(ctx, initial)
	if err != nil {
		return cid.Undef, err
	}
	return oracle.root, nil
}
func verifyFlatGatewayRoot(operation string, expected, observed cid.Cid) error {
	if !expected.Defined() || !observed.Defined() || !expected.Equals(observed) {
		return fmt.Errorf("%s Gateway Root differs from independent per-commit flat oracle", operation)
	}
	return nil
}
func flatSentinelBlock() classifiedBlock {
	return classifiedBlock{block: transport.Block{Codec: cid.DagJSON, Data: []byte(flatSentinelJSON)}, category: categoryCASMetadata, cause: flatSentinelCause, suffix: "flat-layout-sentinel"}
}
func flatSentinelEntry() (engine.Entry, error) {
	block := flatSentinelBlock()
	key, err := clientcas.CIDForBlock(block.block)
	return engine.Entry{Label: []byte("@malt-eval/layout"), Target: key}, err
}
func flatInput(filePath string, mode bool) []byte {
	prefix := "rq3/files/"
	if mode {
		prefix = "rq3/modes/"
	}
	return []byte(prefix + filePath)
}

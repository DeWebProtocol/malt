package protocol

import (
	"fmt"

	"github.com/dewebprotocol/malt/auth/arcset"
	"github.com/dewebprotocol/malt/mutation"
)

const (
	ClientRootMaterializationProfile    = mutation.ClientRootMaterializationProfile
	MaxClientRootMaterializationEntries = 1 << 22
)

// ClientRootMaterialization is the JSON projection of the root-bound
// proof-serving state exported by the browser writer.
type ClientRootMaterialization struct {
	Profile string                   `json:"profile"`
	Maps    []MapMaterializationWire `json:"maps"`
}

type MapMaterializationWire struct {
	TransitionID string                     `json:"transition_id"`
	Root         string                     `json:"root"`
	Entries      []MaterializationEntryWire `json:"entries"`
}

type MaterializationEntryWire struct {
	Path string `json:"path"`
	CID  string `json:"cid"`
}

func NewClientRootMaterialization(bundle mutation.ClientRootBundle, value mutation.ClientRootMaterialization) (ClientRootMaterialization, error) {
	canonical, err := mutation.NewClientRootMaterialization(bundle, value)
	if err != nil {
		return ClientRootMaterialization{}, err
	}
	maps := make([]MapMaterializationWire, len(canonical.Maps))
	totalEntries := 0
	for index, materialization := range canonical.Maps {
		entries := materialization.Entries.Entries()
		totalEntries += len(entries)
		if totalEntries > MaxClientRootMaterializationEntries {
			return ClientRootMaterialization{}, fmt.Errorf("client-root materialization entry count exceeds %d", MaxClientRootMaterializationEntries)
		}
		wireEntries := make([]MaterializationEntryWire, len(entries))
		for entryIndex, entry := range entries {
			wireEntries[entryIndex] = MaterializationEntryWire{
				Path: entry.Coordinate.String(), CID: entry.Target.CID().String(),
			}
		}
		maps[index] = MapMaterializationWire{
			TransitionID: materialization.TransitionID,
			Root:         materialization.Root.String(),
			Entries:      wireEntries,
		}
	}
	return ClientRootMaterialization{Profile: ClientRootMaterializationProfile, Maps: maps}, nil
}

func (w ClientRootMaterialization) Core(bundle mutation.ClientRootBundle) (mutation.ClientRootMaterialization, error) {
	if w.Profile != ClientRootMaterializationProfile {
		return mutation.ClientRootMaterialization{}, fmt.Errorf("client-root materialization profile must be %q", ClientRootMaterializationProfile)
	}
	if len(w.Maps) > MaxClientRootTransitions {
		return mutation.ClientRootMaterialization{}, fmt.Errorf("client-root materialization map count exceeds %d", MaxClientRootTransitions)
	}
	maps := make([]mutation.MapMaterialization, len(w.Maps))
	totalEntries := 0
	for index, materialization := range w.Maps {
		root, err := parseCanonicalCID(materialization.Root, fmt.Sprintf("maps[%d].root", index))
		if err != nil {
			return mutation.ClientRootMaterialization{}, err
		}
		totalEntries += len(materialization.Entries)
		if totalEntries > MaxClientRootMaterializationEntries {
			return mutation.ClientRootMaterialization{}, fmt.Errorf("client-root materialization entry count exceeds %d", MaxClientRootMaterializationEntries)
		}
		entries := make([]arcset.ArcEntry, len(materialization.Entries))
		for entryIndex, entry := range materialization.Entries {
			if len(entry.Path) == 0 || len(entry.Path) > MaxClientRootPathBytes {
				return mutation.ClientRootMaterialization{}, fmt.Errorf("maps[%d].entries[%d].path length is outside 1..%d", index, entryIndex, MaxClientRootPathBytes)
			}
			coordinate, err := arcset.NewMapCoordinate(entry.Path)
			if err != nil {
				return mutation.ClientRootMaterialization{}, fmt.Errorf("maps[%d].entries[%d].path: %w", index, entryIndex, err)
			}
			target, err := parseCanonicalCID(entry.CID, fmt.Sprintf("maps[%d].entries[%d].cid", index, entryIndex))
			if err != nil {
				return mutation.ClientRootMaterialization{}, err
			}
			entries[entryIndex] = arcset.ArcEntry{Coordinate: coordinate, Target: arcset.NewUnknownTarget(target)}
		}
		canonicalEntries, err := arcset.NewCanonicalArcSet(arcset.KindMap, entries)
		if err != nil {
			return mutation.ClientRootMaterialization{}, fmt.Errorf("maps[%d].entries: %w", index, err)
		}
		if canonicalEntries.Len() != len(entries) {
			return mutation.ClientRootMaterialization{}, fmt.Errorf("maps[%d].entries contain duplicate coordinates", index)
		}
		maps[index] = mutation.MapMaterialization{
			TransitionID: materialization.TransitionID, Root: root, Entries: canonicalEntries,
		}
	}
	return mutation.NewClientRootMaterialization(bundle, mutation.ClientRootMaterialization{Profile: w.Profile, Maps: maps})
}

func (w ClientRootMaterialization) Validate(bundle mutation.ClientRootBundle) error {
	_, err := w.Core(bundle)
	return err
}

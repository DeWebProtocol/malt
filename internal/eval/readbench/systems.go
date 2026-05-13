package readbench

import (
	"fmt"
	"strings"
)

// SystemName identifies a read benchmark implementation under test.
type SystemName string

const (
	SystemMALTFlat  SystemName = "maltflat"
	SystemMerkleDAG SystemName = "merkledag"
	SystemHAMT      SystemName = "hamt"
)

// DefaultSystemsCSV is the default comma-separated read benchmark system list.
const DefaultSystemsCSV = "maltflat,merkledag,hamt"

// DefaultSystems returns the default read benchmark system order.
func DefaultSystems() []SystemName {
	return []SystemName{SystemMALTFlat, SystemMerkleDAG, SystemHAMT}
}

// ParseSystemsCSV parses a comma-separated system list.
func ParseSystemsCSV(raw string) ([]SystemName, error) {
	if strings.TrimSpace(raw) == "" {
		return DefaultSystems(), nil
	}
	parts := strings.Split(raw, ",")
	systems := make([]SystemName, 0, len(parts))
	for _, part := range parts {
		system := SystemName(strings.TrimSpace(part))
		if system == "" {
			continue
		}
		if !knownSystem(system) {
			return nil, fmt.Errorf("unknown system %q", system)
		}
		systems = append(systems, system)
	}
	if len(systems) == 0 {
		return nil, fmt.Errorf("no systems selected")
	}
	return systems, nil
}

func normalizeSystems(systems []SystemName) ([]SystemName, error) {
	if len(systems) == 0 {
		return DefaultSystems(), nil
	}
	normalized := make([]SystemName, 0, len(systems))
	for _, system := range systems {
		if !knownSystem(system) {
			return nil, fmt.Errorf("unknown system %q", system)
		}
		normalized = append(normalized, system)
	}
	return normalized, nil
}

func knownSystem(system SystemName) bool {
	switch system {
	case SystemMALTFlat, SystemMerkleDAG, SystemHAMT:
		return true
	default:
		return false
	}
}

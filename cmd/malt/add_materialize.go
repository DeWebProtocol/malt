package main

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/dewebprotocol/malt/api/http"
	"github.com/dewebprotocol/malt/layout/unixfs/manifest"
	daemonclient "github.com/dewebprotocol/malt/sdk/client"
	cid "github.com/ipfs/go-cid"
)

type addMaterializeResult struct {
	Key              cid.Cid
	ArcCount         int
	Descendants      map[string]cid.Cid
	ImmutableObjects int
	MALTObjects      int
	MALTMaps         int
	MALTLists        int
	ArcSets          int
	Arcs             int
}

func loadExistingCurrentTree(ctx context.Context, daemon *daemonclient.Client, casClient addCASClient, rootCID string) (*addNode, error) {
	rootStat, err := daemon.Stat(ctx, rootCID, "")
	if err != nil {
		return nil, err
	}
	if rootStat.Key != rootCID {
		rootCID = rootStat.Key
	}
	if rootStat.Kind != "dir" {
		return nil, fmt.Errorf("current root must be directory, got %q", rootStat.Kind)
	}
	return loadCurrentDirRecursive(ctx, daemon, casClient, rootCID, "", rootStat)
}

func loadCurrentDirRecursive(ctx context.Context, daemon *daemonclient.Client, casClient addCASClient, root string, currentPath string, stat *httpapi.PathStatResponse) (*addNode, error) {
	node := newDirNode()
	node.Changed = false
	node.StorageKind = stat.StorageKind
	keyCID, err := cid.Decode(stat.Key)
	if err != nil {
		return nil, fmt.Errorf("decode directory key %q: %w", stat.Key, err)
	}
	node.Key = keyCID

	if strings.TrimSpace(stat.Payload) == "" {
		return node, nil
	}
	payloadCID, err := cid.Decode(stat.Payload)
	if err != nil {
		return nil, fmt.Errorf("decode directory payload %q: %w", stat.Payload, err)
	}
	raw, err := casClient.Get(ctx, payloadCID)
	if err != nil {
		return nil, fmt.Errorf("fetch directory manifest %s: %w", stat.Payload, err)
	}
	m, err := manifest.ParseDirectoryJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("parse directory manifest %s: %w", stat.Payload, err)
	}
	for _, childName := range m.Entries {
		childPath := childName
		if currentPath != "" {
			childPath = path.Join(currentPath, childName)
		}
		childStat, err := daemon.Stat(ctx, root, childPath)
		if err != nil {
			return nil, err
		}
		switch childStat.Kind {
		case "dir":
			childDir, err := loadCurrentDirRecursive(ctx, daemon, casClient, root, childPath, childStat)
			if err != nil {
				return nil, err
			}
			node.Children[childName] = childDir
		case "file":
			childKey, err := cid.Decode(childStat.Key)
			if err != nil {
				return nil, fmt.Errorf("decode file key %q: %w", childStat.Key, err)
			}
			node.Children[childName] = &addNode{
				Kind:        "file",
				StorageKind: childStat.StorageKind,
				Key:         childKey,
				Changed:     false,
			}
		default:
			return nil, fmt.Errorf("unsupported child kind %q at %q", childStat.Kind, childPath)
		}
	}
	return node, nil
}

func materializeDirectory(ctx context.Context, daemon *daemonclient.Client, casClient addCASClient, node *addNode) (*addMaterializeResult, error) {
	return materializeDirectoryWithBatcher(ctx, daemon, asAddCASBatcher(casClient), node)
}

func addMaterializeStats(dst *addMaterializeResult, src *addMaterializeResult) {
	if dst == nil || src == nil {
		return
	}
	dst.ImmutableObjects += src.ImmutableObjects
	dst.MALTObjects += src.MALTObjects
	dst.MALTMaps += src.MALTMaps
	dst.MALTLists += src.MALTLists
	dst.ArcSets += src.ArcSets
	dst.Arcs += src.Arcs
	dst.ArcCount += src.ArcCount
}

func materializeDirectoryWithBatcher(ctx context.Context, daemon *daemonclient.Client, casClient *addCASBatcher, node *addNode) (*addMaterializeResult, error) {
	if node == nil || node.Kind != "dir" {
		return nil, fmt.Errorf("materializeDirectory requires a directory node")
	}

	names := make([]string, 0, len(node.Children))
	for name := range node.Children {
		names = append(names, name)
	}
	slices.Sort(names)

	desc := make(map[string]cid.Cid)
	childKeys := make(map[string]cid.Cid, len(node.Children))
	stats := &addMaterializeResult{}
	for _, name := range names {
		child := node.Children[name]
		if child == nil {
			continue
		}
		if child.Kind == "dir" {
			mat, err := materializeDirectoryWithBatcher(ctx, daemon, casClient, child)
			if err != nil {
				return nil, err
			}
			addMaterializeStats(stats, mat)
			child.Key = mat.Key
			child.Changed = false
			childKeys[name] = mat.Key
			desc[name] = mat.Key
			for rel, childKey := range mat.Descendants {
				desc[path.Join(name, rel)] = childKey
			}
			continue
		}
		childKeys[name] = child.Key
		desc[name] = child.Key
	}

	if !node.Changed && node.Key.Defined() {
		return &addMaterializeResult{
			Key:              node.Key,
			ArcCount:         stats.ArcCount,
			Descendants:      desc,
			ImmutableObjects: stats.ImmutableObjects,
			MALTObjects:      stats.MALTObjects,
			MALTMaps:         stats.MALTMaps,
			MALTLists:        stats.MALTLists,
			ArcSets:          stats.ArcSets,
			Arcs:             stats.Arcs,
		}, nil
	}

	payloadBytes, err := (&manifest.DirectoryManifest{Entries: names}).MarshalJSON()
	if err != nil {
		return nil, fmt.Errorf("marshal directory manifest: %w", err)
	}
	payloadCID, err := casClient.Put(ctx, payloadBytes)
	if err != nil {
		return nil, fmt.Errorf("upload directory manifest: %w", err)
	}
	if err := casClient.Flush(ctx); err != nil {
		return nil, fmt.Errorf("flush directory manifest: %w", err)
	}

	bindings := make(map[string]string, 1+len(childKeys)+len(desc))
	bindings["@payload"] = payloadCID.String()
	for name, key := range childKeys {
		bindings[name] = key.String()
	}
	for rel, key := range desc {
		if !strings.Contains(rel, "/") {
			continue
		}
		bindings[rel] = key.String()
	}

	resp, err := daemon.CreateRootStructure(ctx, bindings)
	if err != nil {
		return nil, err
	}
	rootCID, err := cid.Decode(resp.Root)
	if err != nil {
		return nil, fmt.Errorf("decode created map root: %w", err)
	}
	node.Key = rootCID
	node.Changed = false
	node.StorageKind = "map"
	arcCount := countDefinedBindings(bindings)
	return &addMaterializeResult{
		Key:              rootCID,
		ArcCount:         stats.ArcCount + arcCount,
		Descendants:      desc,
		ImmutableObjects: stats.ImmutableObjects + 1,
		MALTObjects:      stats.MALTObjects + 1,
		MALTMaps:         stats.MALTMaps + 1,
		MALTLists:        stats.MALTLists,
		ArcSets:          stats.ArcSets + 1,
		Arcs:             stats.Arcs + arcCount,
	}, nil
}

func countDefinedBindings(bindings map[string]string) int {
	count := 0
	for _, v := range bindings {
		if strings.TrimSpace(v) != "" {
			count++
		}
	}
	return count
}

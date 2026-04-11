package main

import (
	"context"
	"fmt"
	"os"

	"github.com/dewebprotocol/malt/core/types/arcset"
	"github.com/dewebprotocol/malt/core/writer"
	cid "github.com/ipfs/go-cid"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(updateCmd)
}

var updateCmd = &cobra.Command{
	Use:   "update <root> <path> <target>",
	Short: "Update an arc in a MALT structure",
	Long: `Update (insert/replace/delete) an arc at the given path.
If target is empty, the arc is deleted.

Examples:
  malt update bafy... data/file.txt bafy...
  malt update bafy... old/link ""`,
	Args: cobra.ExactArgs(3),
	RunE: runUpdate,
}

func runUpdate(cmd *cobra.Command, args []string) error {
	node := mustNode()
	defer node.Close()

	rootCid, err := parseCID(args[0])
	if err != nil {
		return err
	}
	path := args[1]
	targetStr := args[2]

	var newTarget cid.Cid
	if targetStr != "" {
		newTarget, err = parseCID(targetStr)
		if err != nil {
			return err
		}
	}

	w := writer.NewWriter(node.SCE(), node.EAT(), nil)
	ctx := context.Background()

	result, err := w.UpdateArc(ctx, "default", rootCid, path, newTarget)
	if err != nil {
		return fmt.Errorf("update failed: %w", err)
	}

	printJSON(map[string]interface{}{
		"old_root":   result.OldRoot.String(),
		"new_root":   result.NewRoot.String(),
		"path":       result.Path,
		"old_target": result.OldTarget.String(),
		"new_target": result.NewTarget.String(),
		"op":         result.Op.String(),
	})

	fmt.Fprintf(os.Stderr, "arc %q updated (op: %s)\n", path, result.Op.String())
	return nil
}

var updateBatchCmd = &cobra.Command{
	Use:   "batch <root> <path=target>...",
	Short: "Batch update multiple arcs",
	Long: `Update multiple arcs in a single operation.
Use path=target pairs. Empty target deletes the arc.

Examples:
  malt batch bafy... a=bafyaaa b=bafybbb c=""`,
	Args: cobra.MinimumNArgs(2),
	RunE: runBatchUpdate,
}

func runBatchUpdate(cmd *cobra.Command, args []string) error {
	node := mustNode()
	defer node.Close()

	rootCid, err := parseCID(args[0])
	if err != nil {
		return err
	}

	updates := make(map[string]cid.Cid)
	for _, pair := range args[1:] {
		eq := 0
		for i := 0; i < len(pair); i++ {
			if pair[i] == '=' {
				eq = i
				break
			}
		}
		if eq == 0 {
			return fmt.Errorf("invalid update pair %q, expected path=target", pair)
		}
		path := pair[:eq]
		targetStr := pair[eq+1:]

		if targetStr == "" {
			updates[path] = cid.Undef
		} else {
			t, err := parseCID(targetStr)
			if err != nil {
				return err
			}
			updates[path] = t
		}
	}

	w := writer.NewWriter(node.SCE(), node.EAT(), nil)
	ctx := context.Background()

	result, err := w.BatchUpdateArcs(ctx, "default", rootCid, updates)
	if err != nil {
		return fmt.Errorf("batch update failed: %w", err)
	}

	fmt.Printf("old_root: %s\n", result.OldRoot)
	fmt.Printf("new_root: %s\n", result.NewRoot)
	fmt.Fprintf(os.Stderr, "updated %d arc(s)\n", len(result.PerArc))
	return nil
}

var createCmd = &cobra.Command{
	Use:   "create <path=target>...",
	Short: "Create a new structure from arcs",
	Long: `Create a new MALT structure from path-to-target pairs.

Examples:
  malt create data=bafyaaa meta=bafybbb`,
	Args: cobra.MinimumNArgs(1),
	RunE: runCreate,
}

func runCreate(cmd *cobra.Command, args []string) error {
	node := mustNode()
	defer node.Close()

	arcs := make(map[string]cid.Cid)
	for _, pair := range args {
		eq := 0
		for i := 0; i < len(pair); i++ {
			if pair[i] == '=' {
				eq = i
				break
			}
		}
		if eq == 0 {
			return fmt.Errorf("invalid pair %q, expected path=target", pair)
		}
		path := pair[:eq]
		targetStr := pair[eq+1:]

		t, err := parseCID(targetStr)
		if err != nil {
			return err
		}
		arcs[path] = t
	}

	w := writer.NewWriter(node.SCE(), node.EAT(), nil)
	ctx := context.Background()
	snapshot := arcset.NewMapFrom(arcs)

	rootCid, err := w.CreateStructure(ctx, "default", snapshot)
	if err != nil {
		return fmt.Errorf("create structure failed: %w", err)
	}

	fmt.Println(rootCid.String())
	fmt.Fprintf(os.Stderr, "structure created with %d arc(s)\n", len(arcs))
	return nil
}

func init() {
	updateCmd.AddCommand(updateBatchCmd)
	updateCmd.AddCommand(createCmd)
}

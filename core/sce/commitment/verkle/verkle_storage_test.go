package verkle

import (
	"fmt"
	"strings"
	"testing"

	verkle "github.com/ethereum/go-verkle"
)

func TestVerkleStorageSize(t *testing.T) {
	benchmarks := []int{10, 50, 100, 256, 512, 1024}

	fmt.Printf("\n%-10s | %-12s | %-10s | %-10s | %-10s\n",
		"Arcs", "Total Size", "Int. Nodes", "Leaves", "Avg/Node")
	fmt.Println(strings.Repeat("-", 70))

	for _, n := range benchmarks {
		arcs := makeTestArcSet(n)
		root := verkle.New()
		for _, entry := range arcs {
			root.Insert(entry.key, entry.value, nil)
		}
		root.Commit()

		totalBytes := 0
		var internalCount, leafCount int

		collectNodeStats(root, &totalBytes, &internalCount, &leafCount)

		totalNodes := internalCount + leafCount
		avgPerNode := float64(totalBytes) / float64(totalNodes)

		fmt.Printf("%-10d | %-12d | %-10d | %-10d | %-10.1f\n",
			n, totalBytes, internalCount, leafCount, avgPerNode)
	}
}

func collectNodeStats(node verkle.VerkleNode, totalBytes *int, internalCount, leafCount *int) {
	switch n := node.(type) {
	case *verkle.InternalNode:
		*internalCount++
		for _, child := range n.Children() {
			collectNodeStats(child, totalBytes, internalCount, leafCount)
		}
		data, err := n.Serialize()
		if err == nil {
			*totalBytes += len(data)
		}
	case *verkle.LeafNode:
		*leafCount++
		data, err := n.Serialize()
		if err == nil {
			*totalBytes += len(data)
		}
	}
}

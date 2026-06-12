package readbench

import "fmt"

// DepthFixture holds file paths and data at a specific directory depth.
type DepthFixture struct {
	Depth     int
	SmallPath string
	LargePath string
	SmallData []byte
	LargeData []byte
}

// MultiDepthFixture creates file entries at every depth level from 1 to MaxDepth.
// This enables measuring resolve latency at different path depths using the
// same underlying directory structure.
type MultiDepthFixture struct {
	MaxDepth   int
	SmallBytes int
	LargeBytes int
	Fixtures   []DepthFixture
}

// NewMultiDepthFixture creates fixtures at depths 1 through maxDepth.
func NewMultiDepthFixture(maxDepth, smallBytes, largeBytes int) MultiDepthFixture {
	if maxDepth < 1 {
		maxDepth = 1
	}
	fixtures := make([]DepthFixture, maxDepth)
	for d := 1; d <= maxDepth; d++ {
		fixtures[d-1] = DepthFixture{
			Depth:     d,
			SmallPath: fixturePath(d, "small.txt"),
			LargePath: fixturePath(d, "large.bin"),
			SmallData: deterministicBytes(fmt.Sprintf("small-d%d", d), smallBytes),
			LargeData: deterministicBytes(fmt.Sprintf("large-d%d", d), largeBytes),
		}
	}
	return MultiDepthFixture{
		MaxDepth:   maxDepth,
		SmallBytes: smallBytes,
		LargeBytes: largeBytes,
		Fixtures:   fixtures,
	}
}

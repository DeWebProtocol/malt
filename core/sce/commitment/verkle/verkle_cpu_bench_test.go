package verkle

import (
	"os"
	"runtime/pprof"
	"testing"

	verkle "github.com/ethereum/go-verkle"
)

func BenchmarkCPUProfile(b *testing.B) {
	arcs := makeTestArcSet(256)

	f, err := os.Create("verkle_cpu.prof")
	if err != nil {
		b.Fatal(err)
	}
	defer f.Close()

	pprof.StartCPUProfile(f)
	defer pprof.StopCPUProfile()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		root := verkle.New()
		for _, entry := range arcs {
			root.Insert(entry.key, entry.value, nil)
		}
		root.Commit()
	}
}

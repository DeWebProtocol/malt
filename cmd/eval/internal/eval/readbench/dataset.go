package readbench

import (
	"fmt"
	"path"
	"slices"
)

// MatrixDatasetConfig describes one logical lookup dataset shared by all read
// systems in read_matrix.
type MatrixDatasetConfig struct {
	Name         string
	FileCount    int
	Depths       []int
	PayloadBytes int
	// PathsPerDepth controls how many independent lookup keys are measured for
	// each depth. Multiple keys reduce single-key radix/HAMT artifacts in
	// latency aggregates.
	PathsPerDepth int
}

// MatrixDatasetFile is one logical source file before materialization into a
// specific representation.
type MatrixDatasetFile struct {
	Path  string
	Data  []byte
	Depth int
	Role  string
}

// MatrixDataset is the shared logical fixture used by read_matrix.
type MatrixDataset struct {
	Name                string
	Files               []MatrixDatasetFile
	Depths              []int
	FileCount           int
	DirectoryCount      int
	PathCount           int
	LogicalPayloadBytes int64
	SmallFileBytes      int
	LargeFileBytes      int
	SmallPaths          map[int]string
	LookupPaths         map[int][]string
}

// NewMatrixDataset builds a deterministic logical file tree for path lookup.
// The measured dataset has one small payload at each requested path depth.
func NewMatrixDataset(cfg MatrixDatasetConfig) (*MatrixDataset, error) {
	if cfg.Name == "" {
		cfg.Name = "read-matrix"
	}
	if cfg.PayloadBytes <= 0 {
		cfg.PayloadBytes = DefaultSmallFileBytes
	}
	if cfg.PathsPerDepth <= 0 {
		cfg.PathsPerDepth = 1
	}
	depths, err := normalizeDepths(cfg.Depths)
	if err != nil {
		return nil, err
	}
	minFiles := len(depths) * cfg.PathsPerDepth
	if cfg.FileCount == 0 {
		cfg.FileCount = minFiles
	}
	if cfg.FileCount < minFiles {
		return nil, fmt.Errorf("file_count must be at least %d for %d depths", minFiles, len(depths))
	}

	dataset := &MatrixDataset{
		Name:           matrixDatasetName(cfg.Name, depths),
		Depths:         depths,
		FileCount:      cfg.FileCount,
		SmallFileBytes: cfg.PayloadBytes,
		SmallPaths:     make(map[int]string, len(depths)),
		LookupPaths:    make(map[int][]string, len(depths)),
	}
	seen := map[string]struct{}{}
	for _, depth := range depths {
		for sample := 0; sample < cfg.PathsPerDepth; sample++ {
			lookupPath := matrixLookupPath(depth, sample)
			if sample == 0 {
				dataset.SmallPaths[depth] = lookupPath
			}
			dataset.LookupPaths[depth] = append(dataset.LookupPaths[depth], lookupPath)
			dataset.addFile(seen, MatrixDatasetFile{
				Path:  lookupPath,
				Data:  deterministicBytes(fmt.Sprintf("lookup-d%d-s%d", depth, sample), cfg.PayloadBytes),
				Depth: depth,
				Role:  "lookup",
			})
		}
	}
	for i := 0; len(dataset.Files) < cfg.FileCount; i++ {
		depth := depths[i%len(depths)]
		filePath := path.Join(matrixPathAtDepth(depth, fmt.Sprintf("scale-%06d.dat", i)))
		if _, ok := seen[filePath]; ok {
			continue
		}
		dataset.addFile(seen, MatrixDatasetFile{
			Path:  filePath,
			Data:  deterministicBytes(fmt.Sprintf("scale-%d-d%d", i, depth), cfg.PayloadBytes),
			Depth: depth,
			Role:  "scale",
		})
	}
	dataset.DirectoryCount = countDirectories(dataset.Files)
	dataset.PathCount = dataset.FileCount + dataset.DirectoryCount
	return dataset, nil
}

func matrixLookupPath(depth int, sample int) string {
	return matrixPathAtDepth(depth, fmt.Sprintf("lookup-%02d.txt", sample))
}

func matrixPathAtDepth(depth int, filename string) string {
	if depth <= 1 {
		return filename
	}
	parts := make([]string, 0, depth)
	for i := 0; i < depth-1; i++ {
		parts = append(parts, fmt.Sprintf("dir%02d", i))
	}
	parts = append(parts, filename)
	return path.Join(parts...)
}

func (d *MatrixDataset) addFile(seen map[string]struct{}, file MatrixDatasetFile) {
	if _, ok := seen[file.Path]; ok {
		return
	}
	seen[file.Path] = struct{}{}
	d.Files = append(d.Files, file)
	d.LogicalPayloadBytes += int64(len(file.Data))
}

func normalizeDepths(depths []int) ([]int, error) {
	if len(depths) == 0 {
		depths = []int{1, 2, 3, 4, 5, 6}
	}
	out := append([]int(nil), depths...)
	slices.Sort(out)
	out = slices.Compact(out)
	for _, depth := range out {
		if depth < 1 {
			return nil, fmt.Errorf("depths must be >= 1")
		}
	}
	return out, nil
}

func matrixDatasetName(base string, depths []int) string {
	return fmt.Sprintf("%s-depth%d-%d", base, depths[0], depths[len(depths)-1])
}

func countDirectories(files []MatrixDatasetFile) int {
	dirs := map[string]struct{}{}
	for _, file := range files {
		dir := path.Dir(file.Path)
		for dir != "." && dir != "/" && dir != "" {
			dirs[dir] = struct{}{}
			next := path.Dir(dir)
			if next == dir {
				break
			}
			dir = next
		}
	}
	return len(dirs)
}

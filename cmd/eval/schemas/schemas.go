// Package schemas embeds the evaluator JSON schemas for installed binaries.
package schemas

import (
	"embed"
	"fmt"
	"path/filepath"
)

//go:embed *.json
var files embed.FS

// Entry identifies one embedded evaluator JSON schema.
type Entry struct {
	Name string
	Path string
}

// Entries returns schemas known to the evaluator CLI in display form.
func Entries() []Entry {
	return []Entry{
		{Name: "cas-model-result", Path: "cmd/eval/schemas/cas-model-result.schema.json"},
		{Name: "common-record", Path: "cmd/eval/schemas/common-record.schema.json"},
		{Name: "flat-index-cardinality-result", Path: "cmd/eval/schemas/flat-index-cardinality-result.schema.json"},
		{Name: "proof-overhead-result", Path: "cmd/eval/schemas/proof-overhead-result.schema.json"},
		{Name: "read-matrix-result", Path: "cmd/eval/schemas/read-matrix-result.schema.json"},
		{Name: "read-query-result", Path: "cmd/eval/schemas/read-query-result.schema.json"},
		{Name: "readbench-result", Path: "cmd/eval/schemas/readbench-result.schema.json"},
		{Name: "run-manifest", Path: "cmd/eval/schemas/run-manifest.schema.json"},
		{Name: "run-plan", Path: "cmd/eval/schemas/run-plan.schema.json"},
		{Name: "storage-overhead-result", Path: "cmd/eval/schemas/storage-overhead-result.schema.json"},
		{Name: "write-trace-result", Path: "cmd/eval/schemas/write-trace-result.schema.json"},
	}
}

// Read returns one embedded schema by display name.
func Read(name string) ([]byte, error) {
	for _, entry := range Entries() {
		if entry.Name != name {
			continue
		}
		data, err := files.ReadFile(filepath.Base(entry.Path))
		if err != nil {
			return nil, err
		}
		return data, nil
	}
	return nil, fmt.Errorf("unknown schema %q", name)
}

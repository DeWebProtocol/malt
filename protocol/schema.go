package protocol

import (
	"embed"
	"fmt"
	"sort"
)

//go:embed schemas/*.json
var schemaFiles embed.FS

// Schema returns one checked-in serialized contract schema.
func Schema(name string) ([]byte, error) {
	data, err := schemaFiles.ReadFile("schemas/" + name)
	if err != nil {
		return nil, fmt.Errorf("unknown protocol schema %q", name)
	}
	return data, nil
}

// SchemaNames returns the stable checked-in schema filenames.
func SchemaNames() []string {
	entries, _ := schemaFiles.ReadDir("schemas")
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

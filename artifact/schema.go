package artifact

import (
	"embed"
	"fmt"
)

//go:embed schemas/*.schema.json
var schemaFS embed.FS

// Schema returns a checked-in JSON Schema by stable filename.
func Schema(name string) ([]byte, error) {
	data, err := schemaFS.ReadFile("schemas/" + name)
	if err != nil {
		return nil, fmt.Errorf("artifact schema %q: %w", name, err)
	}
	return append([]byte(nil), data...), nil
}

// SchemaNames lists the schemas published by this artifact profile.
func SchemaNames() []string {
	return []string{
		"artifact.schema.json",
		"local-verify-request.schema.json",
		"local-verify-result.schema.json",
		"prove-request.schema.json",
		"resolve-request.schema.json",
		"verify-request.schema.json",
		"verify-result.schema.json",
	}
}

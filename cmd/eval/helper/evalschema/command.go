// Package evalschema exposes evaluator JSON schemas through the CLI.
package evalschema

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"

	evalschemas "github.com/dewebprotocol/malt/cmd/eval/schemas"
	"github.com/spf13/cobra"
)

// Schema identifies one evaluator JSON schema file.
type Schema struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	Content []byte `json:"-"`
}

// DefaultSchemas returns schemas known to the evaluator CLI.
func DefaultSchemas() []Schema {
	entries := evalschemas.Entries()
	schemas := make([]Schema, 0, len(entries))
	for _, entry := range entries {
		content, err := evalschemas.Read(entry.Name)
		if err != nil {
			panic(err)
		}
		schemas = append(schemas, Schema{
			Name:    entry.Name,
			Path:    entry.Path,
			Content: content,
		})
	}
	return schemas
}

// NewCommand creates `malt-eval schema`.
func NewCommand() *cobra.Command {
	return NewCommandWithSchemas(os.Stdout, DefaultSchemas())
}

// NewCommandWithOutput creates a schema command using the default embedded
// schema registry and a caller-supplied output writer.
func NewCommandWithOutput(out io.Writer) *cobra.Command {
	return NewCommandWithSchemas(out, DefaultSchemas())
}

// NewCommandWithSchemas creates a schema command for tests or custom registries.
func NewCommandWithSchemas(out io.Writer, schemas []Schema) *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "schema",
		Short: "List or print evaluator JSON schemas",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return run(out, schemas, name)
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Print a schema by name instead of listing all schemas")
	return cmd
}

func run(out io.Writer, schemas []Schema, name string) error {
	if out == nil {
		return fmt.Errorf("output writer is nil")
	}
	ordered := append([]Schema(nil), schemas...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	if name == "" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(ordered)
	}
	for _, schema := range ordered {
		if schema.Name != name {
			continue
		}
		data := schema.Content
		if data == nil {
			var err error
			data, err = os.ReadFile(schema.Path)
			if err != nil {
				return err
			}
		}
		_, err := out.Write(data)
		return err
	}
	return fmt.Errorf("unknown schema %q", name)
}

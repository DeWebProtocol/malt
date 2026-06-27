package evalsuites

import "testing"

func TestNewRegistryRegistersProductionSuites(t *testing.T) {
	registry := NewRegistry()
	for _, name := range []string{
		"write_trace",
		"read_matrix",
		"read_query",
		"cas_model",
		"proof_overhead",
		"storage_overhead",
	} {
		if suite, ok := registry.Lookup(name); !ok || suite.Name() != name {
			t.Fatalf("suite %q not registered: suite=%v ok=%v", name, suite, ok)
		}
	}
}

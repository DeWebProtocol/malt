package main

import (
	"github.com/dewebprotocol/malt-core/auth/input"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	"testing"
)

func TestFileSnapshotsUseRuntimeSchemaAndRetainHistory(t *testing.T) {
	data := []byte(`[{"files":{"dir/file":"aGVsbG8gd29ybGQ="},"queries":["dir/file","missing"]},{"files":{},"queries":["dir/file"]}]`)
	out, err := compile(t.Context(), data, "ipa", 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Queries) != 3 || out.Queries[0].Root == out.Queries[2].Root {
		t.Fatal("history lost")
	}
	positional := false
	for _, c := range out.Candidates {
		if c.State.Descriptor.Layout == maltcid.Positional {
			positional = true
			for _, e := range c.State.Entries {
				if e.Input.Kind == input.System {
					t.Fatal("positional system")
				}
			}
		} else {
			for _, e := range c.State.Entries {
				if e.Input.Kind == input.Label && string(e.Input.Data) == "dir/file" {
					t.Fatal("flattened alias")
				}
			}
		}
	}
	if !positional {
		t.Fatal("large file skipped measured sequence")
	}
	if _, err = compile(t.Context(), []byte(`[{"files":{"a":"","a/b":""},"queries":["a"]}]`), "ipa", 4); err == nil {
		t.Fatal("accepted collision")
	}
}

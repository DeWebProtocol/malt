package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/dewebprotocol/malt-core/auth/arcset/materializer/memory"
	"github.com/dewebprotocol/malt-core/auth/commitment/ipa"
	"github.com/dewebprotocol/malt-core/auth/engine"
	"github.com/dewebprotocol/malt-core/auth/input"
	"github.com/dewebprotocol/malt-core/protocol"
	"github.com/dewebprotocol/malt-core/sdk/authentication"
	"github.com/dewebprotocol/malt-core/wire/maltcid"
	cid "github.com/ipfs/go-cid"
	"github.com/spf13/cobra"
)

func TestVerifyCommandBindsTypedResultToSelectedRequest(t *testing.T) {
	scheme, err := ipa.NewCommitterScheme(ipa.ProfileDirect)
	if err != nil {
		t.Fatal(err)
	}
	profiles := engine.NewRegistry()
	if err := profiles.Register(scheme); err != nil {
		t.Fatal(err)
	}
	e := engine.New(input.DefaultRegistry(), profiles)
	nodes := memory.NewNodes()
	root, err := e.Build(t.Context(), engine.State{Descriptor: maltcid.RootDescriptor{Layout: maltcid.Prefix, InputRule: uint8(input.BytesSHA256), Profile: maltcid.IPA256}, Entries: []engine.Entry{{Input: input.LabelValue([]byte("docs/file")), Target: cid.MustParse("bafkqaaa")}}}, nodes)
	if err != nil {
		t.Fatal(err)
	}
	q := protocol.AuthenticationRequest{Profile: protocol.AuthenticationPathProfile, Root: root.String(), Steps: []input.Value{input.LabelValue([]byte("docs/file"))}, Operation: "resolve"}
	result, err := authentication.Execute(t.Context(), e, q, nodes)
	if err != nil {
		t.Fatal(err)
	}
	resultJSON, _ := json.Marshal(result)
	for _, test := range []struct {
		name   string
		change func(*protocol.AuthenticationRequest)
		valid  bool
	}{
		{"valid", func(*protocol.AuthenticationRequest) {}, true},
		{"changed label", func(q *protocol.AuthenticationRequest) { q.Steps = []input.Value{input.LabelValue([]byte("other"))} }, false},
		{"changed operation", func(q *protocol.AuthenticationRequest) {
			q.Operation = "binding"
			value := input.LabelValue([]byte("docs/file"))
			q.Input = &value
		}, false},
		{"invalid root", func(q *protocol.AuthenticationRequest) { q.Root = "bafkqaaa" }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := q
			test.change(&request)
			requestJSON, _ := json.Marshal(request)
			path := filepath.Join(t.TempDir(), "request.json")
			if err := os.WriteFile(path, requestJSON, 0600); err != nil {
				t.Fatal(err)
			}
			cmd := &cobra.Command{}
			cmd.SetContext(t.Context())
			cmd.Flags().String("request", path, "")
			cmd.Flags().String("result", "-", "")
			cmd.SetIn(bytes.NewReader(resultJSON))
			var output bytes.Buffer
			cmd.SetOut(&output)
			err := runVerify(cmd, nil)
			if test.valid && (err != nil || output.String() != "valid: true\n") {
				t.Fatalf("verification: %q %v", output.String(), err)
			}
			if !test.valid && err == nil {
				t.Fatal("unbound result accepted")
			}
		})
	}
}

package protocol_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dewebprotocol/malt/auth/proof/prooflist"
	"github.com/dewebprotocol/malt/protocol"
)

func TestDecodeResolveVerificationStrict(t *testing.T) {
	root := protocolTestCID(t, "strict-resolve-root")
	value := protocol.ResolveVerification{
		Request: protocol.ResolveRequest{Profile: protocol.ResolveProfile, Root: root.String(), Segments: []string{}},
		Result: protocol.ResolveResult{
			Profile: protocol.ResolveProfile,
			Target:  root.String(),
			ProofList: prooflist.ProofList{
				Root:  root,
				Steps: []prooflist.Step{},
			},
		},
	}
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.DecodeResolveVerification(raw); err != nil {
		t.Fatalf("DecodeResolveVerification: %v", err)
	}

	withUnknown := strings.TrimSuffix(string(raw), "}") + `,"unexpected":true}`
	if _, err := protocol.DecodeResolveVerification([]byte(withUnknown)); err == nil {
		t.Fatal("DecodeResolveVerification accepted an unknown field")
	}
	if _, err := protocol.DecodeResolveVerification(append(raw, []byte("{}")...)); err == nil {
		t.Fatal("DecodeResolveVerification accepted a trailing JSON value")
	}
}

func TestDecodeReadVerificationRejectsUnknownNestedField(t *testing.T) {
	root := protocolTestCID(t, "strict-read-root")
	target := protocolTestCID(t, "strict-read-target")
	raw := []byte(`{
		"request":{
			"profile":"malt.read/v0alpha1",
			"root":"` + root.String() + `",
			"query":{"kind":"map_key","segments":["name"],"unexpected":true}
		},
		"result":{
			"profile":"malt.read/v0alpha1",
			"target":"` + target.String() + `",
			"prooflist":{"root":{"/":"` + root.String() + `"},"steps":[]}
		}
	}`)
	if _, err := protocol.DecodeReadVerification(raw); err == nil {
		t.Fatal("DecodeReadVerification accepted an unknown nested field")
	}
}

func TestDecodeVerificationRejectsEmptyAndOversizedInputs(t *testing.T) {
	if _, err := protocol.DecodeResolveVerification(nil); err == nil {
		t.Fatal("DecodeResolveVerification accepted an empty input")
	}
	oversized := make([]byte, protocol.MaxVerificationJSONBytes+1)
	if _, err := protocol.DecodeReadVerification(oversized); err == nil {
		t.Fatal("DecodeReadVerification accepted an oversized input")
	}
}
